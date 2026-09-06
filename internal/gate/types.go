// Package gate turns an entity-level change set into a keep/continue/revert
// verdict backed by evidence the agent under review did not produce.
//
// Everything in this package is pure. The collect layer (internal/cli/gate.go)
// performs every read — git, the snapshot, search — and hands the records in.
// That boundary is what lets signals and the verdict be tested with synthetic
// records in milliseconds without tree-sitter, and it keeps a change to one
// signal from reaching the others.
package gate

// Anchor locates a finding in source so a reader can verify it instead of
// trusting it. Every finding Gate prints carries one.
type Anchor struct {
	SymbolID string `json:"symbol_id,omitempty"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
}

// ChangeType is the entity-level shape of a change, mirroring the vocabulary
// sem.EntityChange already uses.
type ChangeType string

const (
	Added            ChangeType = "added"
	Removed          ChangeType = "removed"
	Renamed          ChangeType = "renamed"
	SignatureChanged ChangeType = "signature_changed"
	BodyChanged      ChangeType = "body_changed"
)

// Breaking reports whether this change can break a caller without the caller
// being edited. A body change cannot: its contract is unchanged. Removals,
// renames and signature changes can, which is why only these three open the
// risk dimension.
func (c ChangeType) Breaking() bool {
	return c == Removed || c == Renamed || c == SignatureChanged
}

// CoverageState is the three-way answer to "did anyone check this". The third
// state exists because "no test covers this" and "we could not look for tests
// in this language" are different claims, and reporting either as the other is
// the quiet failure Gate is built to avoid.
type CoverageState string

const (
	// Verified: at least one test covers the entity.
	Verified CoverageState = "verified"
	// Unchecked: the resolver ran and found no covering test.
	Unchecked CoverageState = "unchecked"
	// NoResolver: coverage could not be determined here at all.
	NoResolver CoverageState = "no-resolver"
)

// ChangedEntity is one entity from the semantic diff, enriched by the signals.
type ChangedEntity struct {
	Anchor
	Kind       string        `json:"kind"`
	ChangeType ChangeType    `json:"change_type"`
	Dependents int           `json:"dependents"`
	Coverage   CoverageState `json:"coverage"`
	// CoveringTests names the tests that exercise this entity, bounded by the
	// collect layer. Empty whenever Coverage is not Verified.
	CoveringTests []string `json:"covering_tests,omitempty"`
}

// Dimension names the signal a finding came from, so the renderer can group
// findings and the verdict can tell which evidence was actually available.
type Dimension string

const (
	DimRisk       Dimension = "risk"
	DimCoverage   Dimension = "coverage"
	DimCompanions Dimension = "companions"
	DimClones     Dimension = "clones"
)

// Finding is one thing a reviewer should look at, anchored to source.
type Finding struct {
	Dimension Dimension `json:"dimension"`
	Subject   Anchor    `json:"subject"`
	Summary   string    `json:"summary"`
	// Evidence is the provenance behind Summary — call paths, test names,
	// co-change ratios — one entry per line of rendered output.
	Evidence []string `json:"evidence,omitempty"`
}

// Availability records which dimensions actually produced evidence.
//
// This is load-bearing, not bookkeeping. The revert rule reads "has dependents
// AND no covering test". If the coverage dimension never ran then nothing has a
// covering test, and every dependent-bearing change would read as revert — a
// false accusation manufactured by a missing input. Decide consults this so an
// absent dimension can never push a verdict upward.
type Availability struct {
	Risk       bool `json:"risk"`
	Coverage   bool `json:"coverage"`
	Companions bool `json:"companions"`
	Clones     bool `json:"clones"`
}

// Verdict is the decision. Four states, and the fourth is the one most tools
// omit: when Gate cannot produce evidence, the honest answer is "we could not
// check", not "roll back".
type Verdict string

const (
	Keep     Verdict = "keep"
	Continue Verdict = "continue"
	Revert   Verdict = "revert"
	Unusable Verdict = "unusable"
)

// ExitCode maps the verdict onto the process exit status, so Gate can be a
// pre-push hook or a CI step unchanged.
func (v Verdict) ExitCode() int {
	switch v {
	case Keep:
		return 0
	case Continue:
		return 1
	case Revert:
		return 2
	default:
		return 5
	}
}

// Report is everything one Gate run produced. The renderer is the only consumer
// that knows about output format; nothing else in this package formats a string
// for display.
type Report struct {
	Base       string `json:"base"`
	Head       string `json:"head"`
	Checkpoint string `json:"checkpoint,omitempty"`

	Entities  []ChangedEntity `json:"entities"`
	Findings  []Finding       `json:"findings"`
	Available Availability    `json:"available"`
	Verdict   Verdict         `json:"verdict"`
	ExitCode  int             `json:"exit_code"`

	// VerifyCommand is a runnable test invocation covering the changed code,
	// derived from the repository's own build files. Empty when none could be
	// derived with confidence: a wrong command costs more than no command.
	VerifyCommand string `json:"verify_command,omitempty"`
	// Warnings carry partial failures and skipped work so an incomplete run is
	// never silently reported as a complete one.
	Warnings []string `json:"warnings,omitempty"`
}
