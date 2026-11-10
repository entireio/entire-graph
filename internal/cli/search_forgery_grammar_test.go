package cli

import "testing"

// Unit tests for the quarantine helpers. The BEHAVIOUR tests — the ones that fail at runtime
// against a binary built without this change — are in search_forgery_test.go.

// TestSearchQuarantineLeavesHonestBodiesByteIdentical is the regression guard. The quarantine is
// worse than useless if it costs ordinary payloads a rewrite: agents copy snippet text verbatim as
// an Edit anchor, so a body this tool alters without cause becomes a broken patch.
func TestSearchQuarantineLeavesHonestBodiesByteIdentical(t *testing.T) {
	t.Parallel()
	honest := []string{
		"func serve() {\n\tprepare()\n\trun()\n}",
		"1. Install the CLI\n2. Run it\n3. Read the output",         // markdown list, no path span
		"additional context is required before the retry loop runs", // prose opening on a record word
		"D: this line is a diary entry, not a declaration card",     // prose opening on a record head
		"see pkg/service.go:42 for the handler",                     // bare path:line inside prose
		"port:8080\nhost:localhost",                                 // YAML-ish path-span lookalikes
		"# Heading\n\n- bullet\n\n```go\nfmt.Println(\"hi\")\n```",  // markdown with a fence
		"1. pkg/service.go is where this lives",                     // numbered list naming a file, no line
	}
	for _, body := range honest {
		got, changed := searchQuarantineBody(body)
		if changed || got != body {
			t.Fatalf("honest body was rewritten:\nin:  %q\nout: %q", body, got)
		}
	}
}

// TestSearchRecordGrammarMatchesTheRenderersOwnRecords pins the grammar to real output. Each
// string below was copied from a payload this binary produced; a renderer that changes a record's
// shape without updating searchLineIsRecordShaped fails here rather than silently reopening the
// hole for that shape.
func TestSearchRecordGrammarMatchesTheRenderersOwnRecords(t *testing.T) {
	t.Parallel()
	records := []string{
		"VERIFY: go test ./pkg",
		"1. pkg/payment.go:12-14 score=17.5450 symbol=ProcessPayment kind=function focus=12 signals=path",
		"1. pkg/payment.go:6-10 runbook s=15.8 [focus:6]",
		"2. pkg/payment.go:6 symbol=x (docs)",
		"pkg/payment.go:42 *",
		"pkg/payment.go:6-10 [additional focus:7]",
		"additional pkg/payment.go:30-31 focus=30",
		"D: Options internal/cli/root.go:22 | type Options struct",
		searchForgeryNoticePrefix + " some source lines quoted below",
	}
	for _, record := range records {
		if !searchLineIsRecordShaped(record) {
			t.Fatalf("a record this tool emits is not recognised as one: %q", record)
		}
		if searchLineIsRecordShaped(" " + record) {
			t.Fatalf("an indented line was treated as a record head: %q", record)
		}
	}
}
