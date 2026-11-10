package agentsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActivationOrders(t *testing.T) {
	for _, first := range []string{"graph", "brain"} {
		t.Run(first, func(t *testing.T) {
			repo := t.TempDir()
			second := "brain"
			if first == "brain" {
				second = "graph"
			}
			for i, product := range []string{first, second, first, second} {
				render := func() (string, error) { return Preview(repo, product, Options{}) }
				changed, err := InstallChanged(repo, render)
				if err != nil {
					t.Fatal(err)
				}
				want := map[string]bool{first: true}
				if i > 0 {
					want[second] = true
				}
				if got := readFileForTest(t, filepath.Join(repo, Path)); got != renderActivation(want, ModeNormal) {
					t.Fatalf("step %d incorrect guide", i)
				}
				if i > 1 && len(changed) != 0 {
					t.Fatalf("repeat changed %v", changed)
				}
			}
		})
	}
}

func TestActivationMetadataAuthoritative(t *testing.T) {
	repo := t.TempDir()
	mkdirAllForTest(t, filepath.Join(repo, ".entire"))
	// A leftover legacy marker must not reactivate a product removed in metadata.
	writeFileForTest(t, filepath.Join(repo, Path), renderActivation(map[string]bool{"graph": true}, ModeNormal))
	writeFileForTest(t, filepath.Join(repo, "AGENTS.md"), "<!-- entire-brain:begin -->\nold\n<!-- entire-brain:end -->\n")
	got, err := Preview(repo, "graph", Options{})
	if err != nil || got != renderActivation(map[string]bool{"graph": true}, ModeNormal) {
		t.Fatal("legacy overrode metadata", err)
	}
}

func TestActivationRejectsInvalidStateBeforeWrites(t *testing.T) {
	for _, value := range []string{
		"unknown guide",
		activationPrefix + `{"schema_version":2,"enabled":["graph"]}` + activationSuffix,
		activationPrefix + `{"schema_version":1,"enabled":["unknown"]}` + activationSuffix,
		activationPrefix + `{"schema_version":1,"enabled":["graph","graph"]}` + activationSuffix,
		activationPrefix + `{"schema_version":1,"enabled":[]}` + activationSuffix,
		activationPrefix + `{"schema_version":1,"enabled":["graph"],"extra":true}` + activationSuffix,
		activationPrefix + "{" + activationSuffix,
		activationPrefix + `{"schema_version":1,"enabled":["graph"]}`,
		renderActivation(map[string]bool{"graph": true}, ModeNormal) + renderActivation(map[string]bool{"brain": true}, ModeNormal),
	} {
		t.Run(value, func(t *testing.T) {
			repo := t.TempDir()
			mkdirAllForTest(t, filepath.Join(repo, ".entire"))
			writeFileForTest(t, filepath.Join(repo, Path), value)
			_, err := InstallChanged(repo, func() (string, error) { return Preview(repo, "graph", Options{}) })
			if err == nil {
				t.Fatal("invalid state accepted")
			}
			if got := readFileForTest(t, filepath.Join(repo, Path)); got != value {
				t.Fatal("guide overwritten")
			}
			if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); !os.IsNotExist(err) {
				t.Fatal("partial write")
			}
		})
	}
}

func TestActivationLegacyMigration(t *testing.T) {
	for _, name := range []string{Path, ".entire/brain-agent.md", "AGENTS.md", "CLAUDE.md"} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			mkdirAllForTest(t, filepath.Dir(filepath.Join(repo, name)))
			content := BrainGuide()
			if strings.HasSuffix(name, "AGENTS.md") || name == "CLAUDE.md" {
				content = "user\n<!-- entire-brain:begin -->\nold\n<!-- entire-brain:end -->\n"
			}
			writeFileForTest(t, filepath.Join(repo, name), content)
			got, err := Preview(repo, "graph", Options{})
			if err != nil || got != renderActivation(map[string]bool{"graph": true, "brain": true}, ModeNormal) {
				t.Fatal("lost legacy activation", err)
			}
		})
	}
}

func TestActivationContainedAlias(t *testing.T) {
	repo := t.TempDir()
	mkdirAllForTest(t, filepath.Join(repo, ".entire"))
	writeFileForTest(t, filepath.Join(repo, "guide.md"), BrainGuide())
	symlinkForTest(t, "../guide.md", filepath.Join(repo, Path))
	got, err := Preview(repo, "graph", Options{})
	if err != nil || got != renderActivation(map[string]bool{"graph": true, "brain": true}, ModeNormal) {
		t.Fatal(err)
	}
}

func TestActivationDoesNotReadOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	mkdirAllForTest(t, filepath.Join(repo, ".entire"))
	outside := filepath.Join(t.TempDir(), "guide.md")
	writeFileForTest(t, outside, BrainGuide())
	symlinkForTest(t, outside, filepath.Join(repo, Path))
	if _, err := Preview(repo, "graph", Options{}); err == nil {
		t.Fatal("read outside repo")
	}
}
