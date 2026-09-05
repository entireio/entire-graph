# ADR 0037: captured operation provenance and failures

Status: accepted for explicit capture modes
Date: 2026-09-05

Decision: publish an optional `operation_inputs` manifest after all operation reads.
Its length-prefixed SHA-256 identity binds requested view and observed Git identity,
ordered selected paths, captured ordered policy bytes/presence, effective source
profile/limits, acquisition outcomes and listing warnings. All observed inputs
contribute to identity; at most 256 path observations are printed, with omitted
and unobserved counts explicit. Successful empty content differs from unavailable
content. Oversized observations carry their captured digest. The manifest describes
observations and coverage, never an atomic filesystem revision or complete graph.
Streaming output places this final field in the summary; accumulated snapshots
and search place it in the header/response. Default modes remain unchanged.

Storage acquisition/backing errors remain sticky through later resolver and renderer
reads. Operations fail before their final success result/summary if such an error
occurred. Ordinary unavailable file reads retain existing partial reporting and
are also represented in capture coverage. Context cancellation never emits a
completed manifest. Error output uses a stable safe code without temporary paths.

The 64 MiB memory limit applies to retained capture-store payload, not total RSS;
active parser buffers, rehydration and consumer copies have separate bounds.
Alternatives: per-file hashes alone omit policy and scope; a HEAD key misrepresents
working-tree capture; swallowing backing failures can turn missing edges into a
false complete result. Tests distinguish missing/empty, policy/scope/options,
mutation after capture, bounded manifest output and backing failure after first read.
