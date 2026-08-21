# Native Prose-Session Context Implementation Plan

> [!WARNING]
> Archived on 2026-08-13. This implementation plan was completed and is not a
> current task list. See the [current documentation index](../README.md).

1. Add a failing repository-search test with four Markdown session parents. The
   top session must rank correctly while its answer-bearing fact sits outside the
   current focused snippet. Require the default result to contain that fact and
   carry the `head-window` signal.
2. Preserve the existing code-search byte golden in the same focused test run.
3. Add a small resolver in semantic search: explicit head-window configuration
   wins; otherwise `retrieval_mode=prose-parent` selects an 80-line window; all
   other searches retain zero/off.
4. Run gofmt, the focused prose tests, `go test ./internal/sem`, `go test ./...`,
   and `go vet ./...`.
5. Build a pinned candidate binary and run no-API corpus probes to confirm the
   same top-k session membership, larger bounded native passages, deterministic
   output, and budget compliance. Forward the sealed shared byte ceiling to the
   native Entire search command in the isolated parity harness, with a regression
   test that checks the exact value.
6. Run the fixed development/tune benchmark without touching comparators,
   prompts, readers, graders, or selectors. Iterate only from tune evidence.
7. Freeze a new protocol and disjoint untouched holdout, then run and score it
   once. Report losses honestly and do not tune against the holdout.
8. Only after a qualifying holdout, run the full reconstructed 300/50 suite and
   prepare separate unmerged Entire Graph and GraphMark PRs.
