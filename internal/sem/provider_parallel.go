package sem

import (
	"context"
	"fmt"
	"runtime"
	"sync"
)

// providerFileResult is the ordered handoff between independent file workers
// and the single snapshot reducer. More parse products are added by the
// provider without changing the scheduling contract.
type providerFileResult struct {
	index                 int
	path                  string
	file                  *FileRecord
	language              string
	symbols               []SymbolRecord
	precomputedImports    []string
	hasPrecomputedImports bool
	parsed                bool
	failures              []PartialFailure
}

type providerFileJob struct {
	index int
	path  string
}

func defaultProviderWorkerCount() int {
	return boundedProviderWorkerCount(runtime.GOMAXPROCS(0))
}

func boundedProviderWorkerCount(maxProcs int) int {
	if maxProcs < 1 {
		return 1
	}
	if maxProcs > 8 {
		return 8
	}
	return maxProcs
}

// processProviderFile is the intentionally narrow per-file seam. It owns the
// content read, classification, and parse, but no shared graph state and no
// emission; a later content-reuse layer can replace the read at this boundary
// without changing the deterministic reducer.
func processProviderFile(
	ctx context.Context,
	gate budgetGate,
	sc sourceContext,
	spec profileSpec,
	maxParseBytes int,
	index int,
	path string,
) providerFileResult {
	result := providerFileResult{index: index, path: path}
	// The per-file stop condition is the worker context OR the wall-clock
	// budget. It cannot be ctx alone: context.WithDeadline reports expiry
	// through a runtime timer, so ctx.Err() flips one timer granularity after
	// the deadline actually passed (~15.6 ms on Windows). Inside that window
	// the pipeline keeps handing out files and this function reads, parses and
	// RETURNS them, so the reducer emits file and symbol records dated after
	// the advertised ceiling. gate.expired() compares the clock to the deadline
	// directly, so the answer is true the instant the budget is gone on every
	// platform. ctx stays in the disjunction because the pipeline's own
	// cancellation (a reduce error, or shutdown) travels only on it.
	stop := func() bool { return ctx.Err() != nil || gate.expired() }
	if stop() {
		return result
	}
	// Path-based routing first; files the path cannot classify (extensionless
	// executables like pyenv's libexec/* scripts) get one bounded prefix read
	// to route by shebang before being declared unsupported.
	if !Supported(path) && !shebangRoutable(sc.readPrefix, path) {
		if hint := unsupportedLanguageHint(path); hint != "" {
			result.failures = append(result.failures, PartialFailure{
				Code:                 "E_UNSUPPORTED_LANGUAGE",
				Severity:             "warning",
				FilePath:             path,
				EffectOnCompleteness: "file omitted because no parser is available",
				Detail:               hint,
			})
		}
		return result
	}

	// Checked BEFORE the read, not by wrapping sc.read in gate.reader. A
	// refused read is reported below as E_FILE_READ ("file listed but content
	// was unavailable"), which is the signature of a corrupt or vanished
	// source; routing a budget expiry into it would make a truncated run
	// indistinguishable from a broken repository. Stopping here instead drops
	// the file with no failure record, which is exactly what an expiry during
	// the parse already does, and the run still carries the single
	// E_ANALYSIS_BUDGET_EXCEEDED marker that says why files are missing.
	//
	// This bounds the number of reads STARTED after expiry to zero. It does not
	// bound a read already in flight -- sc.read is synchronous and takes no
	// context -- which is the residual the PR discloses as one in-flight file
	// per worker.
	if stop() {
		return providerFileResult{index: index, path: path}
	}
	content, ok := sc.read(path)
	// sc.read is the one synchronous, unbudgeted step this function cannot
	// interrupt mid-flight (see the comment above), but once it returns,
	// everything below it -- the oversize branch immediately following,
	// E_FILE_TOO_LARGE, E_MINIFIED -- populates a file record and returns
	// without touching the parser or ctx again, so none of those early
	// returns observed a budget that expired while the read was in flight.
	// Rechecking here, before any of them run, keeps every populated return
	// between the read and the parse subject to the same ceiling the
	// pre-read and post-parse checks already enforce.
	if stop() {
		return providerFileResult{index: index, path: path}
	}
	if !ok {
		// A refused read is not a failed one: the reader declines files above
		// the byte cap so no single file can set the snapshot's memory ceiling.
		if over, isOversize := sc.oversizeAt(path); isOversize {
			langSpec, langOK := languageForPath(path)
			if !langOK {
				if prefix, prefixOK := sc.readPrefix(path, shebangSniffLimit); prefixOK {
					langSpec, langOK = languageForShebang(prefix)
				}
			}
			if !langOK {
				result.failures = append(result.failures, PartialFailure{
					Code:                 "E_UNSUPPORTED_LANGUAGE",
					Severity:             "warning",
					FilePath:             path,
					EffectOnCompleteness: "file omitted because no parser is available",
				})
				return result
			}
			language := langSpec.language
			result.language = language
			result.file = &FileRecord{
				RecordType: "file",
				ID:         fileID(sc.key, path),
				Path:       path,
				Blob:       over.Hash,
				Language:   language,
				Bytes:      int(over.Bytes),
				Lines:      over.Lines,
			}
			result.failures = append(result.failures, PartialFailure{
				Code:                 "E_FILE_TOO_LARGE",
				Severity:             "warning",
				FilePath:             path,
				EffectOnCompleteness: "file record emitted but symbol parsing skipped",
				Detail: fmt.Sprintf(
					"file is %d bytes, above max parser input %d bytes; content was never held in memory",
					over.Bytes, maxParseBytes,
				),
			})
			return result
		}
		result.failures = append(result.failures, PartialFailure{
			Code:                 "E_FILE_READ",
			Severity:             "error",
			FilePath:             path,
			EffectOnCompleteness: "file omitted from semantic snapshot",
			Detail:               "file listed but content was unavailable",
		})
		return result
	}

	contentBytes := []byte(content)
	if stop() {
		return providerFileResult{index: index, path: path}
	}
	langSpec, ok := languageForContent(path, content)
	if !ok {
		result.failures = append(result.failures, PartialFailure{
			Code:                 "E_UNSUPPORTED_LANGUAGE",
			Severity:             "warning",
			FilePath:             path,
			EffectOnCompleteness: "file omitted because no parser is available",
		})
		return result
	}
	language := langSpec.language
	if language == "Go" && !goFileMatchesDefaultBuild(path, content) {
		return result
	}
	file := FileRecord{
		RecordType: "file",
		ID:         fileID(sc.key, path),
		Path:       path,
		Blob:       contentHash(contentBytes),
		Language:   language,
		Bytes:      len(contentBytes),
		Lines:      sourceLineCount(content),
	}
	if skipFastProfilePerSymbolScan(spec, language) {
		if stop() {
			return providerFileResult{index: index, path: path}
		}
		result.precomputedImports = importsFor(path, content)
		result.hasPrecomputedImports = true
	}
	if maxParseBytes > 0 && len(contentBytes) > maxParseBytes {
		result.language = language
		result.file = &file
		result.failures = append(result.failures, PartialFailure{
			Code:                 "E_FILE_TOO_LARGE",
			Severity:             "warning",
			FilePath:             path,
			EffectOnCompleteness: "file record emitted but symbol parsing skipped",
			Detail:               fmt.Sprintf("file is %d bytes, above max parser input %d bytes", len(contentBytes), maxParseBytes),
		})
		return result
	}
	if stop() {
		return providerFileResult{index: index, path: path}
	}
	if looksMinified(content) {
		result.language = language
		result.file = &file
		result.failures = append(result.failures, PartialFailure{
			Code:                 "E_MINIFIED",
			Severity:             "warning",
			FilePath:             path,
			EffectOnCompleteness: "file record emitted but symbol parsing skipped",
			Detail:               "file appears minified/bundled (very long lines); not analyzed as source",
		})
		return result
	}

	entities, parsedLanguage, parseStatus := parseWithProfile(ctx, TreeSitterParser{}, spec, langSpec, path, content)
	// The parse and the entity walk both observe ctx now, so a budget that
	// expires mid-file returns a PARTIAL entity set. Truncation is file-atomic:
	// drop the file entirely rather than let the reducer emit a file record with
	// a silently short symbol list that reads as complete.
	if stop() {
		return providerFileResult{index: index, path: path}
	}
	if parsedLanguage == "" {
		result.failures = append(result.failures, PartialFailure{
			Code:                 "E_UNSUPPORTED_LANGUAGE",
			Severity:             "warning",
			FilePath:             path,
			EffectOnCompleteness: "file omitted because no parser is available",
		})
		return result
	}
	if parseStatus.ParseError {
		code := parseStatus.Code
		if code == "" {
			code = "E_PARSE_ERROR"
		}
		effect := "file parsed with syntax errors; semantic facts may be incomplete"
		if code == "E_PARSE_TIMEOUT" {
			effect = "file record emitted but symbol parsing skipped because parser time budget was exceeded"
		}
		result.failures = append(result.failures, PartialFailure{
			Code:                 code,
			Severity:             "warning",
			FilePath:             path,
			EffectOnCompleteness: effect,
			Detail:               parseStatus.Detail,
		})
	}
	file.Language = parsedLanguage
	result.language = parsedLanguage
	result.file = &file
	result.parsed = true
	result.symbols = entitySymbols(sc.key, path, parsedLanguage, entities)
	// syntheticBoundarySymbols rescans file content once per already-parsed
	// symbol (route/tool/workflow boundary detection), which is superlinear in
	// files with very large symbol counts. stop is polled inside its loop, so
	// a deadline expiring mid-scan is caught within one poll stride rather
	// than only after the whole pass finishes. A stop here truncates like the
	// post-parse check above: the file is dropped whole rather than emitted
	// with entity symbols but no synthetic ones, which would read as complete.
	synthetic, truncated := syntheticBoundarySymbols(sc.key, path, parsedLanguage, content, result.symbols, stop)
	if truncated {
		return providerFileResult{index: index, path: path}
	}
	result.symbols = append(result.symbols, synthetic...)
	return result
}

// runProviderFilePipeline processes paths concurrently but reduces results in
// the exact input order. The coordinator admits at most twice the worker count
// of results that have not yet been reduced.
func runProviderFilePipeline(
	ctx context.Context,
	paths []string,
	workers int,
	process func(context.Context, int, string) providerFileResult,
	reduce func(providerFileResult) error,
) error {
	if len(paths) == 0 {
		return ctx.Err()
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(paths) {
		workers = len(paths)
	}

	workerCtx, cancel := context.WithCancel(ctx)
	jobs := make(chan providerFileJob)
	results := make(chan providerFileResult, workers)
	var workerGroup sync.WaitGroup
	workerGroup.Add(workers)
	for range workers {
		go func() {
			defer workerGroup.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					result := process(workerCtx, job.index, job.path)
					select {
					case results <- result:
					case <-workerCtx.Done():
						return
					}
				}
			}
		}()
	}
	defer func() {
		cancel()
		close(jobs)
		workerGroup.Wait()
	}()

	limit := 2 * workers
	nextSubmit, nextReduce, outstanding := 0, 0, 0
	pending := make(map[int]providerFileResult, limit)
	for nextReduce < len(paths) {
		var submit chan<- providerFileJob
		var job providerFileJob
		if nextSubmit < len(paths) && outstanding < limit {
			submit = jobs
			job = providerFileJob{index: nextSubmit, path: paths[nextSubmit]}
		}
		select {
		case <-ctx.Done():
			for {
				reduced := false
				for {
					ordered, ok := pending[nextReduce]
					if !ok {
						break
					}
					if err := reduce(ordered); err != nil {
						return err
					}
					delete(pending, nextReduce)
					nextReduce++
					outstanding--
					reduced = true
				}
				gotResult := false
				select {
				case result := <-results:
					pending[result.index] = result
					gotResult = true
				default:
				}
				if !reduced && !gotResult {
					break
				}
			}
			return ctx.Err()
		case submit <- job:
			nextSubmit++
			outstanding++
		case result := <-results:
			pending[result.index] = result
			for {
				ordered, ok := pending[nextReduce]
				if !ok {
					break
				}
				if err := reduce(ordered); err != nil {
					return err
				}
				delete(pending, nextReduce)
				nextReduce++
				outstanding--
			}
		}
	}
	return nil
}
