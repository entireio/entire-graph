package sem

import (
	"strings"
	"testing"
)

func TestSearchClosedSetVariantOnLine(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		line string
		want string
	}{
		{name: "java constant with an argument", line: `    PLUS("+"),`, want: "PLUS"},
		{name: "rust unit variant", line: `    Alpha,`, want: "Alpha"},
		{name: "rust tuple variant", line: `    Beta(u32),`, want: "Beta"},
		{name: "rust struct variant", line: `    Gamma { x: u8 },`, want: "Gamma"},
		{name: "typescript member with a value", line: `  Delta = 1,`, want: "Delta"},
		{name: "php and swift case keyword", line: `    case Draft;`, want: "Draft"},
		{name: "last member before the semicolon", line: `    QUOT("quotient");`, want: "QUOT"},
		{name: "a field is not a variant", line: `    private final String fn;`, want: ""},
		{name: "an untyped field is not a variant", line: `    String fn;`, want: ""},
		{name: "a method is not a variant", line: `    public String compute(Ops o) {`, want: ""},
		{name: "the constructor is not a variant", line: `    Ops(String fn) {`, want: ""},
		{name: "a comment is not a variant", line: `    // PLUS is the default`, want: ""},
		{name: "a switch inside the body is not a variant", line: `    switch (o) {`, want: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, ok := searchClosedSetVariantOnLine(testCase.line, "Ops")
			if testCase.want == "" {
				if ok {
					t.Fatalf("expected no variant, got %q", got)
				}
				return
			}
			if !ok || got != testCase.want {
				t.Fatalf("variant = %q/%v, want %q", got, ok, testCase.want)
			}
		})
	}
}

func TestSearchClosedSetUnionMembers(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		signature  string
		want       []string
		wantQuoted bool
		wantOK     bool
	}{
		{
			name:       "string literal union",
			signature:  `type Mode = "fast" | "slow" | "auto"`,
			want:       []string{"fast", "slow", "auto"},
			wantQuoted: true, wantOK: true,
		},
		{
			name:      "identifier union",
			signature: `type Node = Element | Comment | Fragment`,
			want:      []string{"Element", "Comment", "Fragment"},
			wantOK:    true,
		},
		{
			// Two variants is a closed set: growing it to three is the add-a-variant task.
			name: "a two-member union is still a union", signature: `type Flag = "on" | "off"`,
			want: []string{"on", "off"}, wantQuoted: true, wantOK: true,
		},
		{name: "no alternatives at all", signature: `type Mode = string`},
		{name: "an empty alternative is malformed", signature: `type Mode = "fast" | `},
		{name: "a mixed union has no reliable arm spelling", signature: `type T = "a" | B | "c"`},
		{name: "a generic alternative is not a variant name", signature: `type T = A | B<C> | D`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, quoted, ok := searchClosedSetUnionMembers(testCase.signature)
			if ok != testCase.wantOK {
				t.Fatalf("ok = %v, want %v (members %v)", ok, testCase.wantOK, got)
			}
			if !ok {
				return
			}
			if strings.Join(got, "|") != strings.Join(testCase.want, "|") || quoted != testCase.wantQuoted {
				t.Fatalf("members = %v quoted=%v, want %v quoted=%v", got, quoted, testCase.want, testCase.wantQuoted)
			}
		})
	}
}

func TestSearchClosedSetCheckedKnowsWhichLanguagesTheCompilerGuards(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		set       searchClosedVariantSet
		construct string
		header    string
		wildcard  bool
		never     bool
		want      string
	}{
		{
			name: "rust match without a wildcard", set: searchClosedVariantSet{language: "Rust"},
			construct: "match", header: "    match k {", want: searchClosedSetCheckedCompiler,
		},
		{
			name: "rust match with a wildcard is not checked", set: searchClosedVariantSet{language: "Rust"},
			construct: "match", header: "    match k {", wildcard: true, want: searchClosedSetCheckedRuntime,
		},
		{
			name: "kotlin when as an expression", set: searchClosedVariantSet{language: "Kotlin"},
			construct: "when", header: "    val name = when (k) {", want: searchClosedSetCheckedCompiler,
		},
		{
			name: "kotlin when as a statement", set: searchClosedVariantSet{language: "Kotlin"},
			construct: "when", header: "    when (k) {", want: searchClosedSetCheckedRuntime,
		},
		{
			name:      "a typescript never assertion is a compile-time check",
			set:       searchClosedVariantSet{language: "TypeScript"},
			construct: "switch", header: "  switch (m) {", never: true, want: searchClosedSetCheckedCompiler,
		},
		{
			name: "a java switch is not checked", set: searchClosedVariantSet{language: "Java"},
			construct: "switch", header: "    switch (o) {", want: searchClosedSetCheckedRuntime,
		},
		{
			name: "a go switch is not checked", set: searchClosedVariantSet{language: "Go"},
			construct: "switch", header: "\tswitch l {", want: searchClosedSetCheckedRuntime,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := searchClosedSetChecked(testCase.set, testCase.construct, testCase.header,
				testCase.wildcard, testCase.never)
			if got != testCase.want {
				t.Fatalf("checked = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestSearchClosedSetSiteRiskOnlyFlagsWhatTheCompilerMisses(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		site     SearchClosedSetSite
		wantWarn bool
	}{
		{
			name:     "exhaustive runtime switch with a throwing default",
			site:     SearchClosedSetSite{Exhaustive: true, Default: searchClosedSetDefaultThrows, Checked: searchClosedSetCheckedRuntime},
			wantWarn: true,
		},
		{
			name:     "exhaustive runtime switch with no default",
			site:     SearchClosedSetSite{Exhaustive: true, Default: searchClosedSetDefaultAbsent, Checked: searchClosedSetCheckedRuntime},
			wantWarn: true,
		},
		{
			name:     "a switch already missing a variant is not safer for it",
			site:     SearchClosedSetSite{Default: searchClosedSetDefaultThrows, Checked: searchClosedSetCheckedRuntime},
			wantWarn: true,
		},
		{
			name: "a default that quietly returns something may be intentional",
			site: SearchClosedSetSite{Exhaustive: true, Default: searchClosedSetDefaultSilent, Checked: searchClosedSetCheckedRuntime},
		},
		{
			name: "the compiler is already the warning",
			site: SearchClosedSetSite{Exhaustive: true, Default: searchClosedSetDefaultAbsent, Checked: searchClosedSetCheckedCompiler},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			warning := searchClosedSetWarning([]SearchClosedSetSite{testCase.site})
			if (warning != "") != testCase.wantWarn {
				t.Fatalf("warning = %q, want warn=%v", warning, testCase.wantWarn)
			}
		})
	}
}

func TestSearchClosedSetRetainsDeclarationAndPreBudgetSiteProvenance(t *testing.T) {
	t.Parallel()

	set := searchClosedVariantSet{
		name: "Ops", kind: "enum", members: []string{"PLUS", "MINUS", "QUOT"},
		declaredIn: "types/Ops.java",
	}
	sites := []SearchClosedSetSite{
		{FilePath: "switches/primary.java", Line: 10, Arms: 3, Exhaustive: true,
			Default: searchClosedSetDefaultThrows, Checked: searchClosedSetCheckedRuntime},
		{FilePath: "switches/a_very_long_secondary_file_name_that_forces_the_budget_to_shrink.java",
			Line: 20, Arms: 3, Exhaustive: true,
			Default: searchClosedSetDefaultThrows, Checked: searchClosedSetCheckedRuntime},
	}
	oneSite := &SearchClosedSet{
		Type: set.name, Kind: set.kind, Variants: len(set.members), Sites: sites[:1],
		Warning: searchClosedSetWarning(sites),
	}
	block := buildSearchClosedSetBlock(set, sites, searchClosedSetCost(oneSite))
	if block == nil {
		t.Fatal("closed-set block did not fit after shrinking")
	}
	if len(block.Sites) != 1 {
		t.Fatalf("sites = %d, want one after budget truncation", len(block.Sites))
	}
	want := []string{
		"switches/a_very_long_secondary_file_name_that_forces_the_budget_to_shrink.java",
		"switches/primary.java",
		"types/Ops.java",
	}
	if strings.Join(block.provenancePaths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("closed-set provenance = %v, want %v", block.provenancePaths, want)
	}
}

func TestSearchClosedSetScanBlockNeedsTwoArmsOfTheSameSet(t *testing.T) {
	t.Parallel()
	set := searchClosedVariantSet{name: "Ops", kind: "enum", language: "Java",
		members: []string{"PLUS", "MINUS", "QUOT"}}
	for _, testCase := range []struct {
		name        string
		lines       []string
		wantFound   bool
		wantArms    int
		wantExh     bool
		wantDefault string
	}{
		{
			name: "exhaustive with a throwing default",
			lines: []string{
				"    switch (op) {",
				"      case PLUS: return \"p\";",
				"      case MINUS: return \"m\";",
				"      case QUOT: return \"q\";",
				"      default: throw new IllegalStateException(\"unknown\");",
				"    }",
			},
			wantFound: true, wantArms: 3, wantExh: true, wantDefault: searchClosedSetDefaultThrows,
		},
		{
			name: "partial with no default",
			lines: []string{
				"    switch (op) {",
				"      case PLUS: return \"p\";",
				"      case MINUS: return \"m\";",
				"    }",
			},
			wantFound: true, wantArms: 2, wantDefault: searchClosedSetDefaultAbsent,
		},
		{
			name: "a default that returns something is not fatal",
			lines: []string{
				"    switch (op) {",
				"      case PLUS: return \"p\";",
				"      case MINUS: return \"m\";",
				"      case QUOT: return \"q\";",
				"      default: return \"unknown\";",
				"    }",
			},
			wantFound: true, wantArms: 3, wantExh: true, wantDefault: searchClosedSetDefaultSilent,
		},
		{
			// One shared name is a coincidence: this switch dispatches on something else and merely
			// mentions a variant inside an arm body. Without two arms there is no evidence.
			name: "one mention is not a switch over the set",
			lines: []string{
				"    switch (verb) {",
				"      case \"get\": return PLUS;",
				"      default: throw new IllegalStateException(\"unknown verb\");",
				"    }",
			},
		},
		{
			// Indentation bounds the block, so the arms of a LATER switch cannot be counted here.
			name: "a following switch is a different block",
			lines: []string{
				"    switch (verb) {",
				"      case \"get\": return PLUS;",
				"    }",
				"    switch (op) {",
				"      case MINUS: return \"m\";",
				"      case QUOT: return \"q\";",
				"    }",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			site, found := searchClosedSetScanBlock(set, "switch", testCase.lines, 0)
			if found != testCase.wantFound {
				t.Fatalf("found = %v, want %v (%+v)", found, testCase.wantFound, site)
			}
			if !found {
				return
			}
			if site.Arms != testCase.wantArms || site.Exhaustive != testCase.wantExh ||
				site.Default != testCase.wantDefault {
				t.Fatalf("site = %d arms / exhaustive %v / default %s, want %d / %v / %s",
					site.Arms, site.Exhaustive, site.Default,
					testCase.wantArms, testCase.wantExh, testCase.wantDefault)
			}
		})
	}
}

func TestSearchClosedSetEndToEndAcrossLanguages(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name         string
		path         string
		source       string
		query        string
		wantType     string
		wantKind     string
		wantVariants int
		wantChecked  string
		wantDefault  string
		wantWarn     bool
	}{
		{
			name: "java enum with a throwing default",
			path: "Ops.java",
			source: `package demo;

public enum Ops
{
  PLUS("plus"),
  MINUS("minus"),
  QUOT("quotient");

  private final String fn;

  public String cacheKey(Ops op)
  {
    switch (op) {
      case PLUS: return "p";
      case MINUS: return "m";
      case QUOT: return "q";
      default: throw new IllegalStateException("unknown op");
    }
  }
}
`,
			query:    "cacheKey for the arithmetic Ops enum is wrong",
			wantType: "Ops", wantKind: "enum", wantVariants: 3,
			wantChecked: searchClosedSetCheckedRuntime, wantDefault: searchClosedSetDefaultThrows,
			wantWarn: true,
		},
		{
			name: "csharp enum with a throwing default",
			path: "Level.cs",
			source: `namespace Demo;

public enum Level
{
    Low,
    Mid,
    High,
}

public static class Levels
{
    public static string Name(Level level)
    {
        switch (level)
        {
            case Level.Low: return "low";
            case Level.Mid: return "mid";
            case Level.High: return "high";
            default: throw new System.ArgumentException("unknown level");
        }
    }
}
`,
			query:    "Level enum Name mapping is missing a level",
			wantType: "Level", wantKind: "enum", wantVariants: 3,
			wantChecked: searchClosedSetCheckedRuntime, wantDefault: searchClosedSetDefaultThrows,
			wantWarn: true,
		},
		{
			name: "php enum with a throwing default",
			path: "Status.php",
			source: `<?php

enum Status
{
    case Draft;
    case Live;
    case Archived;
}

class Renderer
{
    public function label(Status $status): string
    {
        switch ($status) {
            case Status::Draft: return 'draft';
            case Status::Live: return 'live';
            case Status::Archived: return 'archived';
            default: throw new \LogicException('unknown status');
        }
    }
}
`,
			query:    "Status enum label rendering is wrong",
			wantType: "Status", wantKind: "enum", wantVariants: 3,
			wantChecked: searchClosedSetCheckedRuntime, wantDefault: searchClosedSetDefaultThrows,
			wantWarn: true,
		},
		{
			name: "typescript string union with a throwing default",
			path: "mode.ts",
			source: `type Mode = "fast" | "slow" | "auto";

export function pick(m: Mode): number {
  switch (m) {
    case "fast":
      return 1;
    case "slow":
      return 2;
    case "auto":
      return 3;
    default:
      throw new Error("bad mode");
  }
}
`,
			query:    "pick returns the wrong number for a Mode",
			wantType: "Mode", wantKind: "union", wantVariants: 3,
			wantChecked: searchClosedSetCheckedRuntime, wantDefault: searchClosedSetDefaultThrows,
			wantWarn: true,
		},
		{
			name: "go typed const group with no default arm",
			path: "level.go",
			source: `package demo

type Level int

const (
	Low Level = iota
	Mid
	High
)

func name(l Level) string {
	switch l {
	case Low:
		return "low"
	case Mid:
		return "mid"
	case High:
		return "high"
	}
	panic("unknown level")
}
`,
			query:    "name for a Level const is wrong",
			wantType: "Level", wantKind: "const-group", wantVariants: 3,
			wantChecked: searchClosedSetCheckedRuntime, wantDefault: searchClosedSetDefaultAbsent,
			wantWarn: true,
		},
		{
			name: "a rust match with a wildcard is a runtime risk",
			path: "kind.rs",
			source: `pub enum Kind {
    Alpha,
    Beta,
    Gamma,
}

pub fn describe(k: Kind) -> &'static str {
    match k {
        Kind::Alpha => "a",
        Kind::Beta => "b",
        Kind::Gamma => "g",
        _ => unreachable!("unknown kind"),
    }
}
`,
			query:    "describe returns the wrong text for a Kind",
			wantType: "Kind", wantKind: "enum", wantVariants: 3,
			wantChecked: searchClosedSetCheckedRuntime, wantDefault: searchClosedSetDefaultThrows,
			wantWarn: true,
		},
		{
			// The whole point of reporting `checked` per site: Rust's own exhaustiveness check makes
			// the block worthless here, so it must not exist.
			name: "a rust match without a wildcard is the compiler's problem",
			path: "kind.rs",
			source: `pub enum Kind {
    Alpha,
    Beta,
    Gamma,
}

pub fn describe(k: Kind) -> &'static str {
    match k {
        Kind::Alpha => "a",
        Kind::Beta => "b",
        Kind::Gamma => "g",
    }
}
`,
			query: "describe returns the wrong text for a Kind",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeFile(t, repo, testCase.path, testCase.source)
			response, err := SearchRepository(t.Context(), repo, "test-version", testCase.query,
				SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			block := response.ClosedSet
			if !testCase.wantWarn {
				if block != nil {
					t.Fatalf("expected no warning, got: %s", RenderSearchClosedSet(block))
				}
				return
			}
			if block == nil {
				t.Fatalf("no closed-set warning; results: %d", len(response.Results))
			}
			if block.Type != testCase.wantType || block.Kind != testCase.wantKind {
				t.Fatalf("set = %s/%s, want %s/%s", block.Type, block.Kind, testCase.wantType, testCase.wantKind)
			}
			if block.Variants != testCase.wantVariants {
				t.Fatalf("variants = %d, want %d (%s)", block.Variants, testCase.wantVariants,
					RenderSearchClosedSet(block))
			}
			site := block.Sites[0]
			if site.Checked != testCase.wantChecked || site.Default != testCase.wantDefault {
				t.Fatalf("site = checked %s / default %s, want %s / %s",
					site.Checked, site.Default, testCase.wantChecked, testCase.wantDefault)
			}
			if !site.Exhaustive {
				t.Fatalf("site should handle every variant: %s", RenderSearchClosedSet(block))
			}
			if !strings.Contains(block.Warning, "runtime") && !strings.Contains(block.Warning, "falls out") {
				t.Fatalf("warning does not state the consequence: %q", block.Warning)
			}
			if response.Stats.ClosedSetBytes != searchClosedSetCost(block) {
				t.Fatalf("accounted %d bytes, cost %d", response.Stats.ClosedSetBytes, searchClosedSetCost(block))
			}
			if searchClosedSetCost(block) > searchClosedSetMaxBytes {
				t.Fatalf("cost %d exceeds the cap %d", searchClosedSetCost(block), searchClosedSetMaxBytes)
			}
			if err := response.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSearchClosedSetIgnoresASwitchOverSomethingElse(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// One shared name is a coincidence: this switch dispatches on unrelated string constants and
	// happens to mention one variant name inside an arm body.
	writeFile(t, repo, "Ops.java", `package demo;

public enum Ops
{
  PLUS("plus"),
  MINUS("minus"),
  QUOT("quotient");
}
`)
	writeFile(t, repo, "Router.java", `package demo;

public class Router
{
  public String route(String verb)
  {
    switch (verb) {
      case "get": return "PLUS";
      default: throw new IllegalStateException("unknown verb");
    }
  }
}
`)
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"route the verb through the Ops registry",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if response.ClosedSet != nil {
		t.Fatalf("a switch naming one variant is not a switch over the set: %s",
			RenderSearchClosedSet(response.ClosedSet))
	}
}

func TestRenderSearchClosedSetLeadsWithTheConsequence(t *testing.T) {
	t.Parallel()
	rendered := string(RenderSearchClosedSet(&SearchClosedSet{
		Type: "Ops", Kind: "enum", Variants: 3,
		Warning: "adding a variant without adding an arm compiles and then throws at runtime",
		Sites: []SearchClosedSetSite{
			{FilePath: "Ops.java", Line: 12, Symbol: "Ops.cacheKey", Arms: 3, Exhaustive: true,
				Default: searchClosedSetDefaultThrows, Checked: searchClosedSetCheckedRuntime},
		},
	}))
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "CLOSED SET Ops (enum, 3 variants):") {
		t.Fatalf("first line = %q", lines[0])
	}
	if !strings.Contains(lines[0], "throws at runtime") {
		t.Fatalf("the consequence must be on the first line: %q", lines[0])
	}
	for _, want := range []string{"Ops.java:12", "Ops.cacheKey", "exhaustive", "default throws", "checked at runtime"} {
		if !strings.Contains(lines[1], want) {
			t.Fatalf("site line missing %q: %q", want, lines[1])
		}
	}
}
