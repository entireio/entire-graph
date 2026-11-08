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
	// The worker context carries both the caller's cancellation and the
	// pipeline's own (a reduce error, or shutdown). Every stop below drops the
	// file whole; the pipeline then returns the context's error, so nothing a
	// cancelled run half-read or half-parsed is ever emitted.
	stop := func() bool { return ctx.Err() != nil }
	if stop() {
		return result
	}
	var routedLanguage languageSpec
	var routedLanguageOK bool
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
		if sc.readPrefix != nil {
			if prefix, prefixOK := sc.readPrefix(path, shebangSniffLimit); prefixOK {
				routedLanguage, routedLanguageOK = languageForShebang(prefix)
			}
		}
		if !routedLanguageOK {
			if over, isOversize := sc.oversizeAt(path); isOversize && over.Prefix != "" {
				routedLanguage, routedLanguageOK = languageForShebang(over.Prefix)
				if routedLanguageOK {
					routedOversize = &over
				}
			}
		}
		if !routedLanguageOK {
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

	// Checked BEFORE the read, not by wrapping sc.read in cancellableReader. A
	// refused read is reported below as E_FILE_READ ("file listed but content
	// was unavailable"), which is the signature of a corrupt or vanished
	// source; a cancellation is not that, so it drops the file with no failure
	// record instead.
	//
	// This bounds the number of reads STARTED after cancellation to zero. It
	// does not bound a read already in flight: sc.read is synchronous and takes
	// no context.
	if stop() {
		return providerFileResult{index: index, path: path}
	}
	var content string
	var ok bool
	if routedOversize == nil {
		content, ok = sc.read(path)
	}
	// sc.read is the one synchronous step this function cannot interrupt
	// mid-flight (see the comment above), but once it returns, everything below
	// it -- the oversize branch immediately following, E_FILE_TOO_LARGE,
	// E_MINIFIED -- populates a file record and returns without touching the
	// parser or ctx again. Rechecking here keeps a cancellation that landed
	// during the read from still producing one of those records.
	if stop() {
		return providerFileResult{index: index, path: path}
	}
	if !ok {
		// A refused read is not a failed one: the reader declines files above
		// the byte cap so no single file can set the snapshot's memory ceiling.
		var over oversizeFile
		var isOversize bool
		if routedOversize != nil {
			over, isOversize = *routedOversize, true
		} else {
			over, isOversize = sc.oversizeAt(path)
		}
		if isOversize {
			langSpec, langOK := languageForPath(path)
			if !langOK && routedLanguageOK {
				langSpec, langOK = routedLanguage, true
			} else if !langOK && over.Prefix != "" {
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
			Code:     "E_FILE_READ",
			Severity: "error",
			FilePath: path,
			// Preserve shebang routing when the full read fails and there is
			// no file record or filename extension to recover the language.
			Language:             routedLanguage.language,
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
	// The parse and the entity walk both observe ctx, so a cancellation that
	// lands mid-file returns a PARTIAL entity set. Drop the file entirely rather
	// than hand the reducer a file record with a silently short symbol list.
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
	// Partial is included so a status whose output is valid but incomplete is
	// still reported. It reaches completeness as a COUNTED failure: unlike
	// E_FILE_TOO_LARGE/E_MINIFIED, which are in intentionalSkipFailureCodes
	// because the parser never opened the file, a partial result means the graph
	// tried, succeeded in part, and is missing declarations it should have had.
	if parseStatus.ParseError || parseStatus.Partial {
		code := parseStatus.Code
		if code == "" {
			code = "E_PARSE_ERROR"
		}
		effect := "file parsed with syntax errors; semantic facts may be incomplete"
		switch code {
		case "E_PARSE_TIMEOUT":
			effect = "file record emitted but symbol parsing skipped because parser time budget was exceeded"
		case "E_PARSE_DEPTH_EXCEEDED":
			effect = "file record and symbols above the parser depth limit emitted; more deeply nested declarations were not walked, so this file counts against completeness"
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
	// syntheticBoundarySymbols visits every already-parsed symbol
	// (route/tool/workflow boundary detection), which grows with a file's
	// symbol count. stop is polled inside its loop, so
	// a cancellation landing mid-scan is caught within one poll stride rather
	// than only after the whole pass finishes. A stop here drops the file whole,
	// like the post-parse check above.
	synthetic, truncated := syntheticBoundarySymbols(sc.key, path, parsedLanguage, content, result.symbols, stop)
	if truncated {
		return providerFileResult{index: index, path: path}
	}
	result.symbols = append(result.symbols, synthetic...)
	return result
}

// workerStop reports why a pipeline worker must abandon the file it is on.
//
// A worker cannot see the reducer's stop decision -- the reducer runs on the
// coordinator, and its error is what unwinds the pipeline -- so it has to ask
// the two things it can see. canceled is non-nil once the pipeline has canceled
// the worker context, which happens on the way out of runIndexedPipeline: no
// result produced from here on is ever reduced, so finishing the file only
// delays the deferred worker join and pushes the run further past its budget.
// budget is true once the wall-clock budget has run out, which is provably
// wasted work rather than a decision: the reducer re-checks the same monotonic
// deadline in file order at a strictly later moment, so it will stop at this
// index whatever this file returns. Neither can move the truncated set, so
// abandoning changes only how much work is thrown away.
//
// overBudget may be nil for a pipeline with no budget.
func workerStop(ctx context.Context, overBudget func() bool) (canceled error, budget bool) {
	if err := ctx.Err(); err != nil {
		return err, false
	}
	if overBudget == nil {
		return nil, false
	}
	return nil, overBudget()
}

// runProviderFilePipeline processes paths concurrently but reduces results in
// the exact input order. The coordinator admits at most twice the worker count
// of results that have not yet been reduced.
//
// stop is polled immediately before every reduction and may be nil, which means
// "the context is the only stop". It exists because reducing is not free: the
// provider's reducer emits one file record plus that file's entire symbol list
// into the caller's sink, and the coordinator's select can pick a ready result
// over a closed Done channel, so without the poll a cancelled run could still
// emit the results already buffered behind the one in flight. Reduction stays
// atomic per file: the check is between results, never inside one.
func runProviderFilePipeline(
	ctx context.Context,
	stop func() error,
	paths []string,
	workers int,
	process func(context.Context, int, string) providerFileResult,
	reduce func(providerFileResult) error,
) error {
	reduceOne := reduce
	if stop != nil {
		reduceOne = func(result providerFileResult) error {
			if err := stop(); err != nil {
				return err
			}
			return reduce(result)
		}
	}
	return runIndexedPipeline(ctx, len(paths), workers,
		func(workerCtx context.Context, index int) providerFileResult {
			return process(workerCtx, index, paths[index])
		},
		func(_ int, result providerFileResult) error { return reduceOne(result) },
	)
}

// runIndexedPipeline processes count items concurrently and reduces the results
// in exact index order, so the work is parallel and the output is not. Both
// parsing and analysis phases run on it; relations use bounded streaming below.
//
// Ordering is the whole contract. reduce sees index 0, then 1, and so on, no
// matter which worker finishes first, so worker timing cannot reach the emitted
// bytes. process must not touch anything reduce touches.
//
// The coordinator admits at most twice the worker count of results that have not
// yet been reduced, which bounds what a slow reducer, or one item far behind its
// neighbours, can leave buffered.
func runIndexedPipeline[T any](
	ctx context.Context,
	count, workers int,
	process func(ctx context.Context, index int) T,
	reduce func(index int, result T) error,
) error {
	if count == 0 {
		return ctx.Err()
	}
	if workers < 1 {
		workers = 1
	}
	if workers > count {
		workers = count
	}

	type indexed struct {
		index  int
		result T
	}

	workerCtx, cancel := context.WithCancel(ctx)
	jobs := make(chan int)
	results := make(chan indexed, workers)
	var workerGroup sync.WaitGroup
	workerGroup.Add(workers)
	for range workers {
		go func() {
			defer workerGroup.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					out := indexed{index: index, result: process(workerCtx, index)}
					select {
					case results <- out:
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
	pending := make(map[int]T, limit)
	for nextReduce < count {
		var submit chan<- int
		job := nextSubmit
		if nextSubmit < count && outstanding < limit {
			submit = jobs
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case submit <- job:
			nextSubmit++
			outstanding++
		case out := <-results:
			pending[out.index] = out.result
			for {
				ordered, ok := pending[nextReduce]
				if !ok {
					break
				}
				if err := reduce(nextReduce, ordered); err != nil {
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
