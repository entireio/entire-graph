package sem

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"unicode/utf8"
)

var (
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	shapeOnce         sync.Once
	shapeErr          error
)

// validateExtractionRecord protects the private cache wire format from values
// that encoding/json would silently change. Shape validation is performed once
// per process; the normal cache path checks only the record's string leaves.
func validateExtractionRecord(record extractionRecord) error {
	shapeOnce.Do(func() {
		shapeErr = validateExtractionType(reflect.TypeOf(record), "record")
	})
	if shapeErr != nil {
		return shapeErr
	}
	for index, value := range record.RawImports {
		if err := validateExtractionIndexedString(value, "record.RawImports", index, ""); err != nil {
			return err
		}
	}
	if err := validateExtractionString(record.Language, "record.Language"); err != nil {
		return err
	}
	if err := validateExtractionString(record.Status.Code, "record.Status.Code"); err != nil {
		return err
	}
	if err := validateExtractionString(record.Status.Detail, "record.Status.Detail"); err != nil {
		return err
	}
	for index, declaration := range record.Declarations {
		if err := validateExtractionIndexedString(declaration.Kind, "record.Declarations", index, "Kind"); err != nil {
			return err
		}
		if err := validateExtractionIndexedString(declaration.Name, "record.Declarations", index, "Name"); err != nil {
			return err
		}
		if err := validateExtractionIndexedString(declaration.Signature, "record.Declarations", index, "Signature"); err != nil {
			return err
		}
		if err := validateExtractionIndexedString(declaration.BodyHash, "record.Declarations", index, "BodyHash"); err != nil {
			return err
		}
		if err := validateExtractionIndexedString(declaration.Fingerprint, "record.Declarations", index, "Fingerprint"); err != nil {
			return err
		}
		if err := validateExtractionIndexedString(declaration.ParamTypeText, "record.Declarations", index, "ParamTypeText"); err != nil {
			return err
		}
		if err := validateExtractionIndexedString(declaration.ReturnTypeText, "record.Declarations", index, "ReturnTypeText"); err != nil {
			return err
		}
		for parameterIndex, parameter := range declaration.ParameterNames {
			if err := validateExtractionParameterString(parameter, index, parameterIndex); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateExtractionParameterString(value string, declarationIndex, parameterIndex int) error {
	if utf8.ValidString(value) {
		return nil
	}
	return fmt.Errorf("extraction record field record.Declarations[%d].ParameterNames[%d] contains invalid UTF-8", declarationIndex, parameterIndex)
}

func validateExtractionIndexedString(value, collection string, index int, field string) error {
	if utf8.ValidString(value) {
		return nil
	}
	if field == "" {
		return fmt.Errorf("extraction record field %s[%d] contains invalid UTF-8", collection, index)
	}
	return fmt.Errorf("extraction record field %s[%d].%s contains invalid UTF-8", collection, index, field)
}

func validateExtractionString(value, path string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("extraction record field %s contains invalid UTF-8", path)
	}
	return nil
}

// validateExtractionValue is retained as a structural test seam. Production
// validation uses validateExtractionType once plus direct string checks above.
func validateExtractionValue(value reflect.Value, path string) error {
	if !value.IsValid() {
		return nil
	}
	return validateExtractionType(value.Type(), path)
}

func validateExtractionType(typ reflect.Type, path string) error {
	if typ.Implements(jsonMarshalerType) || typ.Implements(textMarshalerType) || (typ.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(jsonMarshalerType)) || (typ.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(textMarshalerType)) {
		return fmt.Errorf("extraction record field %s has a custom JSON/text marshaler", path)
	}
	switch typ.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return nil
	case reflect.Struct:
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			if field.PkgPath != "" {
				return fmt.Errorf("extraction record field %s.%s is unexported", path, field.Name)
			}
			if tag, ok := field.Tag.Lookup("json"); ok && tag != "" {
				return fmt.Errorf("extraction record field %s.%s has JSON tag %q", path, field.Name, tag)
			}
			if err := validateExtractionType(field.Type, path+"."+field.Name); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		return validateExtractionType(typ.Elem(), path+"[]")
	default:
		return fmt.Errorf("extraction record field %s has unsupported JSON shape %s", path, typ)
	}
}
