# Evidence index

Final product-code revision: a70a1892 (git diff -- internal cmd is empty).
Baseline: 3a2a715fad1948e83dc7ebe0d307377ba29e065a.

| Artifact | Meaning and provenance |
|---|---|
| baseline-environment.txt | Pinned detached baseline, command, toolchain/platform, binary and plan SHA-256 |
| baseline-bench.txt | Six successful one-operation synthetic worker characterization samples; not release trials |
| baseline.cpu.pprof; baseline-cpu*.txt | Raw CPU profile and derived top/cumulative reports from the isolated baseline |
| seam-phases.txt | Nine successful synthetic phase-characterization samples, three per profile; serialization overlaps enclosing phases |
| seam-tests.txt; capture-store-tests.txt | Initial seam and bounded-store focused checks |
| compiler-primitives-tests.txt | Fake protocol and input-binding tests; no live server |
| impact-core-tests.txt; impact-source-tests.txt | Graph fixtures plus real Go/TypeScript/Python source chains, output/work bounds and byte-identity checks |
| experimental-core-tests.txt | Initial pure-core tests; superseded for final code by focused-final.txt |
| focused-final.txt | Final focused race checks for added primitives and oversize regression |
| graph-verify.txt | Exact VERIFY command emitted by graph search |
| check-stage-a.txt | Initial full mise run check, passed |
| check-final.txt; check-final-stable.txt | Intermediate full checks, passed; later code changes tested separately |
| check-a70a1892.txt | Full required check on final product-code revision; passed, 730.00 seconds |
| binary-build-info.txt; binary-sha256.txt | Local mise-built binary metadata/hash; code checkout a70a1892, documentation artifacts uncommitted during build |
| development-failures.txt | Retained development failures and unsuccessful live-runtime prerequisite probes |

These artifacts establish only the checks they actually ran. They do not establish P1 cache equivalence/performance, live compiler correctness/isolation, adjudicated impact precision/recall, held-out retrieval quality or agent task resolution. Release denominators for those experiments do not exist yet; they must not be represented as successful or zero-failure samples.
