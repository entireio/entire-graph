# ADR: extraction persistence identity and admission

Status: accepted for experimental opt-in implementation
Date: 2026-09-05

Use a separate per-repository extraction namespace. Keys length-prefix the
repository identity, exact path bytes, captured SHA-256, language, profile,
resolved input limit, exact extraction version and executing binary SHA-256.
The binary digest includes local parser/grammar changes even for dirty development
builds; a version label alone is insufficient. Failure to establish it disables
reuse. Binary hashing is performed once per process and is charged to latency.

Entries contain private declaration metadata, not stable IDs or cross-file
resolution. The graph is always rebuilt. Decode is bounded to 64 MiB, exact-version
and key checked, with corruption treated as a miss. Only complete successful
extractions are persisted in the initial implementation; syntax errors are
recomputed conservatively, as are timeouts and resource failures.

Use the repository's held-root atomic writer. Cleanup visits at most 100001
entries, refuses redirected directories, and retains at most 1 GiB / 100000
entries per repository. Unexpected entries are never followed or removed.
Concurrent processes may temporarily overshoot by their bounded in-flight
writes; the next maintenance pass converges. Do not delete user-selected roots.

No relation family is implicitly present. Initial declarations do not claim to
avoid resolver parsing; relation-input reuse requires separate measured admission.
Defaults remain off and performance gates remain mandatory before promotion.

Entry payloads carry a SHA-256 checksum verified after bounded decode. Reads
also refuse redirected descendants, without creating directories. Publication
requires an exact in-memory payload round trip: invalid UTF-8 strings cannot be
silently replaced by JSON encoding. Such inputs bypass persistence. Quota
maintenance reserves conservative bytes per operation and evicts toward 90% in
bounded batches, avoiding a directory scan for every file write.
