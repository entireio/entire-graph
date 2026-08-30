package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// Every other machine-readable surface tells a consumer which shape it is
// looking at. Measured on origin/main against one fixture repository:
//
//	schema_version : diff, commit, capabilities, symbols, edges, snapshot,
//	                 compact-ndjson
//	format_version : impact, neighbors, def, index, stats
//	NEITHER        : search --format json, search --format ndjson
//
// search is the documented entry point ("start here") and JSON is its DEFAULT
// format, so the one surface a consumer is most likely to parse was the one it
// could not version-check. These tests keep the discriminator on both formats.
func TestSearchJSONCarriesFormatVersion(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	response := sem.SearchResponse{FormatVersion: sem.SearchFormatVersion, Query: "q", Results: []sem.SearchResult{}}
	if err := writeSearchResponse(&out, response, "json", 0); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		FormatVersion *int `json:"format_version"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode search json: %v\n%s", err, out.String())
	}
	if payload.FormatVersion == nil {
		t.Fatalf("search json carries no format_version:\n%s", out.String())
	}
	if *payload.FormatVersion != sem.SearchFormatVersion {
		t.Fatalf("format_version = %d, want %d", *payload.FormatVersion, sem.SearchFormatVersion)
	}
}

func TestSearchNDJSONHeaderCarriesFormatVersion(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	response := sem.SearchResponse{FormatVersion: sem.SearchFormatVersion, Query: "q", Results: []sem.SearchResult{}}
	if err := writeNdjsonSearch(&out, response); err != nil {
		t.Fatal(err)
	}
	first, _, found := strings.Cut(out.String(), "\n")
	if !found {
		t.Fatalf("ndjson stream has no header line:\n%s", out.String())
	}
	var header struct {
		RecordType    string `json:"record_type"`
		FormatVersion *int   `json:"format_version"`
	}
	if err := json.Unmarshal([]byte(first), &header); err != nil {
		t.Fatalf("decode ndjson header: %v\n%s", err, first)
	}
	if header.RecordType != "search_header" {
		t.Fatalf("first record = %q, want search_header", header.RecordType)
	}
	if header.FormatVersion == nil {
		t.Fatalf("ndjson search_header carries no format_version:\n%s", first)
	}
	if *header.FormatVersion != sem.SearchFormatVersion {
		t.Fatalf("format_version = %d, want %d", *header.FormatVersion, sem.SearchFormatVersion)
	}
}
