# Verification

Status: Proposed design. See [the MVP](MVP.md) and [evidence model](anchors-and-evidence.md).

## Verification Has Three Layers

GPS reports distinct layers and never substitutes one for another:

| Layer | Question | Evidence | Cannot prove |
| --- | --- | --- | --- |
| Structural | Are the intent documents, anchors, and graph joins valid? | YAML validation, symbol resolution, drift, relation completeness. | Runtime behavior. |
| Mapping | Is every declared acceptance criterion connected to a relevant test or marked intentionally unverified? | Authored mappings and inferred candidates. | That a test executed or covers every path. |
| Execution | Did an explicitly authorized command produce the expected observed result? | Runner output, parser result, baseline comparison, command provenance. | That all intent is satisfied. |

Developer review determines whether the implementation still fulfills the
requirement. GPS makes the evidence and missing evidence explicit.

## `entire graph check`

`check` is a read-only, static analysis command. It parses inputs, resolves
anchors, joins selected graph evidence, and optionally compares against a base
revision. It never runs a command found in a spec, source comment, test mapping,
or derived `VERIFY:` line.

```sh
entire graph check --repo . --base HEAD --format json
```

Proposed text projection:

```text
GPS Check

Specifications
  PASS 12 valid documents
  WARN 1 proposed requirement has no implementation anchor

Anchors
  PASS 31 valid
  WARN 2 content drift
  ERROR 1 missing anchor

Verification mappings
  PASS 28 declared mappings resolve to tests
  WARN 2 acceptance criteria have no declared verification
  INFO 3 inferred test candidates

Change impact
  HIGH 2 specifications, 5 requirements, 6 affected test mappings

Disposition: REVIEW_REQUIRED
```

The command must not call unexecuted tests `PASS`. A mapping resolves to a test
symbol only when static evidence permits that conclusion; a runner test ID may
be declared but unresolved and reported as `DECLARED_NOT_RESOLVED`.

## Findings and Disposition

Findings are versioned JSON records:

```json
{
  "id": "GPS-ANCHOR-MISSING",
  "severity": "error",
  "status": "open",
  "subject": {"kind": "anchor", "id": "ANCHOR-AUTHENTICATE"},
  "message": "The bound symbol no longer exists in the selected complete graph.",
  "evidence": [],
  "repository_view": {"base": "...", "current": "..."},
  "intent_digest": "sha256:..."
}
```

Initial finding families:

| Family | Examples |
| --- | --- |
| Input | invalid schema, duplicate ID, bad reference, unsafe or excluded input path. |
| Anchor | missing, ambiguous, content drift, structural drift, candidate rebind, unverifiable. |
| Traceability | requirement has no anchor, changed implementation has no linked requirement, relationship cycle. |
| Mapping | acceptance has no declared test, selector unresolved, changed test mapping, inferred-only test relationship. |
| Completeness | parser failure, file limit, unsupported language, changed input during capture, insufficient context budget. |
| Delta | changed symbol affects requirement, spec changed without linked implementation change, removed test mapping. |

Disposition is not a binary correctness result:

| Disposition | Rule |
| --- | --- |
| `PASS` | Selected checks completed with no errors or review-required warnings. |
| `REVIEW_REQUIRED` | Structural findings need human assessment, such as drift or changed intent. |
| `INCOMPLETE` | A conclusion could not be reached because inputs, parser coverage, or limits are incomplete. |
| `FAIL` | Invalid inputs or confirmed broken references prevent traceability. |
| `NOT_CONFIGURED` | No selected specs or policy exist. |

A test failure observed through an execution evidence file can make a review
disposition fail for its declared scope, but `check` does not manufacture an
execution result. A clean check only means the requested static rules found no
blocking issue.

## Test Mapping

Test mapping has three evidence grades:

| Grade | Origin | Rendering |
| --- | --- | --- |
| `DECLARED` | Authored acceptance-to-test mapping. | Required verification obligation. |
| `OBSERVED` | Direct semantic `TESTS`/`CALLS` relation with source evidence. | Supporting code evidence. |
| `INFERRED` | Name, filename, or body heuristic. | Candidate only, never a fulfilled obligation. |

An acceptance criterion with a declared mapping is structurally mapped, even if
the test fails. A requirement with no acceptance criterion is a spec authoring
problem. A test can support multiple criteria, and one criterion can require
multiple tests. Future policies may allow an explicit `verification: manual`
declaration with a reason; absence is not manual verification.

Static mapping is not measured test coverage. The tool should state when it
cannot parse the testing framework or associate a runner ID with a source symbol.

## Execution Evidence

Existing `entire graph verify` is the command-execution boundary. It receives
the command from the caller and runs it with the caller's privileges. Future
GPS support must preserve that boundary:

```sh
entire graph verify --repo . --test "go test ./internal/auth" --record-baseline .entire/graph/evidence/auth-baseline.json
entire graph verify --repo . --test "go test ./internal/auth" --pre-edit-baseline .entire/graph/evidence/auth-baseline.json
```

Verification policy is a strict, optional local document at
`.entire/graph/verification.yaml`:

```yaml
version: 1
scopes:
  - id: auth
    command: go test ./internal/auth
    setup_command: go mod download
```

It is command metadata, not execution authority. When it declares scopes,
`verify` requires `--scope` and rejects caller command or setup metadata that
does not exactly match the selected scope. The command remains caller-supplied.

Evidence records include:

- execution evidence schema version;
- normalized repository identity, resolved commit or worktree content identity;
- intent-set and verification-policy digests;
- command, setup command, parser, platform, timeout, and explicit scope IDs;
- start and finish timestamps, exit code, parser confidence, and result IDs;
- baseline identity and compatibility decision; and
- a statement of unparsed or unavailable results.

The runner rejects incompatible baselines by default: different repository,
command, parser, scope, or policy digest is incompatible. A missing policy
digest in legacy evidence is incompatible with the current policy. Removed test
IDs and missing baseline IDs must be reported; comparison cannot silently
iterate only current test IDs.

Specs may name a runner command only as non-executable metadata for display. A
future `verify --scope <spec-or-acceptance-id>` can select from caller-approved
command configuration, but must print the exact command and require the same
execution authorization. It never executes a command embedded in an untrusted
checkout merely because a spec says so.

See [trust and security](../trust-and-security.md): even a derived test command
is repository-influenced input, and `verify` is not a sandbox.

## Review

`entire graph review` is a later projection, not a replacement for code review.
It can summarize a selected diff as:

```text
Changed: 4 files, 7 symbols
Intent: 2 specs, 5 requirements, 3 acceptance criteria
Anchors: 1 content drift, 0 missing
Verification: 4 declared mappings affected; 2 execution results are stale
Gaps: REQ-AUTH-EXPIRY has no declared acceptance mapping
```

Its output must cite facts, identify scope, and keep reviewer judgment separate.
It cannot approve a change or mark a requirement correct automatically.
