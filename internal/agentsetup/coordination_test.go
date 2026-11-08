package agentsetup

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
				t.Run(product+"/"+strings.Split(previous, "\n")[0]+"/configured="+strconv.FormatBool(configured), func(t *testing.T) {
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
func TestCoordinationNoRuntimeProbes(t *testing.T) {
	for _, guide := range []string{GraphGuide, BrainGuide(), CombinedGuide} {
		// "FIRST action" and "SEARCH FIRST" used to be banned here alongside
		// these probes. They are not probes. They are the directive that makes
		// the product get used, and banning them was benchmark arm-fairness
		// doctrine applied to shipped text -- correct for an A/B cell, fatal in
		// a user's repo, where there is no second arm. The bans below are the
		// genuine ones: a guide must not tell an agent to go probing its own
		// installation at runtime.
		for _, bad := range []string{"entire plugin list", "entire graph version", "entire brain version", "command -v", "setup.json", "if Brain is installed"} {
			if strings.Contains(guide, bad) {
				t.Errorf("guide contains %q", bad)
			}
		}
		// "sufficient locations" was REQUIRED here, which is how the permissive
		// regression was pinned in place from both directions at once. It is now
		// forbidden -- see TestGuidesCarryNoSelfAssessedExits below.
		for _, want := range []string{"focused tests", "untrusted", "Do not automatically install"} {
			if !strings.Contains(guide, want) {
				t.Errorf("guide missing %q", want)
			}
		}
	}
	// "equivalent task context" named the brief's self-assessed exit and is gone
	// with it; "redundant" survives only as part of the narrower rule that the
	// two tools must not be asked the SAME question.
	for _, want := range []string{`entire brain brief "<task>" --json`, "identified gap", "entities history", "memory-informed review", "workspace", "working tree", "stored index"} {
		if !strings.Contains(CombinedGuide, want) {
			t.Errorf("combined guide missing %q", want)
		}
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

// Reproduce the legacy local identity only to build ignored runtime fixtures.
func localKey(p string) string {
	p = filepath.Clean(p)
	sum := sha256.Sum256([]byte(p))
	base := cleanComponent(filepath.Base(p))
	if base == "" {
		base = "repo"
	}
	return fmt.Sprintf("local/%s-%x", base, sum[:6])
}

// TestNormalGuideStaysDirective is the guard the permissive regression got past.
//
// Shipped guidance has to be imperative. An agent offered a self-assessed exit
// takes it: skipping is always the locally cheaper move, and "I already have
// enough context" is always available as a reason. Measured after the 2026-09-14
// wording change: 332 graph calls against 96,987 exploration calls (0.34%), and a
// graph-first rate of 0 in every session sampled.
//
// If a benchmark needs capability-only wording, it gets its own string. It does
// not get to soften this one.
func TestGuidesCarryNoSelfAssessedExits(t *testing.T) {
	for name, guide := range map[string]string{"graph": GraphGuide, "brain": BrainGuide(), "combined": CombinedGuide} {
		// Every exit that was present in the regression, by its exact words.
		for _, exit := range []string{
			"sufficient locations",
			"Skip ceremonial queries",
			"do not require a redundant",
			"Skip this when equivalent task context",
		} {
			if strings.Contains(guide, exit) {
				t.Errorf("%s guide carries the self-assessed exit %q; shipped guidance is imperative", name, exit)
			}
		}
	}
	// ...and the obligation itself is present, not merely the absence of exits.
	//
	// The two guides state it differently ON PURPOSE. A Graph-only guide has
	// one tool, so a first-action obligation is unambiguous ("ONE Graph query"
	// is gone: ONE read as a quota in a measured baseline, so the obligation
	// covers each later question too). The COMBINED guide has two tools, and
	// stating two first-action absolutes is what made a measured session drop
	// Brain altogether -- an agent can only do one thing first, so it picks.
	// The combined guide therefore states an ORDER, and the assertion checks
	// for the order rather than the word MUST.
	if !strings.Contains(GraphGuide, "MUST be a Graph query") {
		t.Error("the Graph-only guide no longer states the query-first obligation")
	}
	if !strings.Contains(GraphGuide, "Do not skip the query") {
		t.Error("the Graph-only guide no longer closes the sufficiency exit")
	}
	for _, want := range []string{
		"Both tools run, in this order",
		`1. entire brain brief`,
		`2. entire graph query`,
		"do not treat having done one as having done the other",
	} {
		if !strings.Contains(CombinedGuide, want) {
			t.Errorf("the combined guide no longer states the ordered sequence; missing %q", want)
		}
	}
	// And it must NOT reintroduce a second first-action absolute beside the
	// sequence: that is the collision this replaced.
	if strings.Contains(CombinedGuide, "FIRST action") {
		t.Error("the combined guide carries a second first-action absolute beside the ordered " +
			"sequence; two absolutes let the agent pick one and drop the other")
	}
}

// TestGuidesNameOnlyCommandsGraphExposes covers issue #323's successor: a guide
// that names a command the installed graph does not expose teaches the agent the
// tool is broken, which is worse than saying nothing. The #323 compromise named
// `search` because `query` was absent from the released v0.4.0 dispatch; every
// graph since 0.4.1 exposes `query` (with `search` as the legacy alias), the
// graph repository's own guide moved to `query`, and the mirrored guides must
// name exactly one spelling or the two binaries drift apart again.
func TestGuidesNameOnlyCommandsGraphExposes(t *testing.T) {
	for name, guide := range map[string]string{
		"graph": GraphGuide, "combined": CombinedGuide,
		"strict-graph": strictGraphWorkflow, "strict-combined": strictCombinedWorkflow,
	} {
		if !strings.Contains(guide, "entire graph query") {
			t.Errorf("%s guide lost the `entire graph query` command", name)
		}
		if strings.Contains(guide, "entire graph search") {
			t.Errorf("%s guide names the legacy `entire graph search` spelling; guides name one verb", name)
		}
	}
}

// A Brain-only activation produces a guide with NO Graph instruction, because
// activation is recorded in the guide rather than probed from the installed
// plugins (preview.go: "activation does not depend on plugin inventory").
//
// Measured in a clean clone: Brain's install alone yields
// `enabled:["brain"]` and zero occurrences of the Graph directive. So the
// directive restored for the combined guide is unreachable for anyone who
// follows Brain's README without also activating Graph — which is why the
// README now names both commands and their order.
func TestBrainOnlyActivationHasNoGraphInstruction(t *testing.T) {
	brainOnly := BrainGuide()
	for _, graphism := range []string{"MUST be ONE Graph search", "entire graph search", "entire graph query"} {
		if strings.Contains(brainOnly, graphism) {
			t.Errorf("the Brain-only guide names %q; it must not instruct an agent to use a product that was never activated", graphism)
		}
	}
	// ...and the combined guide must carry the Graph step, or it reaches nobody.
	if !strings.Contains(CombinedGuide, "entire graph query") {
		t.Error("the combined guide lost the Graph step; only the combined guide can carry it")
	}
}

// The README must name both activation commands and their order. Brain's
// documented path produced a guide with no Graph instruction, so an agent could
// not have followed one.
func TestREADMEDocumentsBothActivationsAndTheirOrder(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	readme := string(data)
	if len(readme) == 0 {
		t.Fatal("README is empty; this assertion would be vacuous")
	}
	for _, want := range []string{"entire graph setup", "entire brain setup"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README does not tell the user to run %q", want)
		}
	}
	if !strings.Contains(readme, "either order") {
		t.Error("README does not state that the two activations compose in either order; " +
			"each setup enables its own product and both render the same mirrored guide text")
	}
}
