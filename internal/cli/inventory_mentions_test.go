package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestInventoryMentionDisclosure(t *testing.T) {
	snapshot := sem.ProviderSnapshot{
		Header: sem.SnapshotHeader{Commit: "committed", LanguageTiers: map[string]string{
			"Svelte": "inventory-only", "Markdown": "inventory-only", "TypeScript": "semantic",
		}},
		Files: []sem.FileRecord{
			{Path: "src/Card.svelte", Language: "Svelte"},
			{Path: "src/Other.svelte", Language: "Svelte"},
			{Path: "README.md", Language: "Markdown"},
			{Path: "src/format.ts", Language: "TypeScript"},
			{Path: "src/Badge.vue", Language: "Vue"},
			{Path: "src/Plain.vue", Language: "Vue"},
		},
	}
	source := map[string][]string{
		"src/Card.svelte":  {"<script>", "  import { formatDue } from './format'", "</script>"},
		"src/Other.svelte": {"<p>{formatDueDate(task)}</p>"}, // a longer name is not a mention
		"README.md":        {"Call formatDue to render a date."},
		"src/format.ts":    {"export function formatDue() {}"},
		// Calls in a template are not modelled: this one counts.
		"src/Badge.vue": {"<template>", "  <span>{{ formatDue(due) }}</span>", "</template>", "", "<script setup>", "import { formatDue } from './format'", "</script>"},
		// A script mention is modelled already: this one does not.
		"src/Plain.vue": {"<template>", "  <span>{{ due }}</span>", "</template>", "", "<script setup>", "import { formatDue } from './format'", "formatDue(due)", "</script>"},
	}
	for i, file := range snapshot.Files {
		snapshot.Files[i].Bytes = len(strings.Join(source[file.Path], "\n"))
	}
	read := func(path string) ([]string, bool) {
		lines, ok := source[path]
		return lines, ok
	}

	warning, ok := inventoryMentionDisclosure(snapshot, neighborEndpoint{Name: "formatDue", Language: "TypeScript"}, read)
	if !ok {
		t.Fatal("expected a disclosure for a TypeScript function mentioned in a Svelte file")
	}
	if warning.Code != inventoryMentionDisclosureCode || warning.Detail != "files: src/Badge.vue, src/Card.svelte" {
		t.Fatalf("warning = %+v", warning)
	}
	if !strings.Contains(warning.EffectOnCompleteness, "2 files (Svelte, Vue template) mention formatDue,") {
		t.Fatalf("effect = %q", warning.EffectOnCompleteness)
	}
	single, ok := inventoryMentionDisclosure(snapshot, neighborEndpoint{Name: "formatDueDate", Language: "TypeScript"}, read)
	if !ok || !strings.Contains(single.EffectOnCompleteness, "1 file (Svelte) mentions formatDueDate,") {
		t.Fatalf("single-file wording = %q", single.EffectOnCompleteness)
	}
	if _, ok := inventoryMentionDisclosure(snapshot, neighborEndpoint{Name: "formatDue", Language: "Go"}, read); ok {
		t.Fatal("a Go function cannot be called from a Svelte file")
	}
	warnings := withInventoryMentionDisclosure(nil, snapshot, neighborEndpoint{Name: "formatDue", Language: "TypeScript"}, read)
	if len(snapshotWarnings(warnings)) != 0 {
		t.Fatal("the query-scoped disclosure must not count as a snapshot diagnostic")
	}

	// A byte budget that runs out after the first mention stops the scan and
	// says so; a scan that finds nothing before stopping reports nothing.
	defer func(limit int) { inventoryMentionMaxBytes = limit }(inventoryMentionMaxBytes)
	inventoryMentionMaxBytes = len(strings.Join(source["src/Badge.vue"], "\n"))
	warning, ok = inventoryMentionDisclosure(snapshot, neighborEndpoint{Name: "formatDue", Language: "TypeScript"}, read)
	if !ok || !strings.HasPrefix(warning.Detail, "files: src/Badge.vue;") || !strings.Contains(warning.Detail, "scan stopped after 1 file") {
		t.Fatalf("truncated warning = %+v (ok=%v)", warning, ok)
	}
}

// A TypeScript helper called only from a Svelte component: impact and neighbors
// report no callers, and must say which unparsed file may hold them.
func TestInventoryMentionDisclosureReachesImpactAndNeighbors(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Inventory Mention Test")
	git(t, repo, "config", "user.email", "inventory-mention@example.invalid")
	write(t, repo, "src/format.ts", "export function formatDue(date: string): string {\n  return date\n}\n")
	write(t, repo, "src/Card.svelte", "<script>\n  import { formatDue } from './format'\n  export let due\n</script>\n\n<p>{formatDue(due)}</p>\n")
	write(t, repo, "src/Badge.vue", "<template><span>{{ formatDue(due) }}</span></template><script setup>\nimport { formatDue } from './format'\ndefineProps(['due'])\n</script>\n")
	git(t, repo, "add", "-A")
	git(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "base")

	cacheDir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		err := Run(t.Context(), Options{
			Version: "test-version",
			Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
			Stdout:  &stdout,
			Stderr:  &stderr,
		}, args)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr.String())
		}
		return stdout.String()
	}

	for _, testCase := range []struct {
		name string
		args []string
		want string
	}{
		{"impact text", []string{"impact", "--repo", repo, "--symbol", "formatDue"}, "Unmodelled mentions (calls there are not in the graph): files: src/Badge.vue, src/Card.svelte"},
		{"neighbors json", []string{"neighbors", "--repo", repo, "--symbol", "formatDue", "--direction", "in", "--format", "json"}, inventoryMentionDisclosureCode},
		{"neighbors agent", []string{"neighbors", "--repo", repo, "--symbol", "formatDue", "--direction", "in", "--format", "agent"}, "Unmodelled mentions"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if out := run(testCase.args...); !strings.Contains(out, testCase.want) {
				t.Fatalf("output lacks %q:\n%s", testCase.want, out)
			}
		})
	}
}

func TestInventoryMentionReadBudgetIncludesScriptOnlyFiles(t *testing.T) {
	source := "<script setup>const x = 1</script>"
	snapshot := sem.ProviderSnapshot{Header: sem.SnapshotHeader{Commit: "committed"}, Files: []sem.FileRecord{
		{Path: "a.vue", Language: "Vue", Bytes: len(source)},
		{Path: "b.vue", Language: "Vue", Bytes: len(source)},
	}}
	saved := inventoryMentionMaxBytes
	inventoryMentionMaxBytes = len(source) + 1
	t.Cleanup(func() { inventoryMentionMaxBytes = saved })
	reads := 0
	_, _ = inventoryMentionDisclosure(snapshot, neighborEndpoint{Name: "x", Language: "Vue"}, func(path string) ([]string, bool) {
		reads++
		return []string{source}, true
	})
	if reads != 1 {
		t.Fatalf("read %d files when only one fits the byte budget", reads)
	}
	// The same snapshot metadata cannot bound a later worktree read.
	snapshot.Header.Commit = ""
	_, _ = inventoryMentionDisclosure(snapshot, neighborEndpoint{Name: "x", Language: "Vue"}, func(string) ([]string, bool) {
		t.Fatal("started a mutable read without room for the per-file ceiling")
		return nil, false
	})

}

func TestInventoryMentionIgnoresScriptSharingTemplateLine(t *testing.T) {
	content := "<template><template v-if='ok'>{{ show }}</template></template><script setup>const hide = 1</script>"
	snapshot := sem.ProviderSnapshot{Files: []sem.FileRecord{{Path: "app.vue", Language: "Vue", Bytes: len(content)}}}
	for _, name := range []string{"show", "hide"} {
		_, found := inventoryMentionDisclosure(snapshot, neighborEndpoint{Name: name, Language: "Vue"}, func(string) ([]string, bool) {
			return []string{content}, true
		})
		if found != (name == "show") {
			t.Errorf("template mention of %s = %v", name, found)
		}
	}
}
