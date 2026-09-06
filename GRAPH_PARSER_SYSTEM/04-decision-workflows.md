# Chapter 4: Decision Workflows

## 1. Impact-Aware Code Review and Change Risk

**Trigger:** a pull request or committed change.

```sh
entire graph review --repo . --base main --evidence evidence.json --format json
entire graph check --repo . --head --base main --evidence evidence.json --format json
```

**Workflow:** GPS compares base and selected head snapshots, detects changes to
bound symbols, joins affected anchors to requirements and declared tests, and
reports drift, removed mappings, specification-only changes, and code-only
changes. The output supplies a disposition and citations rather than a vague
risk score.

**Decision:** review affected requirements and their declared tests when the
disposition is `REVIEW_REQUIRED`; stop and repair invalid or incomplete inputs
for `FAIL` or `INCOMPLETE`.

## 2. Test Selection from Affected Relationships

**Trigger:** implementation change with declared acceptance criteria.

```sh
entire graph review --repo . --base HEAD~1 --format json
entire graph verify --repo . --scope auth --test "go test ./internal/auth" --record-baseline evidence.json
```

**Workflow:** `review` returns declared test mappings whose requirements are
attached to changed anchors. `context` also returns declared tests for a
natural-language request. Inferred `Test*` matches are shown separately as
candidates and do not satisfy an obligation.

**Decision:** run the declared tests first, inspect inferred candidates if
needed, and attach the resulting evidence to later `check` or `review` calls.

## 3. Migration Planning and Dependency-Aware Refactoring

**Trigger:** rename, move, API/type change, route change, or dependency update.

```sh
entire graph impact --repo . --symbol Authenticate --file internal/auth/auth.go --intent --format json
entire graph anchor resolve --repo . --id ANCHOR-AUTHENTICATE --format json
```

**Workflow:** code impact exposes callers, callees, type consumers, data flow,
and siblings. The optional intent projection identifies requirements attached to
the affected code. Anchor resolution reveals whether the migration preserves
the approved target, has content or structural drift, or needs an explicit
rebind.

**Decision:** sequence the migration through impacted callers and declared
tests. Do not update the binding until the replacement symbol and requirement
relationship have been reviewed.

## 4. Codebase Onboarding and Guided Exploration

**Trigger:** an unfamiliar request or repository area.

```sh
entire graph context --repo . --query "how access tokens are issued" --format json
entire graph why --repo . --symbol IssueAccessToken --file internal/auth/token.go --format json
```

**Workflow:** `context` combines matching requirements, approved anchors,
ranked source citations, direct relations, declared tests, candidate tests, and
gaps within a deterministic byte budget. `why` follows a concrete symbol back
to explicit requirements, decisions, and tests.

**Decision:** begin with developer-confirmed intent and approved implementation
links. Treat missing links as an onboarding gap, not a reason to invent an
explanation from naming conventions.

## 5. Entire Checkpoint Intent and Provenance

**Trigger:** a reviewer needs local history for an implementation decision.

```sh
entire graph why --repo . --head --symbol IssueAccessToken \
  --file internal/auth/token.go --history --history-limit 16 --format json
```

**Workflow:** GPS reads bounded local Git history for the resolved symbol path
and exposes commit subjects plus `Entire-Checkpoint` trailers when present.

**Decision:** use checkpoint information to locate the related change session
or understand prior work. Do not treat it as a requirement, approved decision,
or proof that the current implementation is correct.
