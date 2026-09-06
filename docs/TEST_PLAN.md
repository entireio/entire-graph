# GraphAudit Test Strategy & Planned Test Cases

**Document Status**: Pre-Implementation Test Specification.  
*Note: These test scenarios define the acceptance criteria for GraphAudit; test implementation has not yet begun.*

---

## 1. Planned Acceptance Test Scenarios

### Scenario A: Direct Go Structural Test Evidence
- **Precondition**: A Go function `CalculateTotal` in `order.go` is modified. A test `TestCalculateTotal` in `order_test.go` directly calls `CalculateTotal`.
- **Command**: `entire graph audit --base main --test "go test ./order"`
- **Expected Verdict**: `STRUCTURAL CHECKS SATISFIED`
- **Assertion**: Audited Structural Surface contains `CalculateTotal`. Structural test evidence links `TestCalculateTotal`. Zero verification gaps. Command succeeds.

### Scenario B: Green Test Command with Missing Structural Evidence (Zero-Evidence Detector)
- **Precondition**: A Go function `ParseHeader` in `internal/parser/header.go` is added or modified. The entire test suite `go test ./...` passes (exit code 0), but no test symbol in the repo contains a `CALLS` edge to `ParseHeader`.
- **Command**: `entire graph audit --base main --test "go test ./..."`
- **Expected Verdict**: `REVIEW REQUIRED`
- **Assertion**: Despite passing test execution, a Verification Gap is recorded for `ParseHeader`. Test command execution is recorded as passing, but the audit does not issue `STRUCTURAL CHECKS SATISFIED`.

### Scenario C: No Explicit Test Command
- **Precondition**: Valid Go change with matching test symbols in `*_test.go`.
- **Command**: `entire graph audit --base main` (no `--test` flag provided).
- **Expected Verdict**: `REVIEW REQUIRED`
- **Assertion**: Gaps may be zero, but missing execution evidence prevents satisfying structural checks. Explicit instruction indicates an unexecuted audit.

### Scenario D: Failing Explicit Test Command
- **Precondition**: A change has full structural test evidence in `*_test.go`, but the test fails (e.g. assertion failure or compile error).
- **Command**: `entire graph audit --base main --test "go test ./..."`
- **Expected Verdict**: `BLOCKED`
- **Assertion**: Non-zero exit code of the provided command immediately forces `BLOCKED`, overriding all structural matches.

### Scenario E: Degraded Graph / Parser Incompleteness
- **Precondition**: A target file modified in the diff has a syntax error or causes a Tree-Sitter partial failure reported by Entire Graph diagnostics.
- **Command**: `entire graph audit --base main --test "go test ./..."`
- **Expected Verdict**: `REVIEW REQUIRED`
- **Assertion**: Regardless of whether tests pass or edges exist, any unindexed or degraded file in the affected boundary forces `REVIEW REQUIRED` with diagnostic details.

### Scenario F: Ambiguous Symbol Mapping
- **Precondition**: A symbol cannot be uniquely disambiguated within the current snapshot due to multi-package naming collisions or conflicting identifiers.
- **Expected Verdict**: `REVIEW REQUIRED`
- **Assertion**: Audit halts traversal on the ambiguous entity and flags it explicitly as an unmapped gap.

### Scenario G: Non-Go Unsupported Evidence
- **Precondition**: A commit modifies a TypeScript (`.ts`) or Python (`.py`) file.
- **Expected Verdict**: `REVIEW REQUIRED`
- **Assertion**: V1 is strictly scoped to Go. Unsupported language files in the diff trigger a clear explanation that structural test evidence inference is unsupported for that file type.

### Scenario H: CLI Help and Dispatch Integrity
- **Precondition**: Running CLI dispatch commands.
- **Commands**: `entire graph audit --help`, invalid flags, missing arguments.
- **Assertion**: Clean exit codes, accurate usage syntax matching other `entire graph` subcommands, correct JSON flag parsing (`--json`).

### Scenario I: Current-HEAD Worktree Integrity
- **Precondition**: Uncommitted modifications in the working tree vs. committed HEAD.
- **Assertion**: The audit inspects the current active working state against the base reference consistently without leaving transient artifacts on disk.
