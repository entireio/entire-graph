# Parking lot

Deferred work that is intentionally outside the scope of the pull request that
identified it. Each item should be reassessed before implementation so that a
fix does not reduce functionality or performance.

## 1. Validate remaining repository-local structural Git configuration

The broad configuration-derived vectors that motivated this item are now
bounded. Production Git subprocesses disable inherited global and system
configuration; the metadata preflight rejects active repository-local
`[include]` and `[includeIf]` sections and checks active `core.worktree` paths;
protected command-scope `safe.directory` entries retain support for the
explicitly selected directory and its discovery ancestors without using `*`;
and command-scope configuration disables `core.fsmonitor`,
`log.showSignature`, `log.mailmap`, `submodule.recurse`, `core.excludesFile`,
`core.attributesFile`, and `diff.orderFile`. Those fixed settings are no longer
deferred work.

Repository-local structural configuration still cannot be disabled wholesale:
Git needs it for repository format and layout. A follow-up should classify and
preflight the remaining path- or command-bearing settings that the production
command surface can activate. This includes `extensions.refStorage` when it can
name a relocated reftable store, and may include command-bearing settings such as
`core.alternateRefsCommand` as command coverage evolves. The follow-up must
preserve supported repository formats, the provider's no-egress contract, and
current performance; it should not re-list neutralized settings as unresolved.

## 3. Enforce metadata validation at the Git subprocess boundary

Production entrypoints currently perform the required repository metadata
validation before launching Git, but the shared Git command constructors do not
enforce it. A future caller could omit the guard, and repository metadata can
also change between validation and a later Git operation.

A deterministic test now confirms the remaining race: after a successful
validation, replacing a checked metadata entry before command construction lets
the Git subprocess start. Repeating the pathname check closer to launch would
only narrow that window, not close it, so it is not a complete correction.

A follow-up should move enforcement to the shared subprocess boundary, ideally
using held handles or a synthetic/index-only environment where practical. This
likely requires relocating validator and rooted-path helpers into a lower-level
package, along with their platform-specific tests. The design must avoid import
cycles while preserving no-egress guarantees, functionality, and performance.

## 4. Harden dependent-file fallback coordinates for future subdirectory callers

The dependent-file helper passes scope-relative paths to `LimitedFileReader`,
whose contract uses paths rooted at Git's command tree. The current production
caller first normalizes the repository to `RepoCommandRoot`, so its scope prefix
is empty and the mismatch is not reachable in shipped behavior. A direct or
future subdirectory caller could nevertheless undercount dependents when the
batch reader falls back to bounded individual reads.

If such a caller is added, compute the repository prefix once and apply it only
to `LimitedFileReader.Prime` and `LimitedFileReader.ReadFile`; keep the original
scope-relative paths for batch reads, parsing, and diagnostics. Preserve the
current two-batch behavior for newline-bearing candidates and avoid adding work
to the normalized production path until the helper contract actually changes.
