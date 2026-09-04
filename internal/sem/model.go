package sem

import "io"

// moduleKind labels a synthetic entity that represents file/module scope: code
// that lives outside any named symbol (top-level statements, imports, comments).
// Changes attributed to it keep module-scope edits visible in the diff instead
// of being dropped as null.
const moduleKind = "module"

type Entity struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Signature   string `json:"signature"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	BodyHash    string `json:"body_hash"`
	Fingerprint string `json:"fingerprint"`
	// Local marks a callable defined inside another function (a nested/closure
	// def). It is still a real symbol, but it is only callable from within its
	// enclosing function, so call resolution must not name-match it across scopes.
	Local bool `json:"-"`
	// bodyless marks a declaration that declares a callable without defining it:
	// a TypeScript overload signature or an ambient `declare function`. It is a
	// real symbol (the declared types live only there), but it is NOT a second
	// definition of the name — the implementation right below it is. Two rules
	// depend on the distinction: the implementation must keep the bare
	// compound-v1 symbol ID no matter how many signatures precede it, and a call
	// that lands on an overload set must resolve to the implementation instead of
	// being downgraded as ambiguous. Private, like the other parse metadata, so
	// the frozen schema is unchanged.
	bodyless bool
	// cPlusPlusOwner is the declaration's full lexical C++ owner spelling
	// (`acct::Outer::Ledger`). Graph QualifiedName intentionally remains based
	// on the immediate container for stable IDs, so out-of-line definition
	// matching carries this separately.
	cPlusPlusOwner string
	// cLinkage marks a declaration that sits inside an `extern "C" { ... }`
	// block (see declaredWithCLinkage). It is the only per-declaration record of
	// which half of a dual-use C++-labelled header a C translation unit may
	// name. Private, like the other parse metadata, so the frozen schema and the
	// compound-v1 IDs are unchanged.
	cLinkage bool
	// sourceStartByte/sourceEndByte are the exact tree-sitter declaration range.
	// They are internal parse metadata: public schema and stable symbol identity
	// intentionally remain line based. A zero start is valid when end > start.
	sourceStartByte int
	sourceEndByte   int
	// parameterNames holds JS/TS parameter identifiers read from the
	// declaration's formal_parameters AST node (internal parse metadata, like
	// the byte range). Signature-string parsing cannot recover these reliably:
	// generic clauses may themselves contain parenthesized function types.
	parameterNames      []string
	parameterNamesKnown bool
	// paramTypeText/returnTypeText hold the callable's declared types as the
	// parse tree delimits them, so the type passes do not have to re-guess
	// where a signature's parameter list ends. signatureTypesKnown separates
	// "the grammar says this callable declares no return type" from "no parser
	// metadata"; both stay private to preserve the frozen schema.
	paramTypeText       string
	returnTypeText      string
	signatureTypesKnown bool
}

type EntityChange struct {
	Type            string  `json:"type"`
	Kind            string  `json:"kind"`
	Name            string  `json:"name"`
	OldName         string  `json:"old_name,omitempty"`
	NewName         string  `json:"new_name,omitempty"`
	OldSignature    string  `json:"old_signature,omitempty"`
	NewSignature    string  `json:"new_signature,omitempty"`
	OldPath         string  `json:"old_path,omitempty"`
	NewPath         string  `json:"new_path,omitempty"`
	BeforeStartLine int     `json:"before_start_line,omitempty"`
	AfterStartLine  int     `json:"after_start_line,omitempty"`
	DependentsCount int     `json:"dependents_count"`
	Similarity      float64 `json:"similarity,omitempty"`
	// Reconciliation carries explicit identity-continuity metadata when a
	// delete+add pair was reconciled to a single change: RENAMED (same file),
	// MOVED (across files), or RECONCILED_FROM. Empty for ordinary changes.
	Reconciliation string `json:"reconciliation,omitempty"`
}

type FileChange struct {
	Path     string         `json:"path"`
	OldPath  string         `json:"old_path,omitempty"`
	Status   string         `json:"status"`
	Language string         `json:"language,omitempty"`
	Changes  []EntityChange `json:"changes"`
}

type Result struct {
	Checkpoint string            `json:"checkpoint,omitempty"`
	Base       string            `json:"base"`
	Head       string            `json:"head"`
	Files      []FileChange      `json:"files"`
	Warnings   []ProviderWarning `json:"warnings,omitempty"`
	// SchemaVersion pins the shape of this Result so a copy persisted into
	// checkpoint metadata can be read back knowing which schema it was written
	// under. Populated centrally from the package SchemaVersion const at the
	// single place Result is constructed with real content
	// (AnalyzeGitRangeWithOptions) — every caller (diff, analyze, checkpoint)
	// funnels through it.
	SchemaVersion string `json:"schema_version"`
	// ProducerVersion is the entire-graph binary version that produced this
	// Result (build-time version threaded from main.go via Options.Version).
	// Optional/omitted when the caller has no version to attribute.
	ProducerVersion string `json:"producer_version,omitempty"`
}

type Parser interface {
	Parse(path, content string) ([]Entity, string)
}

func WriteText(out io.Writer, result Result) {
	writeText(out, result)
}
