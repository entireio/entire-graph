package sem

import (
	"context"
	"reflect"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestMaskBashCommentBacktickArgumentsPreservesGraphContext(t *testing.T) {
	source := "kube::log::status() { :; }\n" +
		"helper() { :; }\n" +
		"outer() {\n" +
		"  git_grep -e '^// *+k8s:' `# match +k8s: tags` -- ':(glob)**/*.go' `# in any *.go file`\n" +
		"  result=$(helper)\n" +
		"  V=3 kube::log::status\n" +
		"}\n"

	spec, ok := languageForPath("fixture.sh")
	if !ok || spec.grammar == nil {
		t.Fatal("Bash grammar unavailable")
	}
	rawParser := sitter.NewParser()
	defer rawParser.Close()
	rawParser.SetLanguage(spec.grammar)
	rawTree, err := rawParser.ParseCtx(context.Background(), nil, []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	defer rawTree.Close()
	if !rawTree.RootNode().HasError() {
		t.Fatal("raw tree-sitter Bash unexpectedly accepted comment backtick arguments")
	}

	entities, language, status := TreeSitterParser{}.ParseWithStatus("fixture.sh", source)
	if language != "Bash" || status.ParseError {
		t.Fatalf("public parse failed after compatibility masks: language=%q status=%+v", language, status)
	}
	var outer *Entity
	for i := range entities {
		if entities[i].Kind == "function" && entities[i].Name == "outer" {
			outer = &entities[i]
			break
		}
	}
	if outer == nil || outer.StartLine != 3 || outer.EndLine != 7 || outer.BodyHash == "" {
		t.Fatalf("authored outer entity missing range/hash: %+v", entities)
	}

	repo := t.TempDir()
	writeFile(t, repo, "fixture.sh", source)
	uncached, err := BuildProviderSnapshot(t.Context(), repo, "bash-comment-fixture")
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	cached, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "bash-comment-fixture", ProviderSnapshotOptions{
		Worktree:           true,
		ExtractionReuse:    true,
		ExtractionCacheDir: cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(uncached.Symbols, cached.Symbols) || !reflect.DeepEqual(uncached.Relations, cached.Relations) {
		t.Fatalf("cache changed authored graph: uncached symbols=%v relations=%v cached symbols=%v relations=%v", uncached.Symbols, uncached.Relations, cached.Symbols, cached.Relations)
	}
	if !hasRelationBySymbolNameAndFile(cached, "CALLS", "outer", "fixture.sh", "helper", "fixture.sh") {
		t.Fatalf("missing authored helper CALLS edge: %#v", relationsOfType(cached.Relations, "CALLS"))
	}
	if !hasRelationBySymbolNameAndFile(cached, "CALLS", "outer", "fixture.sh", "kube::log::status", "fixture.sh") {
		t.Fatalf("missing authored namespaced CALLS edge: %#v", relationsOfType(cached.Relations, "CALLS"))
	}
}

func TestMaskBashCommentBacktickArgumentsLeavesOtherForms(t *testing.T) {
	cases := []string{
		"echo `date`\n",
		"echo \"`# quoted text`\"\n",
		"# `# comment text`\n",
		"echo `# unterminated\n",
		"echo `# escaped \\` text`\n",
		"echo x`# attached`\n",
		"echo `# first line\nsecond`\n",
	}
	for _, source := range cases {
		if got := maskBashCommentBacktickArguments(source); got != source {
			t.Fatalf("non-target backtick form changed: source=%q got=%q", source, got)
		}
	}
}

func TestMaskBashCommentBacktickArgumentsMasksOnlyStandaloneComments(t *testing.T) {
	source := "run `# first` --flag `# second`\n"
	masked := maskBashCommentBacktickArguments(source)
	if masked == source {
		t.Fatal("standalone comment backticks were not masked")
	}
	if len(masked) != len(source) || strings.Count(masked, "\n") != strings.Count(source, "\n") {
		t.Fatalf("mask changed source coordinates: source=%q masked=%q", source, masked)
	}
	if got := maskBashCommentBacktickArguments(masked); got != masked {
		t.Fatalf("mask was not idempotent: first=%q second=%q", masked, got)
	}
}
