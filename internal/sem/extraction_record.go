package sem

// extractionFormatVersion is private, exact-match data identity. This payload
// is not a public graph record and is not yet persisted.
const extractionFormatVersion = 1

// extractionRecord contains file-local declarations only. Repository aliases,
// final IDs, relation resolution and synthetic boundary symbols are recomputed.
// No raw relation family is present in version 1.
type extractionRecord struct {
	Version      int
	Language     string
	Status       ParseStatus
	Declarations []extractedDeclaration
}

// extractedDeclaration explicitly preserves parser metadata that Entity JSON
// intentionally omits. Do not embed Entity: its wire shape is not this contract.
type extractedDeclaration struct {
	Kind                string
	Name                string
	Signature           string
	StartLine           int
	EndLine             int
	BodyHash            string
	Fingerprint         string
	Local               bool
	Bodyless            bool
	CLinkage            bool
	SourceStartByte     int
	SourceEndByte       int
	ParameterNames      []string
	ParameterNamesKnown bool
	ParamTypeText       string
	ReturnTypeText      string
	SignatureTypesKnown bool
}

// fileExtraction owns the freshly parsed entities. Default-off persistence does
// not pay serialization/deep-copy costs; recordExtraction makes an independent
// payload only when requested by storage or equivalence tests.
type fileExtraction struct {
	entities []Entity
	language string
	status   ParseStatus
}

func extractCapturedSource(spec profileSpec, language languageSpec, source capturedSource) fileExtraction {
	entities, name, status := parseWithProfile(TreeSitterParser{}, spec, language, source.path, source.content)
	return fileExtraction{entities: entities, language: name, status: status}
}

func recordExtraction(entities []Entity, language string, status ParseStatus) extractionRecord {
	record := extractionRecord{Version: extractionFormatVersion, Language: language, Status: status}
	if entities == nil {
		return record
	}
	record.Declarations = make([]extractedDeclaration, len(entities))
	for i, entity := range entities {
		record.Declarations[i] = extractedDeclaration{
			Kind:                entity.Kind,
			Name:                entity.Name,
			Signature:           entity.Signature,
			StartLine:           entity.StartLine,
			EndLine:             entity.EndLine,
			BodyHash:            entity.BodyHash,
			Fingerprint:         entity.Fingerprint,
			Local:               entity.Local,
			Bodyless:            entity.bodyless,
			CLinkage:            entity.cLinkage,
			SourceStartByte:     entity.sourceStartByte,
			SourceEndByte:       entity.sourceEndByte,
			ParameterNames:      cloneExtractionStrings(entity.parameterNames),
			ParameterNamesKnown: entity.parameterNamesKnown,
			ParamTypeText:       entity.paramTypeText,
			ReturnTypeText:      entity.returnTypeText,
			SignatureTypesKnown: entity.signatureTypesKnown,
		}
	}
	return record
}

func (record extractionRecord) entities() []Entity {
	if record.Declarations == nil {
		return nil
	}
	entities := make([]Entity, len(record.Declarations))
	for i, declaration := range record.Declarations {
		entities[i] = Entity{
			Kind:                declaration.Kind,
			Name:                declaration.Name,
			Signature:           declaration.Signature,
			StartLine:           declaration.StartLine,
			EndLine:             declaration.EndLine,
			BodyHash:            declaration.BodyHash,
			Fingerprint:         declaration.Fingerprint,
			Local:               declaration.Local,
			bodyless:            declaration.Bodyless,
			cLinkage:            declaration.CLinkage,
			sourceStartByte:     declaration.SourceStartByte,
			sourceEndByte:       declaration.SourceEndByte,
			parameterNames:      cloneExtractionStrings(declaration.ParameterNames),
			parameterNamesKnown: declaration.ParameterNamesKnown,
			paramTypeText:       declaration.ParamTypeText,
			returnTypeText:      declaration.ReturnTypeText,
			signatureTypesKnown: declaration.SignatureTypesKnown,
		}
	}
	return entities
}

func cloneExtractionStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	copy(result, values)
	return result
}
