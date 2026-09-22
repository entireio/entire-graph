# Entire Graph: open issues and pull requests

Repository: [entireio/entire-graph](https://github.com/entireio/entire-graph)

Snapshot checked on 2026-09-10: **20 open issues and 9 open pull requests**, including **2 draft PRs**.

This is an inventory, not an assessment of contribution availability. Open issues may overlap, have work underway, or need reproduction. Status can change after this snapshot.

## Open issues

| Issue | Problem | Notes |
|---|---|---|
| [#225](https://github.com/entireio/entire-graph/issues/225) | Rake test-task detection ignores namespace scope | Appears to overlap #205 |
| [#224](https://github.com/entireio/entire-graph/issues/224) | Workspace globs lack brace expansion; handling unsupported patterns needs a decision | |
| [#223](https://github.com/entireio/entire-graph/issues/223) | Generated VERIFY commands use POSIX shell syntax and cannot run in PowerShell | |
| [#222](https://github.com/entireio/entire-graph/issues/222) | Baseline validation does not compare `--setup` | |
| [#221](https://github.com/entireio/entire-graph/issues/221) | Docs/fixtures classification swallows program-text fixtures; obvious fix breaks VERIFY | |
| [#220](https://github.com/entireio/entire-graph/issues/220) | `--max-seconds` does not bound stalled stdout; cancellation conflicts with truncation contract | |
| [#219](https://github.com/entireio/entire-graph/issues/219) | Narrow Gradle verification lacks settings-membership check | Prepared reproduction confirms incorrect command derivation; same defect as #203 |
| [#218](https://github.com/entireio/entire-graph/issues/218) | Explain wrapper buffers excessive output; streaming alternative corrupts exit status | |
| [#217](https://github.com/entireio/entire-graph/issues/217) | Memory benchmark cannot bind an arm source checkout without a Git repository | |
| [#216](https://github.com/entireio/entire-graph/issues/216) | Decide when LoCoMo harness should fail immediately versus record a dropped case | |
| [#215](https://github.com/entireio/entire-graph/issues/215) | Decide recommended explain invocation and supported example shells | |
| [#214](https://github.com/entireio/entire-graph/issues/214) | Choose signature-stable versus position-stable C++ overload IDs | Related topic to #34 |
| [#212](https://github.com/entireio/entire-graph/issues/212) | C++ `operator bool()` extracted as `operato` | Earlier session could not reproduce on upstream; do not assume actionable |
| [#208](https://github.com/entireio/entire-graph/issues/208) | Later passes exceed the allocated output byte budget | |
| [#205](https://github.com/entireio/entire-graph/issues/205) | Namespaced Rake task mistaken for a top-level task | Appears to overlap #225 |
| [#203](https://github.com/entireio/entire-graph/issues/203) | Narrow Gradle verification lacks settings-membership check | Same defect as #219 |
| [#201](https://github.com/entireio/entire-graph/issues/201) | GraphQL host-language gate suppresses real edges; removing it introduces other errors | |
| [#199](https://github.com/entireio/entire-graph/issues/199) | Python nested callables emitted as phantom class members | |
| [#175](https://github.com/entireio/entire-graph/issues/175) | `--repo <subdir>` behaves differently between snapshot and diff commands | |
| [#34](https://github.com/entireio/entire-graph/issues/34) | Unstable `compound-v1` IDs for same-name/overloaded symbols | Assigned to **karthik-rameshkumar** |

All issues except #34 had no assignee at the time of checking. Lack of an assignee does not prove that nobody is working on an issue.

## Open pull requests

| PR | Work | Status |
|---|---|---|
| [#237](https://github.com/entireio/entire-graph/pull/237) | Graph Parser System: connect requirements, code, dependencies, tests, and verification evidence | Open |
| [#235](https://github.com/entireio/entire-graph/pull/235) | Add graph changeset risk analysis | Open |
| [#233](https://github.com/entireio/entire-graph/pull/233) | Add evidence states to graph results | Open |
| [#232](https://github.com/entireio/entire-graph/pull/232) | Pact/implementation | Open; no description |
| [#229](https://github.com/entireio/entire-graph/pull/229) | Entire/checkpoints/v1 | Open; no description |
| [#152](https://github.com/entireio/entire-graph/pull/152) | RFD 0006: accurate reference positions in SCIP export | Draft discussion proposal |
| [#146](https://github.com/entireio/entire-graph/pull/146) | RFD: converge code search on one engine | Draft discussion proposal |
| [#142](https://github.com/entireio/entire-graph/pull/142) | Enforce an indexing time ceiling; quadratic relation processing remains | Open |
| [#141](https://github.com/entireio/entire-graph/pull/141) | Disclose source exclusions controlled by repository ignore rules | Open |

## Review context

- Issue #219 remains the current small-step learning task. Its prepared worktree is `fix-gradle-verify-project-membership-219`.
- `TestSearchVerifyGradleNarrowProjectMembership` passes for an included project and fails for an undeclared project because both produce `./gradlew :lib:test --tests 'ATest'`. This tests command derivation; it does not execute Gradle.
- The suite-tier fix in merged PR #197 does not fix the narrow route.
- No full code review or reproduction was performed for the other issues as part of this inventory. Apparent overlaps are noted, not formally deduplicated.
- Keep this local inventory out of the eventual Gradle fix commit.
