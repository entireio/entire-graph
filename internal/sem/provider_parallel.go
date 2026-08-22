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
	sc sourceContext,
	spec profileSpec,
	maxParseBytes int,
	index int,
	path string,
) providerFileResult {
	result := providerFileResult{index: index, path: path}
	if ctx.Err() != nil {
		return result
	}
	var routedOversize *oversizeFile
	// Path-based routing first; files the path cannot classify (extensionless
	// executables like pyenv's libexec/* scripts) get one bounded prefix read
	// to route by shebang before being declared unsupported. Git blob reads
	// are all-or-nothing, so an oversized committed blob can never satisfy
	// that bounded prefix read directly; route it instead from the prefix
	// already captured for free while streaming its digest, when the source
	// kept one, so it does not fall through to "unsupported" purely because
	// of its size.
	if !Supported(path) {
		routable := shebangRoutable(sc.readPrefix, path)
		if !routable {
			if over, isOversize := sc.oversizeAt(path); isOversize && over.Prefix != "" {
				_, routable = languageForShebang(over.Prefix)
				if routable {
					routedOversize = &over
				}
			}
		}
		if !routable {
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
	}

	var content string
	var ok bool
	if routedOversize == nil {
		content, ok = sc.read(path)
	}
	if !ok {
		// A refused read is not a failed one: the reader declines files above
		// the byte cap so no single file can set the snapshot's memory ceiling.
		over, isOversize := sc.oversizeAt(path)
		if routedOversize != nil {
			over, isOversize = *routedOversize, true
		}
		if isOversize {
			langSpec, langOK := languageForPath(path)
			if !langOK && over.Prefix != "" {
				// The ordinary bounded prefix read is doomed here for the same
				// reason the read above was refused (Git blob reads are
				// all-or-nothing); reuse the prefix already captured while
				// streaming this blob's digest instead of retrying it.
				langSpec, langOK = languageForShebang(over.Prefix)
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

	entities, parsedLanguage, parseStatus := parseWithProfile(TreeSitterParser{}, spec, langSpec, path, content)
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
	result.symbols = append(result.symbols, syntheticBoundarySymbols(sc.key, path, parsedLanguage, content, result.symbols)...)
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
