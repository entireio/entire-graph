package sem

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	stdhash "hash"
	"io"
	"net/url"
	"sort"
	"strings"

	scippb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// CompactSnapshotFormatVersion is the fixed wire version for compact snapshots.
const CompactSnapshotFormatVersion = 1

// CompactSnapshotEncoder writes the compact, line-delimited snapshot format.
// The dictionary is intentionally emitted as soon as a record needs its strings:
// this keeps the writer streaming while preserving byte-for-byte determinism.
type CompactSnapshotEncoder struct {
	encoder         *json.Encoder
	out             io.Writer
	strings         map[string]int
	dictionary      []string
	dictionaryBytes int64
	wroteHeader     bool
	wroteSummary    bool
}

func NewCompactSnapshotEncoder(out io.Writer) *CompactSnapshotEncoder {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return &CompactSnapshotEncoder{encoder: encoder, out: out, strings: map[string]int{"": 0}, dictionary: []string{""}}
}

// Encode accepts exactly the public snapshot record types. Header is first and
// summary is last; all other records are dictionary-backed positional arrays.
func (encoder *CompactSnapshotEncoder) Encode(record any) error {
	if encoder.wroteSummary {
		return errors.New("compact snapshot summary must be last")
	}
	switch typed := record.(type) {
	case SnapshotHeader:
		if encoder.wroteHeader {
			return errors.New("compact snapshot has more than one header")
		}
		encoder.wroteHeader = true
		return encoder.writeLine([]any{"h", CompactSnapshotFormatVersion, typed})
	case SnapshotSummary:
		if !encoder.wroteHeader {
			return errors.New("compact snapshot header must be first")
		}
		encoder.wroteSummary = true
		return encoder.writeLine([]any{"m", typed})
	case FileRecord:
		if !encoder.wroteHeader {
			return errors.New("compact snapshot header must be first")
		}
		return encoder.encodeData(encoder.fileRow(typed))
	case ExternalRecord:
		if !encoder.wroteHeader {
			return errors.New("compact snapshot header must be first")
		}
		return encoder.encodeData(encoder.externalRow(typed))
	case SymbolRecord:
		if !encoder.wroteHeader {
			return errors.New("compact snapshot header must be first")
		}
		return encoder.encodeData(encoder.symbolRow(typed))
	case RelationRecord:
		if !encoder.wroteHeader {
			return errors.New("compact snapshot header must be first")
		}
		return encoder.encodeData(encoder.relationRow(typed))
	default:
		return fmt.Errorf("unsupported compact snapshot record %T", record)
	}
}

func (encoder *CompactSnapshotEncoder) DictionaryBytes() int64 { return encoder.dictionaryBytes }

func (encoder *CompactSnapshotEncoder) encodeData(row compactDataRow) error {
	return encoder.writeData(row.row, row.base)
}

// writeData writes dictionary additions immediately before the positional row.
func (encoder *CompactSnapshotEncoder) writeData(row []any, base int) error {
	if len(encoder.dictionary) > base {
		line, err := compactJSONLine([]any{"d", base, encoder.dictionary[base:]})
		if err != nil {
			return err
		}
		if _, err := encoder.out.Write(line); err != nil {
			return err
		}
		encoder.dictionaryBytes += int64(len(line))
	}
	return encoder.writeLine(row)
}

func (encoder *CompactSnapshotEncoder) writeLine(value any) error {
	return encoder.encoder.Encode(value)
}

func compactJSONLine(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (encoder *CompactSnapshotEncoder) intern(value string) int {
	if index, ok := encoder.strings[value]; ok {
		return index
	}
	index := len(encoder.dictionary)
	encoder.strings[value] = index
	encoder.dictionary = append(encoder.dictionary, value)
	return index
}

func (encoder *CompactSnapshotEncoder) fileRow(record FileRecord) compactDataRow {
	base := len(encoder.dictionary)
	row := []any{"f", encoder.intern(record.ID), encoder.intern(record.Path), encoder.intern(record.Blob), encoder.intern(record.Language), record.Bytes}
	return encoder.dataRow(row, base)
}

func (encoder *CompactSnapshotEncoder) externalRow(record ExternalRecord) compactDataRow {
	base := len(encoder.dictionary)
	row := []any{"x", encoder.intern(record.ID), encoder.intern(record.Kind), encoder.intern(record.Value), encoder.intern(record.FilePath), record.StartLine, record.EndLine, encoder.intern(record.Signature), encoder.intern(record.Language), record.External, encoder.intern(record.SourceSymbol), encoder.intern(record.SourceDetails)}
	return encoder.dataRow(row, base)
}

func (encoder *CompactSnapshotEncoder) symbolRow(record SymbolRecord) compactDataRow {
	base := len(encoder.dictionary)
	id := encoder.intern(record.ID)
	stableIDVersion := encoder.intern(record.StableIDVersion)
	kind := encoder.intern(record.Kind)
	name := encoder.intern(record.Name)
	qualifiedName := encoder.intern(record.QualifiedName)
	filePath := encoder.intern(record.FilePath)
	signature := encoder.intern(record.Signature)
	bodyHash := encoder.intern(record.BodyHash)
	language := encoder.intern(record.Language)
	containerID := encoder.intern(record.ContainerID)
	aliases := make([]int, len(record.Aliases))
	for i, alias := range record.Aliases {
		aliases[i] = encoder.intern(alias)
	}
	row := []any{"s", id, stableIDVersion, kind, name, qualifiedName, filePath, record.StartLine, record.EndLine, signature, bodyHash, language, containerID, aliases}
	return encoder.dataRow(row, base)
}

func (encoder *CompactSnapshotEncoder) relationRow(record RelationRecord) compactDataRow {
	base := len(encoder.dictionary)
	fromID := encoder.intern(record.FromID)
	toID := encoder.intern(record.ToID)
	relationType := encoder.intern(record.Type)
	reason := encoder.intern(record.Reason)
	relationScope := encoder.intern(record.RelationScope)
	resolution := encoder.intern(record.Resolution)
	targetKind := encoder.intern(record.TargetKind)
	evidence := make([][]any, len(record.Evidence))
	for i, item := range record.Evidence {
		evidence[i] = []any{encoder.intern(item.Kind), encoder.intern(item.FilePath), item.StartLine, item.EndLine, encoder.intern(item.Detail)}
	}
	var warnings []int
	if record.WarningCodes != nil {
		warnings = make([]int, len(record.WarningCodes))
		for i, code := range record.WarningCodes {
			warnings[i] = encoder.intern(code)
		}
	}
	row := []any{"r", fromID, toID, relationType, record.Confidence, reason, relationScope, resolution, targetKind, evidence, warnings}
	return encoder.dataRow(row, base)
}

// dataRow returns a sentinel row wrapper which lets Encode emit the dictionary
// update before its data row without giving callers access to codec internals.
func (encoder *CompactSnapshotEncoder) dataRow(row []any, base int) compactDataRow {
	return compactDataRow{row: row, base: base}
}

type compactDataRow struct {
	row  []any
	base int
}

// SCIPOmissionNote is the deterministic stderr companion for the experimental
// SCIP export. It names relation facts that cannot be represented as SCIP
// definition/reference occurrences.
type SCIPOmissionNote struct {
	RecordType                string         `json:"record_type"`
	Version                   string         `json:"version"`
	Format                    string         `json:"format"`
	UnsupportedRelationCounts map[string]int `json:"unsupported_relation_counts"`
	MissingTargetRelations    int            `json:"missing_target_relations"`
	MissingEvidenceRelations  int            `json:"missing_evidence_relations"`
	EmittedDefinitions        int            `json:"emitted_definitions"`
	EmittedReferences         int            `json:"emitted_references"`
	WorktreeSnapshot          bool           `json:"worktree_snapshot,omitempty"`
	PartialSnapshot           bool           `json:"partial_snapshot,omitempty"`
	CompletenessLevel         string         `json:"completeness_level,omitempty"`
	WarningCount              int            `json:"warning_count,omitempty"`
	PartialFailureCount       int            `json:"partial_failure_count,omitempty"`
	// UnidentifiedRecords counts records the encoder could not key and
	// therefore did not carry into the index: a file with no path, or a symbol
	// or external endpoint with no id. Such a record is a provider bug rather
	// than an expected input, but dropping one silently would make the index
	// quietly incomplete, which this format's whole point is to avoid.
	UnidentifiedRecords int `json:"unidentified_records,omitempty"`
	// UnlocatedSymbols counts symbols with no file path. They are emitted, so
	// they can still be referenced, but they land in a synthetic document and
	// carry no navigable definition location.
	UnlocatedSymbols int `json:"unlocated_symbols,omitempty"`
	// EmittedImplementations counts relationships emitted for the inheritance
	// family, which SCIP answers "Find Implementations" from.
	EmittedImplementations int `json:"emitted_implementations,omitempty"`
	// LanguageTiers classifies each language in the snapshot as "semantic" or
	// "inventory-only", carried through from the summary.
	//
	// SCIP has no way to express the difference: every discovered file becomes a
	// Document, so an inventory-only file -- discovered and listed, but never
	// parsed for symbols or relations -- is indistinguishable from a
	// semantically extracted one. A consumer deciding per language whether to
	// trust this feed cannot make that decision from the protobuf alone, and
	// this is the field that lets it.
	LanguageTiers map[string]string `json:"language_tiers,omitempty"`
	// Commit and Tree are the revision this index describes.
	//
	// They used to ride inside every symbol as the SCIP package version, which
	// gave a symbol a new identity on every commit. Removing them from the
	// moniker was right, but they then survived nowhere: SCIP Metadata carries
	// ToolInfo, ProjectRoot and an encoding and has no revision field, so two
	// committed exports of the same repository at the same project version were
	// indistinguishable from the artifacts alone. They belong here -- once per
	// index, where they identify the export without touching symbol identity.
	// Empty for a worktree export, which describes no commit; worktree_snapshot
	// says so.
	Commit string `json:"commit,omitempty"`
	Tree   string `json:"tree,omitempty"`
	// PartialFailures carries the summary's failure records, not just their
	// count. A count says something was skipped; only the records say which path
	// and why, which is what the provider contract requires unparseable input to
	// surface as. PartialFailureCount stays for consumers that only need the
	// number.
	PartialFailures []PartialFailure `json:"partial_failures,omitempty"`
}

// SCIPSnapshotEncoder writes an experimental SCIP Index protobuf for complete
// snapshots. The native provider stream is still the semantic authority; this
// encoder projects definition and reference-like facts into SCIP's navigation
// model and reports omitted relation families separately.
type SCIPSnapshotEncoder struct {
	out       io.Writer
	header    *SnapshotHeader
	summary   *SnapshotSummary
	files     map[string]FileRecord
	externals map[string]ExternalRecord
	symbols   map[string]SymbolRecord
	relations []RelationRecord
	// projectVersion is the SCIP package version: the project's own declared
	// version, never the commit. A commit here would give every symbol a new
	// identity on every commit, which defeats the cross-index linking the field
	// exists for. Commit provenance lives in Metadata and the omission note.
	projectVersion string
	wroteHeader    bool
	wroteSummary   bool
	note           SCIPOmissionNote
}

// NewSCIPSnapshotEncoder returns an encoder for one complete snapshot stream.
func NewSCIPSnapshotEncoder(out io.Writer, projectVersion string) *SCIPSnapshotEncoder {
	if strings.TrimSpace(projectVersion) == "" {
		projectVersion = ScipProjectVersionUnknown
	}
	// SetProjectVersion may replace this before the summary; see its comment.
	return &SCIPSnapshotEncoder{
		out:            out,
		projectVersion: projectVersion,
		files:          map[string]FileRecord{},
		externals:      map[string]ExternalRecord{},
		symbols:        map[string]SymbolRecord{},
		note: SCIPOmissionNote{
			RecordType:                "scip_omissions",
			Version:                   "entire-graph-scip-omissions/v1",
			Format:                    "scip",
			UnsupportedRelationCounts: map[string]int{},
		},
	}
}

func (encoder *SCIPSnapshotEncoder) Encode(record any) error {
	if encoder.wroteSummary {
		return errors.New("scip snapshot summary must be last")
	}
	switch typed := record.(type) {
	case SnapshotHeader:
		if encoder.wroteHeader {
			return errors.New("scip snapshot has more than one header")
		}
		encoder.wroteHeader = true
		header := typed
		encoder.header = &header
		return nil
	case SnapshotSummary:
		if !encoder.wroteHeader {
			return errors.New("scip snapshot header must be first")
		}
		encoder.wroteSummary = true
		summary := typed
		encoder.summary = &summary
		payload, err := encoder.marshalIndex()
		if err != nil {
			return err
		}
		var written int
		written, err = encoder.out.Write(payload)
		if err == nil && written != len(payload) {
			err = io.ErrShortWrite
		}
		return err
	case FileRecord:
		if !encoder.wroteHeader {
			return errors.New("scip snapshot header must be first")
		}
		if typed.Path == "" {
			encoder.note.UnidentifiedRecords++
			return nil
		}
		encoder.files[typed.Path] = typed
		return nil
	case ExternalRecord:
		if !encoder.wroteHeader {
			return errors.New("scip snapshot header must be first")
		}
		if typed.ID == "" {
			encoder.note.UnidentifiedRecords++
			return nil
		}
		encoder.externals[typed.ID] = typed
		return nil
	case SymbolRecord:
		if !encoder.wroteHeader {
			return errors.New("scip snapshot header must be first")
		}
		if typed.ID == "" {
			encoder.note.UnidentifiedRecords++
			return nil
		}
		encoder.symbols[typed.ID] = typed
		return nil
	case RelationRecord:
		if !encoder.wroteHeader {
			return errors.New("scip snapshot header must be first")
		}
		encoder.relations = append(encoder.relations, typed)
		return nil
	default:
		return fmt.Errorf("unsupported scip snapshot record %T", record)
	}
}

// SetProjectVersion supplies the SCIP package version after construction.
//
// The version comes from the snapshot build itself, through the provider's
// validated content reader, so it arrives after the encoder exists but before
// the summary that triggers marshalling -- which is the only deadline, since
// nothing is written until then. A blank value leaves the fallback in place.
func (encoder *SCIPSnapshotEncoder) SetProjectVersion(version string) {
	// Bounded here too, not only at the source: this is exported, the value is
	// repository-controlled, and it is copied into every emitted symbol, so an
	// overlong one amplifies into the whole index rather than costing one field.
	if version = strings.TrimSpace(version); version != "" && len(version) <= ScipProjectVersionMaxLen {
		encoder.projectVersion = version
	}
}

// OmissionNote returns a copy of the current export accounting.
func (encoder *SCIPSnapshotEncoder) OmissionNote() SCIPOmissionNote {
	note := encoder.note
	note.UnsupportedRelationCounts = map[string]int{}
	for key, value := range encoder.note.UnsupportedRelationCounts {
		note.UnsupportedRelationCounts[key] = value
	}
	return note
}

func (encoder *SCIPSnapshotEncoder) marshalIndex() ([]byte, error) {
	header := SnapshotHeader{Provider: ProviderName}
	if encoder.header != nil {
		header = *encoder.header
	}
	explicitWorktree := scipSummaryHasWarning(encoder.summary, "W_WORKTREE_SNAPSHOT")
	worktree := explicitWorktree || scipSummaryHasWarning(encoder.summary, "E_NO_GIT_HEAD")
	if worktree {
		header.Commit = ""
		header.Tree = ""
	}
	encoder.note.UnsupportedRelationCounts = map[string]int{}
	encoder.note.MissingTargetRelations = 0
	encoder.note.MissingEvidenceRelations = 0
	encoder.note.EmittedDefinitions = 0
	encoder.note.EmittedReferences = 0
	encoder.note.EmittedImplementations = 0
	encoder.note.UnlocatedSymbols = 0
	encoder.note.WorktreeSnapshot = worktree
	// header.Commit/Tree are blanked above for a worktree export, so this
	// carries the revision exactly when there is one.
	encoder.note.Commit = header.Commit
	encoder.note.Tree = header.Tree

	documents := map[string]*scippb.Document{}
	documentFor := func(filePath, language string) *scippb.Document {
		if filePath == "" {
			filePath = "_unknown"
		}
		doc, ok := documents[filePath]
		if !ok {
			doc = &scippb.Document{
				RelativePath:     filePath,
				PositionEncoding: scippb.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
			}
			documents[filePath] = doc
		}
		if doc.Language == "" {
			if language != "" {
				doc.Language = scipLanguage(language)
			} else if file, ok := encoder.files[filePath]; ok {
				doc.Language = scipLanguage(file.Language)
			}
		}
		return doc
	}

	filePaths := scipSortedKeys(encoder.files)
	for _, filePath := range filePaths {
		file := encoder.files[filePath]
		_ = documentFor(file.Path, file.Language)
	}

	symbolIDs := scipSortedKeys(encoder.symbols)
	symbols := make(map[string]string, len(symbolIDs))
	for _, id := range symbolIDs {
		symbols[id] = scipSymbol(header, encoder.projectVersion, encoder.symbols[id])
	}
	externalIDs := scipSortedKeys(encoder.externals)
	externals := make(map[string]string, len(externalIDs))
	for _, id := range externalIDs {
		externals[id] = scipExternalSymbol(header, encoder.projectVersion, encoder.externals[id])
	}

	infos := make(map[string]*scippb.SymbolInformation, len(symbolIDs))
	for _, id := range symbolIDs {
		record := encoder.symbols[id]
		scipID := symbols[id]
		if record.FilePath == "" {
			encoder.note.UnlocatedSymbols++
		}
		doc := documentFor(record.FilePath, record.Language)
		info := &scippb.SymbolInformation{
			Symbol:      scipID,
			DisplayName: scipFirstNonEmpty(record.Name, record.QualifiedName, record.ID),
			Kind:        scipKind(record.Kind),
		}
		info.SignatureDocumentation = scipSignatureDocumentation(record.Language, record.Signature)
		if record.ContainerID != "" {
			info.EnclosingSymbol = symbols[record.ContainerID]
		}
		if record.QualifiedName != "" && record.QualifiedName != record.Name {
			info.Documentation = append(info.Documentation, record.QualifiedName)
		}
		infos[id] = info
		doc.Symbols = append(doc.Symbols, info)
		if record.StartLine > 0 {
			// The definition occurrence marks the DECLARATION, not the whole
			// body. Spanning declaration-through-body made the definition
			// overlap every call inside it, so a positional lookup anywhere in a
			// multi-line function matched both the definition and whatever
			// reference sat on that line. The full span is what enclosing_range
			// is for.
			occurrence := scipOccurrenceFromLines(record.StartLine, record.StartLine, scipID, int32(scippb.SymbolRole_Definition))
			if record.EndLine > record.StartLine {
				occurrence.SetEnclosingSourceRange(scipRangeFromLines(record.StartLine, record.EndLine))
			}
			doc.Occurrences = append(doc.Occurrences, occurrence)
			encoder.note.EmittedDefinitions++
		}
	}

	externalInfos := make([]*scippb.SymbolInformation, 0, len(externalIDs))
	for _, id := range externalIDs {
		record := encoder.externals[id]
		info := &scippb.SymbolInformation{
			Symbol:      externals[id],
			DisplayName: scipFirstNonEmpty(record.Value, record.ID),
			Kind:        scipKind(record.Kind),
		}
		info.SignatureDocumentation = scipSignatureDocumentation(record.Language, record.Signature)
		if record.SourceDetails != "" {
			info.Documentation = append(info.Documentation, record.SourceDetails)
		}
		externalInfos = append(externalInfos, info)
	}

	for _, relation := range encoder.relations {
		relationType := strings.ToUpper(relation.Type)
		if relationType == "DEFINES" || relationType == "CONTAINS" {
			continue
		}
		if !scipReferenceRelation(relationType) {
			encoder.note.UnsupportedRelationCounts[relationType]++
			continue
		}
		target := symbols[relation.ToID]
		if target == "" {
			target = externals[relation.ToID]
		}
		if target == "" {
			encoder.note.MissingTargetRelations++
			continue
		}
		if scipImplementationRelation(relationType) {
			// SCIP answers "Find Implementations" from SymbolInformation
			// relationships, not from occurrences. Emitting these only as
			// reference occurrences meant the navigation they exist for
			// returned nothing, and because the types count as supported the
			// loss was not reported either.
			if info := infos[relation.FromID]; info != nil {
				info.Relationships = append(info.Relationships, &scippb.Relationship{
					Symbol:           target,
					IsImplementation: true,
					IsReference:      true,
				})
				encoder.note.EmittedImplementations++
			}
		}
		emitted := false
		for _, evidence := range relation.Evidence {
			if evidence.FilePath == "" || evidence.StartLine <= 0 {
				continue
			}
			doc := documentFor(evidence.FilePath, "")
			doc.Occurrences = append(doc.Occurrences, scipOccurrenceFromLines(evidence.StartLine, evidence.EndLine, target, scipRoles(relationType)))
			encoder.note.EmittedReferences++
			emitted = true
		}
		if !emitted {
			encoder.note.MissingEvidenceRelations++
		}
	}

	index := &scippb.Index{Metadata: scipMetadata(header, explicitWorktree)}
	for _, info := range infos {
		if len(info.Relationships) > 0 {
			info.Relationships = scippb.CanonicalizeRelationships(info.Relationships)
		}
	}
	for _, docPath := range scipSortedKeys(documents) {
		doc := documents[docPath]
		sortSCIPDocument(doc)
		index.Documents = append(index.Documents, doc)
	}
	sort.Slice(externalInfos, func(i, j int) bool { return externalInfos[i].Symbol < externalInfos[j].Symbol })
	index.ExternalSymbols = externalInfos
	return proto.MarshalOptions{Deterministic: true}.Marshal(index)
}

func scipSummaryHasWarning(summary *SnapshotSummary, code string) bool {
	if summary == nil {
		return false
	}
	for _, warning := range summary.Warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func scipMetadata(header SnapshotHeader, worktree bool) *scippb.Metadata {
	arguments := []string{"snapshot", "--format", "scip"}
	if worktree {
		arguments = append(arguments, "--worktree")
	}
	return &scippb.Metadata{
		ToolInfo: &scippb.ToolInfo{
			Name:      ProviderName,
			Version:   header.ProviderVersion,
			Arguments: arguments,
		},
		ProjectRoot:          scipProjectRoot(header.RepoRoot),
		TextDocumentEncoding: scippb.TextEncoding_UTF8,
	}
}

func sortSCIPDocument(doc *scippb.Document) {
	sort.Slice(doc.Occurrences, func(i, j int) bool {
		left, right := doc.Occurrences[i], doc.Occurrences[j]
		if compare := left.Compare(right); compare != 0 {
			return compare < 0
		}
		return left.SymbolRoles < right.SymbolRoles
	})
	sort.Slice(doc.Symbols, func(i, j int) bool {
		left, right := doc.Symbols[i], doc.Symbols[j]
		if left.Symbol != right.Symbol {
			return left.Symbol < right.Symbol
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.DisplayName < right.DisplayName
	})
}

func scipSignatureDocumentation(language, signature string) *scippb.Signature {
	if signature == "" {
		return nil
	}
	return &scippb.Signature{Language: scipLanguage(language), Text: signature}
}

func scipOccurrenceFromLines(startLine, endLine int, symbol string, roles int32) *scippb.Occurrence {
	occurrence := &scippb.Occurrence{Symbol: symbol, SymbolRoles: roles}
	occurrence.SetSourceRange(scipRangeFromLines(startLine, endLine))
	return occurrence
}

func scipRangeFromLines(startLine, endLine int) scippb.Range {
	if startLine < 1 {
		startLine = 1
	}
	if endLine < startLine {
		endLine = startLine
	}
	start := scippb.Position{Line: int32(startLine - 1)}
	// Entire Graph records line spans but not columns. Cover each complete
	// inclusive source line so SCIP position lookups can hit the occurrence.
	end := scippb.Position{Line: int32(endLine)}
	return scippb.Range{Start: start, End: end}
}

func scipReferenceRelation(relationType string) bool {
	switch relationType {
	case "IMPORTS", "CALLS", "CONSTRUCTS", "ASYNC_CALLS", "EXTENDS", "INHERITS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES":
		return true
	default:
		return false
	}
}

// scipImplementationRelation reports whether a relation is the kind SCIP
// expresses as a SymbolInformation relationship rather than only as an
// occurrence.
func scipImplementationRelation(relationType string) bool {
	switch relationType {
	case "IMPLEMENTS", "INHERITS", "EXTENDS", "OVERRIDES":
		return true
	}
	return false
}

func scipRoles(relationType string) int32 {
	switch relationType {
	case "IMPORTS":
		return int32(scippb.SymbolRole_Import)
	case "WRITES_FIELD":
		return int32(scippb.SymbolRole_WriteAccess)
	case "READS_FIELD":
		return int32(scippb.SymbolRole_ReadAccess)
	default:
		return 0
	}
}

func scipKind(kind string) scippb.SymbolInformation_Kind {
	switch strings.ToLower(kind) {
	case "method":
		return scippb.SymbolInformation_Method
	case "function", "func":
		return scippb.SymbolInformation_Function
	case "constructor":
		return scippb.SymbolInformation_Constructor
	case "class":
		return scippb.SymbolInformation_Class
	case "interface":
		return scippb.SymbolInformation_Interface
	case "struct":
		return scippb.SymbolInformation_Struct
	case "enum":
		return scippb.SymbolInformation_Enum
	case "macro":
		return scippb.SymbolInformation_Macro
	case "message":
		return scippb.SymbolInformation_Message
	case "rpc":
		return scippb.SymbolInformation_Method
	case "service":
		return scippb.SymbolInformation_Interface
	case "trait":
		return scippb.SymbolInformation_Trait
	case "union":
		return scippb.SymbolInformation_Union
	case "field":
		return scippb.SymbolInformation_Field
	case "property":
		return scippb.SymbolInformation_Property
	case "constant":
		return scippb.SymbolInformation_Constant
	case "variable":
		return scippb.SymbolInformation_Variable
	case "module":
		return scippb.SymbolInformation_Module
	case "namespace":
		return scippb.SymbolInformation_Namespace
	case "package":
		return scippb.SymbolInformation_Package
	case "type":
		return scippb.SymbolInformation_Type
	case "type_alias":
		return scippb.SymbolInformation_TypeAlias
	default:
		return 0
	}
}

func scipSymbol(header SnapshotHeader, projectVersion string, record SymbolRecord) string {
	name := scipFirstNonEmpty(record.Name, record.QualifiedName, "symbol")
	return scipSymbolPrefix(header, projectVersion, record.FilePath) + scipDescriptor(name, record.Kind, shortDigest(record.ID))
}

func scipDescriptor(name, kind, short string) string {
	if methodLikeSCIPKind(kind) {
		return scipIdentifier(name) + "(s" + short + ")."
	}
	descriptorName := name
	if !strings.Contains(descriptorName, short) {
		descriptorName += "$" + short
	}
	suffix := "."
	switch strings.ToLower(kind) {
	case "class", "interface", "struct", "enum", "message", "service", "trait", "type", "type_alias", "union":
		suffix = "#"
	case "module", "namespace", "package":
		suffix = "/"
	case "macro":
		suffix = "!"
	}
	return scipIdentifier(descriptorName) + suffix
}

func methodLikeSCIPKind(kind string) bool {
	switch strings.ToLower(kind) {
	case "constructor", "func", "function", "method", "rpc":
		return true
	default:
		return false
	}
}

func scipExternalSymbol(header SnapshotHeader, projectVersion string, record ExternalRecord) string {
	name := scipFirstNonEmpty(record.Value, record.ID, "external")
	return scipSymbolPackage(header, projectVersion) + "external/" + scipDescriptor(name, record.Kind, shortDigest(record.ID))
}

func scipSymbolPrefix(header SnapshotHeader, projectVersion, filePath string) string {
	prefix := scipSymbolPackage(header, projectVersion)
	if filePath != "" {
		prefix += scipIdentifier(filePath) + "/"
	}
	return prefix
}

func scipSymbolPackage(header SnapshotHeader, projectVersion string) string {
	packageName := scipFirstNonEmpty(header.RepoKey, "repository")
	// The version is the project's own, from its root manifest, and NOT the
	// commit. Using the commit made every symbol's identity change on every
	// commit: two indexes of the same repository one unrelated commit apart
	// shared no symbol at all, so nothing downstream could match a symbol
	// across commits. Commit and tree provenance are carried by Metadata, the
	// feed's own addressing, and the omission note's worktree_snapshot flag.
	version := scipFirstNonEmpty(projectVersion, ScipProjectVersionUnknown)
	return "entire-graph . " + scipPackageComponent(packageName) + " " + scipPackageComponent(version) + " "
}

func scipPackageComponent(value string) string {
	if value == "" {
		return "."
	}
	return strings.ReplaceAll(value, " ", "  ")
}

func scipIdentifier(value string) string {
	if value == "" {
		return "."
	}
	simple := true
	for _, r := range value {
		if !(r == '_' || r == '+' || r == '-' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			simple = false
			break
		}
	}
	if simple {
		return value
	}
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func scipLanguage(language string) string {
	language = strings.TrimSpace(language)
	switch language {
	case "Bash", "Zsh":
		return "ShellScript"
	case "C++":
		return "CPP"
	case "C#":
		return "CSharp"
	case "CoffeeScript":
		return "Coffeescript"
	case "Common Lisp":
		return "CommonLisp"
	case "F#":
		return "FSharp"
	case "INI":
		return "Ini"
	case "Just":
		return "Justfile"
	case "Make":
		return "Makefile"
	case "MATLAB":
		return "Matlab"
	case "Objective-C":
		return "Objective_C"
	case "Objective-C++":
		return "Objective_CPP"
	case "Protocol Buffers":
		return "Protobuf"
	case "reStructuredText":
		return "ReST"
	case "Starlark":
		return "Skylark"
	case "Visual Basic .NET":
		return "VisualBasic"
	default:
		// Provider names that already match the SCIP enum need no translation.
		// Unknown languages remain truthful instead of acquiring an invented ID.
		return language
	}
}

func scipProjectRoot(root string) string {
	if root == "" {
		return ""
	}
	// Normalize before building the URI. url.URL takes Path verbatim, so a
	// Windows path went out malformed: "C:\\repo" became "file://C:%5Crepo",
	// with the drive read as an authority and the separators percent-escaped,
	// which no SCIP consumer can resolve back to a directory.
	path := strings.ReplaceAll(root, "\\", "/")
	// Extended-length and device prefixes ("\\\\?\\C:\\repo",
	// "\\\\.\\C:\\repo") normalize to "//?/C:/repo", which the UNC branch below
	// would otherwise read as a host of "?". Strip the prefix first and let the
	// drive-letter branch handle what is left.
	for _, prefix := range []string{"//?/UNC/", "//?/", "//./"} {
		if strings.HasPrefix(path, prefix) {
			rest := strings.TrimPrefix(path, prefix)
			if prefix == "//?/UNC/" {
				path = "//" + rest
			} else {
				path = rest
			}
			break
		}
	}
	if host, share, ok := scipUNCParts(path); ok {
		// A UNC path's server is the URI authority: \\server\share\dir is
		// file://server/share/dir.
		return (&url.URL{Scheme: "file", Host: host, Path: share}).String()
	}
	if scipHasDriveLetter(path) {
		// A drive letter is part of the path, not an authority, so it needs the
		// leading slash that makes the authority empty: file:///C:/repo.
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// scipHasDriveLetter reports whether path begins with a Windows drive
// specifier such as "C:".
func scipHasDriveLetter(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	letter := path[0]
	return (letter >= 'a' && letter <= 'z') || (letter >= 'A' && letter <= 'Z')
}

// scipUNCParts splits a slash-normalized UNC path into its server and the
// remainder. It requires a non-empty server and share, so "//" alone is not a
// UNC path.
func scipUNCParts(path string) (string, string, bool) {
	if !strings.HasPrefix(path, "//") {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, "//")
	host, remainder, found := strings.Cut(rest, "/")
	if host == "" || !found || remainder == "" {
		return "", "", false
	}
	return host, "/" + remainder, true
}

func scipSortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func scipFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func shortDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

// DecodeCompactSnapshot validates and decodes records incrementally. It retains
// only the string dictionary; callers that need materialization use the loader.
func DecodeCompactSnapshot(in io.Reader, emit func(any) error) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	dictionary := []string{""}
	known := map[string]bool{"": true}
	seenHeader, seenSummary := false, false
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(line) == 0 {
			return fmt.Errorf("compact snapshot line %d is blank", lineNumber)
		}
		if seenSummary {
			return fmt.Errorf("compact snapshot has record after summary at line %d", lineNumber)
		}
		var fields []json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			return fmt.Errorf("compact snapshot line %d: %w", lineNumber, err)
		}
		if len(fields) == 0 {
			return fmt.Errorf("compact snapshot line %d has no tag", lineNumber)
		}
		var tag string
		if err := json.Unmarshal(fields[0], &tag); err != nil {
			return fmt.Errorf("compact snapshot line %d tag: %w", lineNumber, err)
		}
		switch tag {
		case "h":
			if seenHeader || lineNumber != 1 || len(fields) != 3 {
				return fmt.Errorf("compact snapshot header must be first and have arity 3")
			}
			var version int
			if err := json.Unmarshal(fields[1], &version); err != nil {
				return fmt.Errorf("compact snapshot header version: %w", err)
			}
			if version != CompactSnapshotFormatVersion {
				return fmt.Errorf("unsupported compact snapshot version %d", version)
			}
			var header SnapshotHeader
			if err := json.Unmarshal(fields[2], &header); err != nil {
				return fmt.Errorf("compact snapshot header: %w", err)
			}
			seenHeader = true
			if err := emit(header); err != nil {
				return err
			}
		case "d":
			if !seenHeader || seenSummary || len(fields) != 3 {
				return fmt.Errorf("compact snapshot dictionary has invalid placement or arity")
			}
			var base int
			var stringsToAdd []string
			if err := json.Unmarshal(fields[1], &base); err != nil {
				return fmt.Errorf("compact snapshot dictionary base: %w", err)
			}
			if err := json.Unmarshal(fields[2], &stringsToAdd); err != nil {
				return fmt.Errorf("compact snapshot dictionary values: %w", err)
			}
			if base != len(dictionary) {
				return fmt.Errorf("compact snapshot dictionary base %d does not equal %d", base, len(dictionary))
			}
			if len(stringsToAdd) == 0 {
				return errors.New("compact snapshot dictionary update is empty")
			}
			for _, value := range stringsToAdd {
				if value == "" {
					return errors.New("compact snapshot dictionary repeats empty string")
				}
				if known[value] {
					return fmt.Errorf("compact snapshot dictionary duplicates %q", value)
				}
				known[value] = true
				dictionary = append(dictionary, value)
			}
		case "f", "x", "s", "r":
			if !seenHeader {
				return errors.New("compact snapshot data requires header")
			}
			record, err := decodeCompactData(tag, fields, dictionary)
			if err != nil {
				return fmt.Errorf("compact snapshot line %d: %w", lineNumber, err)
			}
			if err := emit(record); err != nil {
				return err
			}
		case "m":
			if !seenHeader || len(fields) != 2 {
				return fmt.Errorf("compact snapshot summary has invalid placement or arity")
			}
			var summary SnapshotSummary
			if err := json.Unmarshal(fields[1], &summary); err != nil {
				return fmt.Errorf("compact snapshot summary: %w", err)
			}
			seenSummary = true
			if err := emit(summary); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown compact snapshot tag %q", tag)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("compact snapshot scan: %w", err)
	}
	if !seenHeader {
		return errors.New("compact snapshot is missing header")
	}
	if !seenSummary {
		return errors.New("compact snapshot is missing summary")
	}
	return nil
}

func decodeCompactData(tag string, fields []json.RawMessage, dictionary []string) (any, error) {
	stringAt := func(position int) (string, error) {
		var index int
		if err := json.Unmarshal(fields[position], &index); err != nil {
			return "", err
		}
		if index < 0 || index >= len(dictionary) {
			return "", fmt.Errorf("dictionary index %d is out of range", index)
		}
		return dictionary[index], nil
	}
	integerAt := func(position int) (int, error) { var value int; return value, json.Unmarshal(fields[position], &value) }
	switch tag {
	case "f":
		if len(fields) != 6 {
			return nil, fmt.Errorf("file record arity %d, want 6", len(fields))
		}
		id, e1 := stringAt(1)
		path, e2 := stringAt(2)
		blob, e3 := stringAt(3)
		language, e4 := stringAt(4)
		size, e5 := integerAt(5)
		if err := firstCompactError(e1, e2, e3, e4, e5); err != nil {
			return nil, err
		}
		return FileRecord{RecordType: "file", ID: id, Path: path, Blob: blob, Language: language, Bytes: size}, nil
	case "x":
		if len(fields) != 12 {
			return nil, fmt.Errorf("external record arity %d, want 12", len(fields))
		}
		values := make([]string, 8)
		for i, position := range []int{1, 2, 3, 4, 7, 8, 10, 11} {
			var err error
			values[i], err = stringAt(position)
			if err != nil {
				return nil, err
			}
		}
		start, e1 := integerAt(5)
		end, e2 := integerAt(6)
		var external bool
		e3 := json.Unmarshal(fields[9], &external)
		if err := firstCompactError(e1, e2, e3); err != nil {
			return nil, err
		}
		return ExternalRecord{RecordType: "external", ID: values[0], Kind: values[1], Value: values[2], FilePath: values[3], StartLine: start, EndLine: end, Signature: values[4], Language: values[5], External: external, SourceSymbol: values[6], SourceDetails: values[7]}, nil
	case "s":
		if len(fields) != 14 {
			return nil, fmt.Errorf("symbol record arity %d, want 14", len(fields))
		}
		values := make([]string, 10)
		for i, position := range []int{1, 2, 3, 4, 5, 6, 9, 10, 11, 12} {
			var err error
			values[i], err = stringAt(position)
			if err != nil {
				return nil, err
			}
		}
		start, e1 := integerAt(7)
		end, e2 := integerAt(8)
		if err := firstCompactError(e1, e2); err != nil {
			return nil, err
		}
		var aliasIndexes []int
		if err := json.Unmarshal(fields[13], &aliasIndexes); err != nil {
			return nil, err
		}
		aliases := make([]string, len(aliasIndexes))
		for i, index := range aliasIndexes {
			if index < 0 || index >= len(dictionary) {
				return nil, fmt.Errorf("dictionary index %d is out of range", index)
			}
			aliases[i] = dictionary[index]
		}
		return SymbolRecord{RecordType: "symbol", ID: values[0], StableIDVersion: values[1], Kind: values[2], Name: values[3], QualifiedName: values[4], FilePath: values[5], StartLine: start, EndLine: end, Signature: values[6], BodyHash: values[7], Language: values[8], ContainerID: values[9], Aliases: aliases}, nil
	case "r":
		if len(fields) != 11 {
			return nil, fmt.Errorf("relation record arity %d, want 11", len(fields))
		}
		values := make([]string, 7)
		for i, position := range []int{1, 2, 3, 5, 6, 7, 8} {
			var err error
			values[i], err = stringAt(position)
			if err != nil {
				return nil, err
			}
		}
		var confidence float64
		if err := json.Unmarshal(fields[4], &confidence); err != nil {
			return nil, err
		}
		var rawEvidence [][]json.RawMessage
		if err := json.Unmarshal(fields[9], &rawEvidence); err != nil {
			return nil, err
		}
		evidence := make([]Evidence, len(rawEvidence))
		for i, raw := range rawEvidence {
			if len(raw) != 5 {
				return nil, fmt.Errorf("evidence arity %d, want 5", len(raw))
			}
			var err error
			evidence[i].Kind, err = compactRawString(raw[0], dictionary)
			if err != nil {
				return nil, err
			}
			evidence[i].FilePath, err = compactRawString(raw[1], dictionary)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(raw[2], &evidence[i].StartLine); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(raw[3], &evidence[i].EndLine); err != nil {
				return nil, err
			}
			evidence[i].Detail, err = compactRawString(raw[4], dictionary)
			if err != nil {
				return nil, err
			}
		}
		var warningIndexes []int
		if err := json.Unmarshal(fields[10], &warningIndexes); err != nil {
			return nil, err
		}
		var warnings []string
		if warningIndexes != nil {
			warnings = make([]string, len(warningIndexes))
			for i, index := range warningIndexes {
				if index < 0 || index >= len(dictionary) {
					return nil, fmt.Errorf("dictionary index %d is out of range", index)
				}
				warnings[i] = dictionary[index]
			}
		}
		return RelationRecord{RecordType: "relation", FromID: values[0], ToID: values[1], Type: values[2], Confidence: confidence, Reason: values[3], RelationScope: values[4], Resolution: values[5], TargetKind: values[6], Evidence: evidence, WarningCodes: warnings}, nil
	}
	return nil, fmt.Errorf("unknown compact data tag %q", tag)
}

func compactRawString(raw json.RawMessage, dictionary []string) (string, error) {
	var index int
	if err := json.Unmarshal(raw, &index); err != nil {
		return "", err
	}
	if index < 0 || index >= len(dictionary) {
		return "", fmt.Errorf("dictionary index %d is out of range", index)
	}
	return dictionary[index], nil
}
func firstCompactError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// SnapshotSemanticHasher hashes the canonical public NDJSON projection rather
// than the compact representation. A different dictionary layout therefore can
// never change the semantic identity of an otherwise identical snapshot.
type SnapshotSemanticHasher struct {
	digest stdhash.Hash
	buffer bytes.Buffer
}

func NewSnapshotSemanticHasher() *SnapshotSemanticHasher {
	return &SnapshotSemanticHasher{digest: sha256.New()}
}
func (hasher *SnapshotSemanticHasher) Add(record any) error {
	public, err := publicSnapshotRecord(record)
	if err != nil {
		return err
	}
	hasher.buffer.Reset()
	encoder := json.NewEncoder(&hasher.buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(public); err != nil {
		return err
	}
	_, err = hasher.digest.Write(hasher.buffer.Bytes())
	if err != nil {
		return err
	}
	return nil
}
func (hasher *SnapshotSemanticHasher) SumHex() string {
	return hex.EncodeToString(hasher.digest.Sum(nil))
}

func publicSnapshotRecord(record any) (any, error) {
	switch record.(type) {
	case SnapshotHeader, FileRecord, ExternalRecord, SymbolRecord, RelationRecord, SnapshotSummary:
		return record, nil
	default:
		return nil, fmt.Errorf("unsupported snapshot public record %T", record)
	}
}

func publicSnapshotRecordJSON(record any) ([]byte, error) {
	public, err := publicSnapshotRecord(record)
	if err != nil {
		return nil, err
	}
	return compactJSONLine(public)
}
