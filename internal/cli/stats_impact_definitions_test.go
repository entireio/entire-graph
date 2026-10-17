package cli

import (
	"slices"
	"strings"
	"testing"
)

// TestGraphOutputPathsReadsClippedImpactDisambiguation: an ambiguous `impact`
// answers with the candidate definitions (and their bodies), not with a focus.
// Clipped by `| head`, that output used to yield no paths at all, because
// neither "definitions" nor "match_bodies" opened an answer section, so a
// genuinely answered impact call was classified ineligible for follow-up.
func TestGraphOutputPathsReadsClippedImpactDisambiguation(t *testing.T) {
	t.Parallel()
	whole := `{"format_version":1,"repo_root":"/r","commit":"c","tree":"t","profile":"full","query":"Run","depth":2,` +
		`"focus_matches_total":2,"disambiguation_required":true,` +
		`"definitions":[{"id":"x:a/run.go:Run","name":"Run","kind":"function","file_path":"a/run.go","start_line":3,"end_line":3},` +
		`{"id":"x:b/run.go:Run","name":"Run","kind":"function","file_path":"b/run.go","start_line":3,"end_line":3}],` +
		`"callers":{"total":0,"entries":null},"partial_failures":[],"stats":{"files":2}}`
	clipped := whole[:strings.Index(whole, `"callers"`)+len(`"callers":{"tot`)]
	for name, text := range map[string]string{"whole": whole, "clipped": clipped} {
		got := graphOutputPaths(text)
		if !slices.Equal(got, []string{"a/run.go", "b/run.go"}) {
			t.Fatalf("%s impact disambiguation: paths %q, want both definitions", name, got)
		}
	}
	bodies := `{"query":"Run","match_bodies":[{"name":"Run","file_path":"a/run.go","start_line":3,"end_line":3,"source":"func Run() int { return 1 }"}],"disambiguation_required":true,"defin`
	if got := graphOutputPaths(bodies); !slices.Equal(got, []string{"a/run.go"}) {
		t.Fatalf("clipped match_bodies: paths %q, want the body's file", got)
	}
}
