package sem

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"unicode/utf8"
)

// validateExtractionRecord protects the private cache wire format from values
// that encoding/json would silently change. The record deliberately uses only
// plain exported structs, scalar values, and slices; new wire shapes require an
// explicit review rather than inheriting JSON's lossy behavior.
func validateExtractionRecord(record extractionRecord) error {
	return validateExtractionValue(reflect.ValueOf(record), "record")
}

var (
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

func validateExtractionValue(value reflect.Value, path string) error {
	if !value.IsValid() {
		return nil
	}
	typ := value.Type()
	if typ.Implements(jsonMarshalerType) || typ.Implements(textMarshalerType) || (typ.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(jsonMarshalerType)) || (typ.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(textMarshalerType)) {
		return fmt.Errorf("extraction record field %s has a custom JSON/text marshaler", path)
	}
	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return fmt.Errorf("extraction record field %s contains invalid UTF-8", path)
		}
		return nil
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return nil
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			field := typ.Field(index)
			if field.PkgPath != "" {
				return fmt.Errorf("extraction record field %s.%s is unexported", path, field.Name)
			}
			if tag, ok := field.Tag.Lookup("json"); ok && tag != "" {
				return fmt.Errorf("extraction record field %s.%s has JSON tag %q", path, field.Name, tag)
			}
			if err := validateExtractionValue(value.Field(index), path+"."+field.Name); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			if err := validateExtractionValue(value.Index(index), fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("extraction record field %s has unsupported JSON shape %s", path, typ)
	}
}
