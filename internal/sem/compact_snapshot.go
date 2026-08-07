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
