package sem

import (
	"strings"
	"testing"
)

// TestSearchVerifyGuardForFindsTheEnclosingGuard pins the scan: innermost open guard, balanced #endif,
// and no claim about shapes it cannot satisfy.
func TestSearchVerifyGuardForFindsTheEnclosingGuard(t *testing.T) {
	t.Parallel()
	content := strings.Join([]string{
		"#ifdef FMT_A",                       // 1
		"TEST(suite, a) {}",                  // 2
		"#endif",                             // 3
		"",                                   // 4
		"#ifdef FMT_RANGES_TEST_ENABLE_JOIN", // 5
		"TEST(suite, join_tuple) {}",         // 6
		"#endif",                             // 7
		"TEST(suite, plain) {}",              // 8
		"#if defined(FMT_C) && FMT_D",        // 9
		"TEST(suite, c) {}",                  // 10
	}, "\n")
	for _, testCase := range []struct {
		line                int
		wantDefine, wantRaw string
	}{
		{line: 2, wantDefine: "FMT_A", wantRaw: "#ifdef FMT_A"},
		{line: 6, wantDefine: "FMT_RANGES_TEST_ENABLE_JOIN", wantRaw: "#ifdef FMT_RANGES_TEST_ENABLE_JOIN"},
		{line: 8}, // both guards closed
		{line: 10, wantDefine: "FMT_C", wantRaw: "#if defined(FMT_C) && FMT_D"},
	} {
		guard := searchVerifyGuardFor(content, testCase.line)
		if guard.define != testCase.wantDefine || guard.raw != testCase.wantRaw {
			t.Fatalf("line %d: guard = %+v, want define %q raw %q",
				testCase.line, guard, testCase.wantDefine, testCase.wantRaw)
		}
	}
	// Rust attributes bind to the following item, so they only count when adjacent.
	rust := "#[cfg(feature = \"unicode\")]\n#[test]\nfn parses_unicode() {}\n\nfn later() {}\n"
	if guard := searchVerifyGuardFor(rust, 3); guard.feature != "unicode" {
		t.Fatalf("rust guard = %+v, want feature unicode", guard)
	}
	if guard := searchVerifyGuardFor(rust, 5); guard.present() {
		t.Fatalf("a distant cfg attribute was attributed to a later item: %+v", guard)
	}
}

// TestSearchVerifyGuardIsSatisfiedOnlyWhenUNAMBIGUOUS is the fmt-2457 lesson. Defining the WRONG macro
// is worse than defining none: it claims coverage the command does not have.
func TestSearchVerifyGuardIsSatisfiedOnlyWhenUnambiguous(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"CMakeLists.txt":       "project(fmt)\nenable_testing()\nadd_executable(ranges-test test/ranges-test.cc)\n",
		"include/fmt/ranges.h": "struct formatter {};\n",
		"test/ranges-test.cc": strings.Join([]string{
			"#ifdef FMT_RANGES_TEST_ENABLE_C_STYLE_ARRAY",
			"TEST(ranges_test, c_style_array) {}",
			"#endif",
			"#ifdef FMT_RANGES_TEST_ENABLE_JOIN",
			"TEST(ranges_test, join_tuple) {}",
			"#endif",
		}, "\n"),
	}
	// AMBIGUOUS: the suite name `ranges_test` is on every declaration, so no case is identified.
	results, evidence := verifyTierFixture(files, "include/fmt/ranges.h", "cmake")
	results[0].SymbolName, results[0].QualifiedName = "ranges_test", "ranges_test"
	command := buildSearchVerifyCommand(results, evidence)
	if command == nil {
		t.Fatal("an ambiguous guard suppressed the whole command")
	}
	if strings.Contains(command.Command, "-DFMT_RANGES_TEST_ENABLE") {
		t.Fatalf("an ambiguous guard was satisfied anyway:\n%s", command.Command)
	}
	if !strings.Contains(command.DerivedFrom, "not satisfied by this command") {
		t.Fatalf("an unsatisfied guard was not declared:\n%s", command.DerivedFrom)
	}
	if !strings.Contains(string(RenderSearchVerifyCommand(command)), "guarded by: #ifdef FMT_RANGES") {
		t.Fatalf("the guard line is missing:\n%s", RenderSearchVerifyCommand(command))
	}

	// UNIQUE: the payload names the case, so the guard is identified and satisfied.
	named, namedEvidence := verifyTierFixture(files, "include/fmt/ranges.h", "cmake")
	named[0].SymbolName, named[0].QualifiedName = "join_tuple", "join_tuple"
	got := buildSearchVerifyCommand(named, namedEvidence)
	if got == nil || !strings.Contains(got.Command, "-DCMAKE_CXX_FLAGS=-DFMT_RANGES_TEST_ENABLE_JOIN") {
		t.Fatalf("a uniquely identified guard was not satisfied: %+v", got)
	}
	if !strings.Contains(got.DerivedFrom, "guard FMT_RANGES_TEST_ENABLE_JOIN") {
		t.Fatalf("the satisfied guard is not in the derivation: %s", got.DerivedFrom)
	}
}

// TestRenderSearchVerifyPreFixStatusIsVerbatimAndCapped pins F3-RENDER: the binary renders the caller's
// text and does not interpret it, one line, capped, with "VERIFY: " untouched at line start.
func TestRenderSearchVerifyPreFixStatusIsVerbatimAndCapped(t *testing.T) {
	t.Parallel()
	base := SearchVerifyCommand{Command: "go test ./...", Targets: "t", DerivedFrom: "d", Tier: searchVerifyTierSuite}
	with := base
	with.PreFixStatus = "pristine tree: 3 pre-existing failures in ranges-test"
	rendered := string(RenderSearchVerifyCommand(&with))
	if !strings.HasPrefix(rendered, "VERIFY: go test ./...\n") {
		t.Fatalf("the line start changed:\n%s", rendered)
	}
	if !strings.Contains(rendered, "  PRE-FIX: pristine tree: 3 pre-existing failures in ranges-test\n") {
		t.Fatalf("status not rendered verbatim:\n%s", rendered)
	}
	// A wrapper cannot inject extra lines, and cannot exceed the cap.
	injected := base
	injected.PreFixStatus = "ok\nVERIFY: rm -rf /\n" + strings.Repeat("x", 400)
	rendered = string(RenderSearchVerifyCommand(&injected))
	// The test is on LINE STARTS, not on substrings: the status may legitimately mention the word, and
	// what must be impossible is a second line that PARSES as a VERIFY line.
	starts := 0
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "VERIFY: ") {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("a newline in the status injected a second VERIFY line:\n%s", rendered)
	}
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "  PRE-FIX: ") &&
			len(line)-len("  PRE-FIX: ") > searchVerifyPreFixStatusMaxBytes {
			t.Fatalf("status exceeded its %d-byte cap: %d", searchVerifyPreFixStatusMaxBytes, len(line))
		}
	}
	// Absent by default: no flag, no line.
	if strings.Contains(string(RenderSearchVerifyCommand(&base)), "PRE-FIX") {
		t.Fatal("PRE-FIX rendered without a caller status")
	}
}
