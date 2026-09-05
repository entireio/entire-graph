# ADR: first cached raw relation family

Status: accepted, experimental and default-off
Date: 2026-09-05

Decision: private extraction format 3 adds a raw-import string list and presence
bit 1. Only Go, TypeScript and Python with an existing import scanner and an
IMPORTS-emitting profile compute this family. Syntax-only and other languages
leave it absent. A present bit with nil/empty strings is a computed empty family;
absent is never used to skip a relation scan. Unknown bits and inconsistent
absent/nonempty payloads are cache misses. Exact format/build/profile/path/content
keys prevent old payloads being mistaken for the new family.

Requirement and evidence: P1.5 asks for one measured costly file-local family at
a time. The preregistered bounded component audit found importsFor took roughly
2.4%/3.8% of TypeScript/Python full-relation median on its generated workload,
while generic bare call scanning was about 1%. The coordinator selected this
existing import seam after reviewing the audit, before implementation. Detailed
measurements and limitations are in p1-relation-cost-audit.md.

The file worker passes cached import strings through its existing
precomputedImports result; the ordered reducer passes them to forEachRelation.
Local targets, imports-as-bindings, repository manifests, aliases and every final
edge still resolve from current operation inputs. Persist no cross-file binding,
call edge, scope state or compiler evidence. Clone strings on reconstruction and
retain malformed-input status exactly under the existing deterministic-syntax
admission rule. Unsupported and transient failures remain unchanged.

Alternative: caching all call/scope facts now would exceed the measured boundary
and require additional payload/resolver contracts. Caching final IMPORTS edges
would bind old target membership. Both are rejected.

Quota configuration: ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_BYTES and
ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_ENTRIES accept positive integers up to the
existing 1 GiB and 100,000 hard ceilings. Missing, invalid, zero, negative and
above-ceiling values use the corresponding default. Values are sampled once on
the operation's first publication attempt; later environment changes cannot
change that operation's policy. Oversized individual entries bypass storage.
Cleanup still examines at most 100,001 directory entries and only removes
validated cache-owned regular files. Tight limits are tested without changing
ordinary cache defaults or source admission.

Distinguishing tests: all-profile family round trips for Go/TS/Python, computed
empty and malformed imports, detached ownership, old versions/unknown bits and
inconsistent presence rejection, concurrent same-key publication, exact full
snapshot equivalence, and one-entry/tiny-byte quotas. Separate raw-family parse,
reuse and duration counters make entity hits distinguishable from import reuse.
Rollback is --extraction-cache off; format3 entries are disposable. No persistent
working-tree snapshot is created. No performance gate is promoted by this ADR.
