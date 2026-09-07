# Retained failed `mise run check` evidence

This directory preserves the raw check output from `/tmp/entire-graph-8763b0d8-mise-check.txt` without rewriting it. The check failed in `internal/sem` at `TestTreeSitterParserYAMLMasksQuotedMappingKeys` (`parser_test.go:1204`) after the statusline suite reported 151 passing tests and several Go packages completed.

The manifest records the command, logged stage commands, raw-log hash, original source-log timestamp, retained-copy timestamp, and failure detail. Stage scheduling/order is not inferred from the interleaved output. The `1056.44s` duration is retained as the worker-reported value from the log.

The current checkout contains uncommitted parser and documentation modifications observed when this evidence was preserved. The retained check has no before/after source snapshot or exact source-tree identity, so the evidence makes no unchanged-source or immutable-gate claim and makes no temporal attribution of those modifications to the failed check.

No post-check rerun is included. This is preservation evidence only.
