# Parking lot

Deferred work that is intentionally outside the scope of the pull request that
identified it. Each item should be reassessed before implementation so that a
fix does not reduce functionality or performance.

## 1. Structurally validate Git configuration-derived paths and commands

The repository metadata validator covers structural Git metadata, but not paths
or commands supplied by Git configuration. Examples include `include.path`,
`core.worktree`, `core.excludesFile`, `core.fsmonitor`, and
`core.alternateRefsCommand`. In a repository with hostile configuration, Git
could therefore read an external, off-volume, or UNC path, or invoke a configured
command, even after the structural metadata checks pass.

A follow-up should centralize or synthesize the configuration used by production
Git subprocesses. It must preserve supported repository behavior, the provider's
no-egress contract, and current performance. PR #134 contains related Git
configuration defenses; rebases must preserve both sets of protections.

## 3. Enforce metadata validation at the Git subprocess boundary

Production entrypoints currently perform the required repository metadata
validation before launching Git, but the shared Git command constructors do not
enforce it. A future caller could omit the guard, and repository metadata can
also change between validation and a later Git operation.

A follow-up should move enforcement to the shared subprocess boundary, ideally
using held handles or a synthetic/index-only environment where practical. This
likely requires relocating validator and rooted-path helpers into a lower-level
package, along with their platform-specific tests. The design must avoid import
cycles while preserving no-egress guarantees, functionality, and performance.
