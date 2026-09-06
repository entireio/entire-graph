# Retained request diagnostic: enumeration timeout

The historical Kubernetes request was replayed once from source5a60fc8f on
Darwin/arm64, using the same query, syntax-only profile, default file cap and
cache-off options. It exceeded its120-second context deadline and returned an
error after161.18seconds. The cache-on arm was explicitly unrun. Full response,
source/binary/harness identities, unchanged corpus hashes and logs are retained
in `retained-search-5a60fc8f/`. This is an isolated correctness diagnostic, not
another comparative sweep or a Linux campaign result.

A separate ten-second hard-timeout stack diagnostic using the same executable
found the request in `gitDirExcluder.observeListedPaths`, via `gitDirLinkTarget`
and `sameVolumePathResolver`, during working-tree enumeration. Its expected
forced-timeout stack is retained in `retained-search-timeout-trace/`. That stack
locates work at ten seconds; it does not prove where every later second was spent.
The listed-path observation loop's cancellation behavior is the next concrete
correctness investigation. Do not weaken containment/volume checks or relabel
this failed attempt as a successful equivalence result.

Subdirectory attribute integration9c7c70b8 proceeded independently and passed
focused/race tests. No benchmark campaign or VM was started. Historical116
campaign observations and1434explicit unrun cells remain unchanged; these two
diagnostic executions are separate evidence.

Cancellation checks were added in `6102b209` between bounded metadata probes
and listed-path chains, preserving the original context error. Focused regression
tests cover stopping before a later path. The existing resolver cannot simply
be reused: it contains a mount-table snapshot whose freshness is part of the
no-egress boundary. `TestPathMountGuardRefreshesBetweenResolvers` and
`TestPathMountGuardDoesNotReuseTableAfterReadFailure` encode that requirement.
No resolver reuse or mutable path-decision cache was introduced.
