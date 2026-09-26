package agentsetup

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureOptions(t *testing.T, plugins ...string) Options {
	t.Helper()
	return Options{StateDir: t.TempDir(), ConfigDir: t.TempDir(), DataDir: t.TempDir(), ListPlugins: func() (string, error) {
		if len(plugins) == 0 {
			return "No plugins installed in /fixture.\nInstall one with 'entire plugin install <name|url|path>', or drop an entire-<name> binary anywhere on $PATH.\n", nil
		}
		text := "Managed plugin directory: /fixture\n\n"
		for _, plugin := range plugins {
			text += "  " + plugin + " v1.0.0 → /fixture/entire-" + plugin + "\n"
		}
		return text, nil
	}}
}
func fixtureSetup(t *testing.T, repo string, opts Options, content string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.StateDir, "repos", filepath.FromSlash(localKey(canonical)), "setup.json")
	mkdirAllForTest(t, filepath.Dir(path))
	writeFileForTest(t, path, content)
	return path
}
func TestCoordinationModes(t *testing.T) {
	for _, product := range []string{"graph", "brain"} {
		for _, previous := range []string{"", GraphGuide, BrainGuide(), CombinedGuide} {
			for _, configured := range []bool{false, true} {
				t.Run(product+"/"+strings.Split(previous, "\n")[0]+"/configured="+boolText(configured), func(t *testing.T) {
					repo := t.TempDir()
					opts := fixtureOptions(t, "graph", "brain")
					opts.ListPlugins = func() (string, error) { t.Fatal("queried global plugin inventory"); return "", nil }
					if configured {
						fixtureSetup(t, repo, opts, `{"schema_version":1}`)
					}
					if previous != "" {
						mkdirAllForTest(t, filepath.Join(repo, ".entire"))
						writeFileForTest(t, filepath.Join(repo, Path), previous)
					}
					active := map[string]bool{product: true}
					if previous == GraphGuide || previous == CombinedGuide {
						active["graph"] = true
					}
					if previous == BrainGuide() || previous == CombinedGuide {
						active["brain"] = true
					}
					guide, err := Preview(repo, product, opts)
					if err != nil || guide != renderActivation(active, ModeNormal) {
						t.Fatalf("mode: %v; got %q", err, guide)
					}
					if previous == "" {
						if _, err := os.Stat(filepath.Join(repo, Path)); !os.IsNotExist(err) {
							t.Fatal("preview wrote activation")
						}
					} else if got := readFileForTest(t, filepath.Join(repo, Path)); got != previous {
						t.Fatal("preview changed activation")
					}
				})
			}
		}
	}
}
func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
func TestCoordinationActivationOrdersAndStableMigration(t *testing.T) {
	for _, first := range []string{"graph", "brain"} {
		t.Run(first, func(t *testing.T) {
			repo := t.TempDir()
			opts := fixtureOptions(t, "brain", "graph")
			setup := fixtureSetup(t, repo, opts, `{"schema_version":1}`)
			original := "USER PREFIX\n<!-- entire-graph:begin -->\nold Graph\n<!-- entire-graph:end -->\nMIDDLE\n<!-- entire-brain:begin -->\nold Brain\n<!-- entire-brain:end -->\nUSER SUFFIX\n"
			writeFileForTest(t, filepath.Join(repo, "AGENTS.md"), original)
			writeFileForTest(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
			for _, legacy := range []string{"graph-agent.md", "brain-agent.md"} {
				mkdirAllForTest(t, filepath.Join(repo, ".entire"))
				writeFileForTest(t, filepath.Join(repo, ".entire", legacy), "# Entire repository agent guide — Graph and Brain\n")
			}
			second := "brain"
			if first == "brain" {
				second = "graph"
			}
			var before string
			for _, product := range []string{first, second, first, second} {
				render := func() (string, error) { return Preview(repo, product, opts) }
				if err := Install(repo, render, io.Discard); err != nil {
					t.Fatal(err)
				}
				displayed, err := render()
				if err != nil {
					t.Fatal(err)
				}
				if got := readFileForTest(t, filepath.Join(repo, Path)); got != displayed || got != renderActivation(map[string]bool{"graph": true, "brain": true}, ModeNormal) {
					t.Fatal("installed/displayed guides differ")
				}
				text := readFileForTest(t, filepath.Join(repo, "AGENTS.md"))
				for _, want := range []string{"USER PREFIX\n", "\nMIDDLE\n", "\nUSER SUFFIX\n"} {
					if !strings.Contains(text, want) {
						t.Fatalf("lost user text %q", want)
					}
				}
				if strings.Count(text, agentPointerBegin) != 1 || strings.Contains(text, "entire-graph:begin") || strings.Contains(text, "entire-brain:begin") {
					t.Fatal("duplicate managed instructions")
				}
				if before != "" && before != text {
					t.Fatal("repeat generation changed bytes")
				}
				before = text
				for _, legacy := range []string{"graph-agent.md", "brain-agent.md"} {
					if got := readFileForTest(t, filepath.Join(repo, ".entire", legacy)); got != legacyRedirect {
						t.Fatal("legacy instructions still active")
					}
				}
			}
			os.Remove(setup)
			if err := Install(repo, func() (string, error) { return Preview(repo, "graph", opts) }, io.Discard); err != nil {
				t.Fatal(err)
			}
			if got := readFileForTest(t, filepath.Join(repo, Path)); got != renderActivation(map[string]bool{"graph": true, "brain": true}, ModeNormal) {
				t.Fatal("runtime removal changed activation")
			}
		})
	}
}

// TestCoordinationNoRuntimeProbes keeps the normal-mode guides free of tool-presence probing:
// the guide must not tell an agent to go and find out whether Graph or Brain is installed.
//
// "FIRST action" and "SEARCH FIRST" used to be banned here alongside those probes. They are not
// probes; they are the directive that makes the product get used, and banning them was benchmark
// arm-fairness doctrine applied to shipped text. See the header comment in guide.go, and
// TestNormalGuideStaysDirective, which now asserts the opposite of what this list used to.
func TestCoordinationNoRuntimeProbes(t *testing.T) {
	for _, guide := range []string{GraphGuide, BrainGuide(), CombinedGuide} {
		for _, bad := range []string{"entire plugin list", "entire graph version", "entire brain version", "command -v", "setup.json", "if Brain is installed"} {
			if strings.Contains(guide, bad) {
				t.Errorf("guide contains %q", bad)
			}
		}
		for _, want := range []string{"useful locations", "focused tests", "untrusted", "Do not automatically install"} {
			if !strings.Contains(guide, want) {
				t.Errorf("guide missing %q", want)
			}
		}
	}
	for _, want := range []string{`entire brain brief "<task>" --json`, "equivalent task context", "not redundant", "identified gap", "entities history", "memory-informed review", "workspace", "working tree", "stored index"} {
		if !strings.Contains(CombinedGuide, want) {
			t.Errorf("combined guide missing %q", want)
		}
	}
}
func TestCoordinationPluginListFormat(t *testing.T) {
	for _, raw := range []string{"", "[]", "Managed plugin directory: /fixture\n", "Managed plugin directory: /fixture\nbrain", "Managed plugin directory: /fixture\n  brain /a\n  brain /b\n"} {
		if _, err := parsePluginList(raw); err == nil {
			t.Errorf("accepted malformed listing %q", raw)
		}
	}
	raw := "Managed plugin directory: /fixture\n\n  brain                                    → /path with spaces/brain\n  graph                v0.4.0 (pinned)     /fixture/graph\n"
	got, err := parsePluginList(raw)
	if err != nil || !got["brain"] || !got["graph"] {
		t.Fatal(got, err)
	}
}
func TestCoordinationOutsideRepository(t *testing.T) {
	t.Chdir(t.TempDir())
	root, err := Context("", "")
	if err != nil || root != "" {
		t.Fatal(root, err)
	}
	opts := Options{ListPlugins: func() (string, error) { t.Fatal("outside-repo preview queried plugins"); return "", nil }}
	for _, product := range []string{"graph", "brain"} {
		if _, err := Preview(root, product, opts); err != nil {
			t.Fatal(err)
		}
	}
}
func TestCoordinationLegacyMarkerValidation(t *testing.T) {
	for _, product := range []string{"graph", "brain"} {
		repo := t.TempDir()
		writeFileForTest(t, filepath.Join(repo, "CLAUDE.md"), "<!-- entire-"+product+":begin -->\n")
		if err := Install(repo, func() (string, error) { return CombinedGuide, nil }, io.Discard); err == nil {
			t.Fatal("malformed legacy markers accepted")
		}
		if _, err := os.Stat(filepath.Join(repo, Path)); !os.IsNotExist(err) {
			t.Fatal("partial install")
		}
	}
}

func TestCoordinationLegacyAliasesAndProtectedTargets(t *testing.T) {
	t.Run("non-markdown generated legacy landing", func(t *testing.T) {
		repo := t.TempDir()
		mkdirAllForTest(t, filepath.Join(repo, ".entire"))
		writeFileForTest(t, filepath.Join(repo, "GUIDE"), "# entire-graph — instructions for coding agents (follow directly)\nold workflow\n")
		symlinkForTest(t, "../GUIDE", filepath.Join(repo, ".entire/graph-agent.md"))
		for _, guide := range []string{CombinedGuide, GraphGuide, CombinedGuide} {
			if err := Install(repo, func() (string, error) { return guide, nil }, io.Discard); err != nil {
				t.Fatal(err)
			}
			if got := readFileForTest(t, filepath.Join(repo, "GUIDE")); got != legacyRedirect {
				t.Fatal("legacy alias not migrated")
			}
			if got := readFileForTest(t, filepath.Join(repo, Path)); got != guide {
				t.Fatal("regeneration did not update the shared guide")
			}
		}
	})
	for _, kind := range []string{"outside", "git", "instruction-alias"} {
		t.Run(kind, func(t *testing.T) {
			repo := t.TempDir()
			mkdirAllForTest(t, filepath.Join(repo, ".entire"))
			var target string
			switch kind {
			case "outside":
				target = filepath.Join(t.TempDir(), "rules.md")
			case "git":
				target = filepath.Join(repo, ".git", "config")
				mkdirAllForTest(t, filepath.Dir(target))
			case "instruction-alias":
				target = filepath.Join(repo, "AGENTS.md")
			}
			writeFileForTest(t, target, "PRESERVE")
			symlinkForTest(t, target, filepath.Join(repo, ".entire/brain-agent.md"))
			if err := Install(repo, func() (string, error) { return CombinedGuide, nil }, io.Discard); err == nil {
				t.Fatal("accepted protected legacy target")
			}
			if got := readFileForTest(t, target); got != "PRESERVE" {
				t.Fatal("modified protected target")
			}
			if _, err := os.Stat(filepath.Join(repo, Path)); !os.IsNotExist(err) {
				t.Fatal("partial shared guide")
			}
		})
	}
}

func TestInstallChangedReport(t *testing.T) {
	repo := t.TempDir()
	render := func() (string, error) { return "guide\n", nil }
	changed, err := InstallChanged(repo, render)
	if err != nil || len(changed) != 3 {
		t.Fatalf("first: %v, %v", changed, err)
	}
	changed, err = InstallChanged(repo, render)
	if err != nil || len(changed) != 0 {
		t.Fatalf("repeat: %v, %v", changed, err)
	}
}

// A directly invoked binary must generate its own instructions without a host.
func TestCoordinationWithoutEntireHost(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, product := range []string{"graph", "brain"} {
		t.Run(product, func(t *testing.T) {
			repo := t.TempDir()
			opts := Options{StateDir: t.TempDir(), ConfigDir: t.TempDir(), DataDir: t.TempDir()}
			want := renderActivation(map[string]bool{product: true}, ModeNormal)
			render := func() (string, error) { return Preview(repo, product, opts) }
			got, err := render()
			if err != nil || got != want {
				t.Fatalf("standalone preview: %v", err)
			}
			if err := Install(repo, render, io.Discard); err != nil {
				t.Fatal(err)
			}
			if got := readFileForTest(t, filepath.Join(repo, Path)); got != want {
				t.Fatal("installed guide differs from preview")
			}
			changed, err := InstallChanged(repo, render)
			if err != nil || len(changed) != 0 {
				t.Fatalf("repeat generation: %v, %v", changed, err)
			}
		})
	}
}

func TestCoordinationIgnoresRuntimeStoreRoots(t *testing.T) {
	for _, key := range []string{"ENTIRE_BRAIN_STATE_DIR", "ENTIRE_BRAIN_CONFIG_DIR", "ENTIRE_BRAIN_DATA_DIR"} {
		t.Run(key, func(t *testing.T) {
			// An invalid ambient root must not affect explicit, isolated stores.
			t.Setenv(key, "relative-ambient-store")
			repo := t.TempDir()
			opts := fixtureOptions(t, "brain")
			fixtureSetup(t, repo, opts, `{"schema_version":1}`)
			guide, err := Preview(repo, "graph", opts)
			if err != nil || guide != renderActivation(map[string]bool{"graph": true}, ModeNormal) {
				t.Fatalf("runtime stores affected activation: %v", err)
			}
		})
	}
}

type migrationFailureWriter struct{ mutate func() }

func (w *migrationFailureWriter) Write(p []byte) (int, error) {
	if w.mutate != nil {
		w.mutate()
		w.mutate = nil
	}
	return len(p), nil
}

func TestCoordinationPartialMigrationKeepsRedirectTarget(t *testing.T) {
	repo := t.TempDir()
	mkdirAllForTest(t, filepath.Join(repo, ".entire"))
	graph := filepath.Join(repo, ".entire/graph-agent.md")
	brain := filepath.Join(repo, ".entire/brain-agent.md")
	for _, path := range []string{graph, brain} {
		writeFileForTest(t, path, "old guide\n")
	}
	const instructions = "user instructions\n"
	writeFileForTest(t, filepath.Join(repo, "AGENTS.md"), instructions)
	writeFileForTest(t, filepath.Join(repo, "CLAUDE.md"), instructions)
	// Change the second legacy target after preflight, when Install reports
	// the canonical guide write. The first redirect will already be written
	// when the second write fails. Cleanup must not strand that redirect.
	out := &migrationFailureWriter{mutate: func() {
		if err := os.Remove(brain); err != nil {
			t.Fatal(err)
		}
		mkdirAllForTest(t, brain)
	}}
	render := func() (string, error) { return CombinedGuide, nil }
	if err := Install(repo, render, out); err == nil {
		t.Fatal("concurrent target replacement was not reported")
	}
	if got := readFileForTest(t, graph); got != legacyRedirect {
		t.Fatal("fixture did not reach a partially migrated state")
	}
	if got := readFileForTest(t, filepath.Join(repo, Path)); got != CombinedGuide {
		t.Fatal("partial migration stranded the written redirect")
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if got := readFileForTest(t, filepath.Join(repo, name)); got != instructions {
			t.Fatal("failed migration changed instruction entry points")
		}
	}
	if err := os.Remove(brain); err != nil {
		t.Fatal(err)
	}
	writeFileForTest(t, brain, "old guide\n")
	if err := Install(repo, render, io.Discard); err != nil {
		t.Fatalf("regeneration after repair: %v", err)
	}
	if got := readFileForTest(t, brain); got != legacyRedirect {
		t.Fatal("regeneration did not finish migration")
	}
}
