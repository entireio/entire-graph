package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
	scippb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestResolveRepoHonorsInheritedGitCeiling(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	ceiling := filepath.Join(repo, "discovery-boundary")
	child := filepath.Join(ceiling, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", ceiling)
	t.Chdir(child)
	// Repository discovery with a ceiling is deliberately in-process. If this
	// regresses to Git, an empty PATH makes the test fail for the right reason.
	t.Setenv("PATH", t.TempDir())

	if discovered, err := resolveRepo(t.Context(), EntireEnv{}, ""); err == nil {
		t.Fatalf("resolveRepo = %q, nil; want no implicit repository above ceiling %q", discovered, ceiling)
	}

	// A repository selected by the trusted Entire environment is not discovery
	// and remains authoritative even when a caller also supplied a ceiling.
	selected, err := resolveRepo(t.Context(), EntireEnv{RepoRoot: repo}, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected != repo {
		t.Fatalf("resolveRepo with ENTIRE_REPO_ROOT = %q, want %q", selected, repo)
	}
}

func TestDiscoverImplicitCheckoutRootStopsAtGitCeiling(t *testing.T) {
	outer := t.TempDir()
	if err := os.Mkdir(filepath.Join(outer, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	ceiling := filepath.Join(outer, "discovery-boundary")
	blockedChild := filepath.Join(ceiling, "blocked", "child")
	insideRepo := filepath.Join(ceiling, "inside-repo")
	insideChild := filepath.Join(insideRepo, "child")
	for _, dir := range []string{blockedChild, filepath.Join(insideRepo, ".git"), insideChild} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", ceiling)

	if root, ok := discoverImplicitCheckoutRoot(blockedChild); ok {
		t.Fatalf("filesystem fallback discovered %q above ceiling %q", root, ceiling)
	}
	root, ok := discoverImplicitCheckoutRoot(insideChild)
	if !ok || root != insideRepo {
		t.Fatalf("filesystem fallback below ceiling = (%q, %v), want (%q, true)", root, ok, insideRepo)
	}
}

func TestDiscoverImplicitCheckoutRootCanonicalizesGitCeilingsBeforeEmptyMarker(t *testing.T) {
	requireSymlinkSupport(t)

	outer := t.TempDir()
	boundary := filepath.Join(outer, "discovery-boundary")
	child := filepath.Join(boundary, "nested", "child")
	for _, dir := range []string{filepath.Join(outer, ".git"), child} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(t.TempDir(), "boundary-alias")
	if err := os.Symlink(boundary, alias); err != nil {
		t.Fatal(err)
	}

	t.Run("before marker", func(t *testing.T) {
		t.Setenv("GIT_CEILING_DIRECTORIES", alias)
		if root, ok := discoverImplicitCheckoutRoot(child); ok {
			t.Fatalf("filesystem fallback discovered %q above symlinked ceiling %q", root, alias)
		}
	})

	t.Run("after marker", func(t *testing.T) {
		t.Setenv("GIT_CEILING_DIRECTORIES", string(os.PathListSeparator)+alias)
		root, ok := discoverImplicitCheckoutRoot(child)
		if !ok || root != outer {
			t.Fatalf("filesystem fallback with non-canonicalized ceiling = (%q, %v), want (%q, true)", root, ok, outer)
		}
	})
}

func TestDiscoverImplicitCheckoutRootDoesNotExcludeStartingDirectory(t *testing.T) {
	outer := t.TempDir()
	child := filepath.Join(outer, "child")
	for _, dir := range []string{filepath.Join(outer, ".git"), child} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", child)

	root, ok := discoverImplicitCheckoutRoot(child)
	if !ok || root != outer {
		t.Fatalf("filesystem fallback from the ceiling directory = (%q, %v), want (%q, true)", root, ok, outer)
	}

	if err := os.Mkdir(filepath.Join(child, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, ok = discoverImplicitCheckoutRoot(child)
	if !ok || root != child {
		t.Fatalf("filesystem fallback for repository at the ceiling = (%q, %v), want (%q, true)", root, ok, child)
	}
}

func TestDiscoverImplicitCheckoutRootDiscardsUnresolvableGitCeilings(t *testing.T) {
	outer := t.TempDir()
	child := filepath.Join(outer, "nested", "child")
	for _, dir := range []string{filepath.Join(outer, ".git"), child} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing-ceiling")
	t.Setenv("GIT_CEILING_DIRECTORIES", "relative-ceiling"+string(os.PathListSeparator)+missing)

	root, ok := discoverImplicitCheckoutRoot(child)
	if !ok || root != outer {
		t.Fatalf("filesystem fallback with unusable ceilings = (%q, %v), want (%q, true)", root, ok, outer)
	}
}

func TestResolveRepoIgnoresUnrelatedGitCeilingsWithoutStartingGit(t *testing.T) {
	repo := t.TempDir()
	child := filepath.Join(repo, "nested", "child")
	for _, dir := range []string{filepath.Join(repo, ".git"), child} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Chdir(child)

	got, err := resolveRepo(t.Context(), EntireEnv{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != repo {
		t.Fatalf("resolveRepo with unrelated ceiling = %q, want %q", got, repo)
	}
}

func TestDoctorPrintsEntireEnvironment(t *testing.T) {
	var out bytes.Buffer
	dataDir := t.TempDir()
	err := Run(t.Context(), Options{
		Env: EntireEnv{
			CLIVersion:    "0.6.3",
			RepoRoot:      t.TempDir(),
			PluginDataDir: dataDir,
		},
		Stdout: &out,
		Stderr: &out,
	}, []string{"doctor"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ENTIRE_CLI_VERSION=0.6.3", "ENTIRE_REPO_ROOT=", "ENTIRE_PLUGIN_DATA_DIR=", "plugin_data_dir=writable", "repo_root="} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out.String())
		}
	}
}

func TestDoctorWorksOutsideGitRepo(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	var out bytes.Buffer
	err = Run(t.Context(), Options{
		Env:    EntireEnv{PluginDataDir: t.TempDir()},
		Stdout: &out,
		Stderr: &out,
	}, []string{"doctor"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"repo_root=<unset>", "repo_error="} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out.String())
		}
	}
}

func TestProviderJSONCommands(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	var versionOut bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &versionOut}, []string{"version", "--json"}); err != nil {
		t.Fatal(err)
	}
	var version map[string]string
	if err := json.Unmarshal(versionOut.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if version["provider"] != "entire-graph" || version["version"] != "0.1.0" {
		t.Fatalf("version json = %#v", version)
	}

	var doctorOut bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &doctorOut}, []string{"doctor", "--json"}); err != nil {
		t.Fatal(err)
	}
	var doctor map[string]any
	if err := json.Unmarshal(doctorOut.Bytes(), &doctor); err != nil {
		t.Fatalf("doctor json invalid:\n%s\n%v", doctorOut.String(), err)
	}
	if doctor["repo_root"] != repo {
		t.Fatalf("doctor repo_root = %#v", doctor["repo_root"])
	}
	if doctor["no_egress"] != true {
		t.Fatalf("doctor no_egress = %#v", doctor["no_egress"])
	}

	var capabilitiesOut bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &capabilitiesOut}, []string{"capabilities", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capabilitiesOut.String(), `"supported_relation_types"`) {
		t.Fatalf("capabilities output:\n%s", capabilitiesOut.String())
	}
	if !strings.Contains(capabilitiesOut.String(), `"compact_snapshot_ndjson_v1":true`) {
		t.Fatalf("capabilities omit compact snapshot support:\n%s", capabilitiesOut.String())
	}
	if !strings.Contains(capabilitiesOut.String(), `"scip_snapshot_experimental":true`) {
		t.Fatalf("capabilities omit scip snapshot support:\n%s", capabilitiesOut.String())
	}
}

func TestProviderProfileFlag(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.go", "package a\ntype T struct{ X int }\nfunc (t *T) M() int { return t.X }\n")

	// syntax-only: header reports the profile; only structural relations appear.
	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out},
		[]string{"snapshot", "--repo", repo, "--format", "ndjson", "--profile", "syntax-only"})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header json: %v", err)
	}
	if header["profile"] != "syntax-only" {
		t.Fatalf("header profile = %v", header["profile"])
	}
	for _, line := range lines[1:] {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec["record_type"] == "relation" {
			if rt := rec["type"]; rt != "DEFINES" && rt != "CONTAINS" {
				t.Fatalf("syntax-only emitted non-structural relation %v", rt)
			}
		}
	}

	// An unknown profile is rejected.
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &bytes.Buffer{}},
		[]string{"snapshot", "--repo", repo, "--format", "ndjson", "--profile", "bogus"}); err == nil {
		t.Fatalf("expected error for unknown profile")
	}
}

func TestProviderNDJSONCommands(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", `def validate_token(token):
    return bool(token)

def check_token(token):
    return validate_token(token)
`)

	tests := []struct {
		command         string
		wantRecordTypes []string
	}{
		{command: "snapshot", wantRecordTypes: []string{"file", "symbol", "relation"}},
		{command: "symbols", wantRecordTypes: []string{"symbol"}},
		{command: "edges", wantRecordTypes: []string{"relation"}},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{tt.command, "--repo", repo, "--format", "ndjson"})
		if err != nil {
			t.Fatalf("%s: %v", tt.command, err)
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) < 2 {
			t.Fatalf("%s emitted too few lines:\n%s", tt.command, out.String())
		}
		var header map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
			t.Fatalf("%s invalid header json %q: %v", tt.command, lines[0], err)
		}
		if header["schema_version"] != "1.1" || header["provider"] != "entire-graph" {
			t.Fatalf("%s header = %#v", tt.command, header)
		}
		seenTypes := map[string]bool{}
		allowedTypes := map[string]bool{"summary": true} // streaming trailer, always allowed
		for _, recordType := range tt.wantRecordTypes {
			allowedTypes[recordType] = true
		}
		for _, line := range lines[1:] {
			var decoded map[string]any
			if err := json.Unmarshal([]byte(line), &decoded); err != nil {
				t.Fatalf("%s invalid json line %q: %v", tt.command, line, err)
			}
			recordType, ok := decoded["record_type"].(string)
			if !ok {
				t.Fatalf("%s record missing record_type: %#v", tt.command, decoded)
			}
			if !allowedTypes[recordType] {
				t.Fatalf("%s emitted unexpected record type %q in %#v", tt.command, recordType, decoded)
			}
			seenTypes[recordType] = true
		}
		for _, recordType := range tt.wantRecordTypes {
			if !seenTypes[recordType] {
				t.Fatalf("%s missing record type %q:\n%s", tt.command, recordType, out.String())
			}
		}
	}
}

func TestSnapshotAcceptsNoNetwork(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	var out bytes.Buffer
	err = Run(t.Context(), Options{Version: "0.1.0", Stdout: &out}, []string{"snapshot", "--repo", ".", "--format", "ndjson", "--no-network"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"schema_version":"1.1"`) {
		t.Fatalf("snapshot output:\n%s", out.String())
	}
}

func TestSnapshotAcceptsWorktree(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"snapshot", "--repo", repo, "--format", "ndjson", "--no-network", "--worktree"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"schema_version":"1.1"`) {
		t.Fatalf("snapshot output:\n%s", out.String())
	}
}

func TestProviderRecordsCacheDoesNotReplaySameTreeCommitHeader(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "main.go", "package sample\n\nfunc Main() {}\n")
	git(t, repo, "add", "main.go")
	git(t, repo, "commit", "-m", "initial")
	cacheDir := t.TempDir()

	run := func() string {
		t.Helper()
		var out bytes.Buffer
		err := Run(t.Context(), Options{
			Version: "commit-cache-test",
			Env:     EntireEnv{RepoRoot: repo},
			Stdout:  &out,
			Stderr:  io.Discard,
		}, []string{"snapshot", "--repo", repo, "--format", "ndjson", "--cache-dir", cacheDir})
		if err != nil {
			t.Fatal(err)
		}
		line, _, ok := bytes.Cut(out.Bytes(), []byte{'\n'})
		if !ok {
			t.Fatalf("snapshot omitted header line: %q", out.Bytes())
		}
		var header sem.SnapshotHeader
		if err := json.Unmarshal(line, &header); err != nil {
			t.Fatalf("decode snapshot header: %v\n%s", err, line)
		}
		return header.Commit
	}

	first := run()
	git(t, repo, "commit", "--allow-empty", "-m", "same tree, new provenance")
	second := run()
	if want := rev(t, repo, "HEAD^{commit}"); second != want {
		t.Fatalf("same-tree cache replayed commit %q, want current commit %q (first %q)", second, want, first)
	}
	if second == first {
		t.Fatal("empty commit reused the previous snapshot header")
	}
}

func TestSnapshotCompactNDJSONRoundTripsToNativeRecords(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "main.go", "package sample\n\nfunc caller() { callee() }\nfunc callee() {}\n")

	var native, compact bytes.Buffer
	opts := Options{Version: "compact-test", Env: EntireEnv{RepoRoot: repo}, Stderr: io.Discard}
	opts.Stdout = &native
	if err := Run(t.Context(), opts, []string{"snapshot", "--repo", repo, "--worktree"}); err != nil {
		t.Fatal(err)
	}
	opts.Stdout = &compact
	if err := Run(t.Context(), opts, []string{"snapshot", "--repo", repo, "--worktree", "--format", "compact-ndjson"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(compact.Bytes(), []byte(`["h",1,`)) {
		t.Fatalf("compact stream did not start with v1 header: %q", compact.Bytes())
	}
	index, err := sem.LoadCompactSnapshot(bytes.NewReader(compact.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	nativeRecords := decodeNativeSnapshotRecords(t, native.Bytes())
	if got, want := index.CanonicalSemanticHash, snapshotSemanticHash(t, nativeRecords); got != want {
		t.Fatalf("canonical semantic hash = %s, want %s", got, want)
	}
	var decoded []any
	if _, err := sem.DecodeCompactSnapshot(bytes.NewReader(compact.Bytes()), func(record any) error {
		decoded = append(decoded, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := snapshotPublicProjection(t, decoded), snapshotPublicProjection(t, nativeRecords); !reflect.DeepEqual(got, want) {
		t.Fatalf("compact public projection differs\n got=%s\nwant=%s", got, want)
	}
}

func TestSnapshotNDJSONRemainsDefaultObjectFormat(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "main.go", "package sample\nfunc main() {}\n")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Env: EntireEnv{RepoRoot: repo}, Stdout: &out, Stderr: io.Discard}, []string{"snapshot", "--repo", repo, "--worktree"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("{")) {
		t.Fatalf("default snapshot is not object NDJSON: %q", out.Bytes())
	}
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
		var object map[string]any
		if err := json.Unmarshal(line, &object); err != nil {
			t.Fatalf("default snapshot line is not an object: %v", err)
		}
		if object["record_type"] == nil && object["schema_version"] == nil {
			t.Fatalf("default snapshot object is neither header nor record: %s", line)
		}
	}
}

func TestCompactNDJSONIsSnapshotOnly(t *testing.T) {
	testSnapshotFormatIsSnapshotOnly(t, "compact-ndjson")
}

func TestSnapshotSCIPIsSnapshotOnly(t *testing.T) {
	testSnapshotFormatIsSnapshotOnly(t, "scip")
}

func testSnapshotFormatIsSnapshotOnly(t *testing.T, format string) {
	t.Helper()
	repo := t.TempDir()
	write(t, repo, "main.go", "package sample\nfunc main() {}\n")
	for _, command := range []string{"symbols", "edges"} {
		err := Run(t.Context(), Options{Env: EntireEnv{RepoRoot: repo}, Stdout: io.Discard}, []string{command, "--repo", repo, "--worktree", "--format", format})
		if err == nil || !strings.Contains(err.Error(), "only valid for snapshot") {
			t.Fatalf("%s %s format error = %v", command, format, err)
		}
	}
}

func TestCompactNDJSONRejectsTargetedRelationFilters(t *testing.T) {
	testSnapshotFormatRejectsTargetedRelationFilters(t, "compact-ndjson")
}

func TestSnapshotSCIPRejectsTargetedRelationFilters(t *testing.T) {
	testSnapshotFormatRejectsTargetedRelationFilters(t, "scip")
}

func testSnapshotFormatRejectsTargetedRelationFilters(t *testing.T, format string) {
	t.Helper()
	repo := t.TempDir()
	write(t, repo, "main.go", "package sample\nfunc caller() { callee() }\nfunc callee() {}\n")
	err := Run(t.Context(), Options{Env: EntireEnv{RepoRoot: repo}, Stdout: io.Discard}, []string{"snapshot", "--repo", repo, "--worktree", "--format", format, "--from", "caller"})
	if err == nil || !strings.Contains(err.Error(), "requires a complete snapshot") {
		t.Fatalf("targeted %s format error = %v", format, err)
	}
}

func TestSnapshotSCIPReservesStderrForOmissionNote(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "main.go", "package sample\nfunc main() {}\n")
	err := Run(t.Context(), Options{Env: EntireEnv{RepoRoot: repo}, Stdout: io.Discard, Stderr: io.Discard}, []string{"snapshot", "--repo", repo, "--worktree", "--format", "scip", "--progress"})
	if err == nil || !strings.Contains(err.Error(), "stderr is reserved for the JSON omission note") {
		t.Fatalf("scip progress error = %v", err)
	}
}

func TestSnapshotSCIPEmitsBinaryIndexAndOmissionNote(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "main.go", "package sample\n\nfunc caller() { callee() }\nfunc callee() {}\n")

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), Options{Version: "scip-test", Env: EntireEnv{RepoRoot: repo}, Stdout: &stdout, Stderr: &stderr}, []string{"snapshot", "--repo", repo, "--worktree", "--format", "scip"})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.Len() == 0 || bytes.HasPrefix(stdout.Bytes(), []byte("{")) || bytes.HasPrefix(stdout.Bytes(), []byte(`["h"`)) {
		t.Fatalf("scip output does not look binary: %q", stdout.Bytes())
	}
	var index scippb.Index
	if err := proto.Unmarshal(stdout.Bytes(), &index); err != nil {
		t.Fatalf("scip output is not a valid Index protobuf: %v", err)
	}
	if got := index.GetMetadata().GetToolInfo().GetName(); got != sem.ProviderName {
		t.Fatalf("tool name = %q, want %q", got, sem.ProviderName)
	}
	if got := index.GetMetadata().GetToolInfo().GetArguments(); !strings.Contains(strings.Join(got, " "), "--worktree") {
		t.Fatalf("scip metadata omits worktree provenance: %#v", got)
	}
	documents := map[string]*scippb.Document{}
	displayNames := map[string]bool{}
	references := 0
	// This fixture declares no manifest version, so every symbol must carry the
	// unversioned fallback. Worktree provenance is asserted through the omission
	// note below, not through the package version, which no longer encodes it.
	unversionedSymbols := 0
	for _, doc := range index.GetDocuments() {
		documents[doc.GetRelativePath()] = doc
		for _, info := range doc.GetSymbols() {
			displayNames[info.GetDisplayName()] = true
			parsed, err := scippb.ParseSymbol(info.GetSymbol())
			if err != nil {
				t.Fatalf("invalid SCIP symbol %q: %v", info.GetSymbol(), err)
			}
			if parsed.GetPackage().GetVersion() == sem.ScipProjectVersionUnknown {
				unversionedSymbols++
			}
		}
		for _, occurrence := range doc.GetOccurrences() {
			if occurrence.GetSymbolRoles()&int32(scippb.SymbolRole_Definition) == 0 {
				references++
			}
		}
	}
	if documents["main.go"] == nil || documents["main.go"].GetLanguage() != "Go" || !displayNames["caller"] || !displayNames["callee"] || references == 0 || unversionedSymbols == 0 {
		t.Fatalf("scip index omitted expected navigation facts: docs=%v names=%v references=%d unversioned_symbols=%d", documents, displayNames, references, unversionedSymbols)
	}
	var note sem.SCIPOmissionNote
	if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &note); err != nil {
		t.Fatalf("stderr omission note is not JSON: %q: %v", stderr.String(), err)
	}
	if note.RecordType != "scip_omissions" || note.Format != "scip" || note.EmittedDefinitions == 0 || !note.WorktreeSnapshot || note.WarningCount == 0 {
		t.Fatalf("unexpected scip omission note: %#v", note)
	}
}

func TestSCIPOmissionNoteWithSummaryCapturesPartialState(t *testing.T) {
	ok := &sem.SnapshotSummary{Stats: sem.ProviderStats{CompletenessLevel: "ok"}}
	note := scipOmissionNoteWithSummary(sem.SCIPOmissionNote{Format: "scip"}, ok)
	if note.PartialSnapshot {
		t.Fatalf("clean summary marked partial: %#v", note)
	}

	partial := &sem.SnapshotSummary{
		Warnings:        []sem.ProviderWarning{{Code: "W"}},
		PartialFailures: []sem.PartialFailure{{Code: "E"}},
		Stats:           sem.ProviderStats{CompletenessLevel: "degraded"},
	}
	note = scipOmissionNoteWithSummary(sem.SCIPOmissionNote{Format: "scip"}, partial)
	if !note.PartialSnapshot || note.CompletenessLevel != "degraded" || note.WarningCount != 1 || note.PartialFailureCount != 1 {
		t.Fatalf("partial summary not captured in scip note: %#v", note)
	}
}

func decodeNativeSnapshotRecords(t *testing.T, data []byte) []any {
	t.Helper()
	var records []any
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		var envelope struct {
			RecordType    string `json:"record_type"`
			SchemaVersion string `json:"schema_version"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatal(err)
		}
		var record any
		if envelope.SchemaVersion != "" {
			var decoded sem.SnapshotHeader
			if err := json.Unmarshal(line, &decoded); err != nil {
				t.Fatal(err)
			}
			record = decoded
		} else {
			switch envelope.RecordType {
			case "file":
				var decoded sem.FileRecord
				if err := json.Unmarshal(line, &decoded); err != nil {
					t.Fatal(err)
				}
				record = decoded
			case "external":
				var decoded sem.ExternalRecord
				if err := json.Unmarshal(line, &decoded); err != nil {
					t.Fatal(err)
				}
				record = decoded
			case "symbol":
				var decoded sem.SymbolRecord
				if err := json.Unmarshal(line, &decoded); err != nil {
					t.Fatal(err)
				}
				record = decoded
			case "relation":
				var decoded sem.RelationRecord
				if err := json.Unmarshal(line, &decoded); err != nil {
					t.Fatal(err)
				}
				record = decoded
			case "summary":
				var decoded sem.SnapshotSummary
				if err := json.Unmarshal(line, &decoded); err != nil {
					t.Fatal(err)
				}
				record = decoded
			default:
				t.Fatalf("unexpected record type %q", envelope.RecordType)
			}
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func snapshotSemanticHash(t *testing.T, records []any) string {
	t.Helper()
	hash := sem.NewSnapshotSemanticHasher()
	for _, record := range records {
		if err := hash.Add(record); err != nil {
			t.Fatal(err)
		}
	}
	return hash.SumHex()
}

func snapshotPublicProjection(t *testing.T, records []any) []json.RawMessage {
	t.Helper()
	projection := make([]json.RawMessage, 0, len(records))
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		projection = append(projection, data)
	}
	return projection
}

func TestSearchCommandReturnsRankedJSON(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", `def validate_token(token):
    """Validate a signed authentication token."""
    return bool(token)
`)
	write(t, repo, "unsupported.f90", "subroutine unsupported\nend subroutine unsupported\n")

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"search",
		"--repo", repo,
		"--query", "validate authentication token",
		"--format", "json",
		"--profile", "syntax-only",
		"--worktree",
		"--top-k", "3",
		"--index-all-files",
	})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Results []struct {
			Rank       int    `json:"rank"`
			FilePath   string `json:"file_path"`
			SymbolName string `json:"symbol_name"`
		} `json:"results"`
		Stats struct {
			ContextBudgetBytes int `json:"context_budget_bytes"`
			ResultBytes        int `json:"result_bytes"`
			QueryLatencyMS     int `json:"query_latency_ms"`
			TotalLatencyMS     int `json:"total_latency_ms"`
			SearchLatencyMS    int `json:"search_latency_ms"`
		} `json:"stats"`
		PartialFailures []struct {
			Code     string `json:"code"`
			FilePath string `json:"file_path"`
		} `json:"partial_failures"`
		Completeness struct {
			Languages map[string]struct {
				Files int `json:"files"`
			} `json:"languages"`
		} `json:"completeness"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatalf("invalid search JSON: %v\n%s", err, out.String())
	}
	if len(response.Results) == 0 || response.Results[0].Rank != 1 || response.Results[0].FilePath != "auth.py" || response.Results[0].SymbolName != "validate_token" {
		t.Fatalf("search response = %#v", response)
	}
	if response.Stats.ContextBudgetBytes != defaultSearchContextBytes || response.Stats.ResultBytes > response.Stats.ContextBudgetBytes {
		t.Fatalf("search context budget = %#v", response.Stats)
	}
	if response.Stats.SearchLatencyMS != response.Stats.TotalLatencyMS || response.Stats.TotalLatencyMS < response.Stats.QueryLatencyMS {
		t.Fatalf("search telemetry = %#v", response.Stats)
	}
	if len(response.PartialFailures) != 1 || response.PartialFailures[0].Code != "E_UNSUPPORTED_LANGUAGE" || response.PartialFailures[0].FilePath != "unsupported.f90" {
		t.Fatalf("search partial failures = %#v", response.PartialFailures)
	}
	if response.Completeness.Languages["Python"].Files != 1 {
		t.Fatalf("search completeness = %#v", response.Completeness)
	}
}

func TestSearchCommandAgentFormatIsCompactAndFocused(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", `def validate_token(token):
    """Validate a signed authentication token."""
    first = token.strip()
    second = first.lower()
    third = bool(second)
    return third
`)

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"search",
		"--repo", repo,
		"--query", "validate authentication token",
		"--format", "agent",
		"--profile", "syntax-only",
		"--worktree",
		"--top-k", "3",
		"--max-context-bytes", "512",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Len() > 512 {
		t.Fatalf("agent output used %d bytes, budget 512:\n%s", out.Len(), out.String())
	}
	if !strings.Contains(out.String(), "1. auth.py:1-6 validate_token") || !strings.Contains(out.String(), "validate_token") {
		t.Fatalf("agent output omitted ranked location or focused code:\n%s", out.String())
	}
	if strings.Contains(out.String(), `"symbol_id"`) || strings.Contains(out.String(), `"stats"`) {
		t.Fatalf("agent output retained machine-schema overhead:\n%s", out.String())
	}
	for _, want := range []string{"Index: cache-", "Query:", "Preselect:", "Total:"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("agent output omitted %q telemetry:\n%s", want, out.String())
		}
	}
}

func TestSearchCommandAgentFormatDoesNotTreatHeaderSizedBudgetAsUnbounded(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", `def validate_token(token):
    """Validate a signed authentication token."""
    return bool(token)
`)

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"search",
		"--repo", repo,
		"--query", "validate authentication token",
		"--format", "agent",
		"--profile", "syntax-only",
		"--worktree",
		"--max-context-bytes", "20",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Index: cache-miss") {
		t.Fatalf("agent output omitted cache state: %q", out.String())
	}
	if strings.Contains(out.String(), "validate_token") || out.Len() > 20 {
		t.Fatalf("header-sized positive budget became unbounded: %q", out.String())
	}
}

func TestSearchCommandAgentFormatKeepsTopLocationUnderTightBudget(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.py", "def target():\n    return True\n")

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"search",
		"--repo", repo,
		"--query", "target",
		"--format", "agent",
		"--profile", "syntax-only",
		"--worktree",
		"--max-context-bytes", "64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Len() > 64 {
		t.Fatalf("tight agent output used %d bytes, budget 64: %q", out.Len(), out.String())
	}
	if !strings.Contains(out.String(), "a.py:1") || !strings.Contains(out.String(), "target") || !strings.Contains(out.String(), "*") {
		t.Fatalf("tight telemetry crowded out the top-ranked location: %q", out.String())
	}
	if !strings.HasPrefix(out.String(), "I:miss/") {
		t.Fatalf("tight output omitted compact telemetry: %q", out.String())
	}
}

func TestAgentSearchBudgetsFavorHigherRanks(t *testing.T) {
	budgets := rankedAgentSearchBudgets(4, 1000)
	if len(budgets) != 4 || budgets[0] <= budgets[1] || budgets[1] <= budgets[2] || budgets[2] <= budgets[3] {
		t.Fatalf("budgets are not rank weighted: %#v", budgets)
	}
	total := 0
	for _, budget := range budgets {
		total += budget
	}
	if total != 1000 {
		t.Fatalf("budget total = %d, want 1000: %#v", total, budgets)
	}
}

func TestHelpDocumentsNeighborAgentContextCap(t *testing.T) {
	var out bytes.Buffer
	renderCommandHelp(&out, "neighbors")
	help := out.String()
	if !strings.Contains(help, "--max-context-bytes") {
		t.Fatalf("neighbors help omitted --max-context-bytes flag:\n%s", help)
	}
	if !strings.Contains(help, "16384") {
		t.Fatalf("neighbors help omitted agent context cap default 16384:\n%s", help)
	}
}

func TestDiffProgressReportsToStderr(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change")

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), Options{
		Env:    EntireEnv{RepoRoot: repo},
		Stdout: &stdout,
		Stderr: &stderr,
	}, []string{"diff", "--base", "HEAD~1", "--head", "HEAD", "--progress"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"phase=discover", "phase=parse", "phase=reconcile", "phase=dependents", "phase=complete"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("progress output missing %q:\n%s", want, stderr.String())
		}
	}
	if strings.Contains(stdout.String(), "graph diff progress") {
		t.Fatalf("progress leaked to stdout:\n%s", stdout.String())
	}
}

func TestNeighborsAgentFormatReturnsBoundedCallGraphAndPaths(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "calls.go", `package calls

func Alpha() { Beta() }
func Beta() { Gamma() }
func Gamma() {}
`)

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"neighbors",
		"--repo", repo,
		"--symbol", "Beta",
		"--format", "agent",
		"--depth", "2",
		"--limit", "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, expected := range []string{
		"Focus: Beta (calls.go:4)",
		"Callers:\n- Alpha (calls.go:3)",
		"Callees:\n- Gamma (calls.go:5)",
		"Alpha -> Beta -> Gamma",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("neighbors output omitted %q:\n%s", expected, text)
		}
	}
}

func TestNeighborsJSONDisambiguatesByFileAndDirection(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "one.py", "def target():\n    helper()\n\ndef helper():\n    return True\n")
	write(t, repo, "two.py", "def target():\n    return False\n")

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"neighbors", "--repo", repo, "--symbol", "target", "--file", "one.py",
		"--direction", "out", "--format", "json",
	})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Matches []struct {
			Symbol struct {
				FilePath string `json:"file_path"`
			} `json:"symbol"`
			Incoming []any `json:"incoming"`
			Outgoing []any `json:"outgoing"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Matches) != 1 || response.Matches[0].Symbol.FilePath != "one.py" || len(response.Matches[0].Incoming) != 0 || len(response.Matches[0].Outgoing) != 1 {
		t.Fatalf("neighbors response = %#v", response)
	}
}

func TestNeighborsIncludesTopLevelFileCallersAndConstructors(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "target.py", "class Result:\n    pass\n\ndef target():\n    return Result()\n")
	write(t, repo, "entry.py", "from target import target\n\nvalue = target()\n")

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"neighbors", "--repo", repo, "--symbol", "target", "--depth", "2", "--format", "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, expected := range []string{
		"Callers:\n- entry.py (entry.py) [file-level, import_resolved]",
		"Callees:\n- Result (target.py:1) [CONSTRUCTS, exact]",
		"entry.py -> target -> Result",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("neighbors output omitted %q:\n%s", expected, text)
		}
	}
}

func TestProviderCommandsAcceptIgnoreFile(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, ".brainignore", "ignored/\n")
	write(t, repo, "ignored/ignored.py", "def ignored():\n    return True\n")
	write(t, repo, "keep.py", "def keep():\n    return True\n")

	for _, command := range []string{"snapshot", "symbols", "edges"} {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			command,
			"--repo", repo,
			"--format", "ndjson",
			"--worktree",
			"--ignore-file", ".brainignore",
		})
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if !strings.Contains(out.String(), `"schema_version":"1.1"`) {
			t.Fatalf("%s output missing header:\n%s", command, out.String())
		}
		if strings.Contains(out.String(), "ignored.py") || strings.Contains(out.String(), "ignored") {
			t.Fatalf("%s output included ignored path:\n%s", command, out.String())
		}
	}
}

func TestProviderCommandsAcceptIncludeFile(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, ".gitignore", "ignored/\n")
	write(t, repo, ".graphinclude", "ignored/\n")
	write(t, repo, "ignored/reopened.py", `def reopened():
    return True
`)

	for _, command := range []string{"snapshot", "symbols", "edges"} {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			command,
			"--repo", repo,
			"--format", "ndjson",
			"--worktree",
			"--include-file", ".graphinclude",
		})
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if !strings.Contains(out.String(), `"schema_version":"1.1"`) {
			t.Fatalf("%s output missing header:\n%s", command, out.String())
		}
		if !strings.Contains(out.String(), "reopened") {
			t.Fatalf("%s output did not include reopened file:\n%s", command, out.String())
		}
	}
}

func TestSnapshotSkipsTrackedVendoredDependencies(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "src/app.ts", "export function app() { return true }\n")
	write(t, repo, "node_modules/pkg/index.ts", "export function vendored() { return false }\n")
	write(t, repo, "apps/web/package-lock.json", `{"packages":{"node_modules/noisy":{"version":"1.0.0"}}}`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"snapshot",
		"--repo", repo,
		"--format", "ndjson",
		"--no-network",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "src/app.ts") || !strings.Contains(text, "app") {
		t.Fatalf("snapshot output missing app source:\n%s", text)
	}
	if strings.Contains(text, "node_modules") || strings.Contains(text, "vendored") || strings.Contains(text, "package-lock") {
		t.Fatalf("snapshot output included tracked dependency:\n%s", text)
	}
}

func TestAnalyzeJSONCommand(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "update")

	var out bytes.Buffer
	err := Run(t.Context(), Options{
		Env:    EntireEnv{RepoRoot: repo},
		Stdout: &out,
		Stderr: &out,
	}, []string{"analyze", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"dependents_count"`) {
		t.Fatalf("analyze json missing dependents_count:\n%s", out.String())
	}
}

// TestDiffJSONIncludesSchemaVersion pins that `diff --json` output is
// versioned: downstream consumers are about to persist this payload into
// checkpoint metadata, so every emission must carry schema_version (and, when
// the caller has a build version to attribute, producer_version) so a reader
// years later knows which shape it is holding.
func TestDiffJSONIncludesSchemaVersion(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "update")

	var out bytes.Buffer
	err := Run(t.Context(), Options{
		Version: "9.9.9-test",
		Env:     EntireEnv{RepoRoot: repo},
		Stdout:  &out,
		Stderr:  &out,
	}, []string{"diff", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"schema_version": "1.1"`) {
		t.Fatalf("diff json missing schema_version:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `"producer_version": "9.9.9-test"`) {
		t.Fatalf("diff json missing producer_version:\n%s", out.String())
	}

	var payload struct {
		SchemaVersion   string `json:"schema_version"`
		ProducerVersion string `json:"producer_version"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("diff json invalid:\n%s\n%v", out.String(), err)
	}
	if payload.SchemaVersion != "1.1" {
		t.Fatalf("schema_version = %q, want 1.1", payload.SchemaVersion)
	}
	if payload.ProducerVersion != "9.9.9-test" {
		t.Fatalf("producer_version = %q, want 9.9.9-test", payload.ProducerVersion)
	}
}

func TestDiffJSONCommandCoversEverySupportedLanguage(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	fixtures := []struct {
		path     string
		language string
		before   string
		after    string
	}{
		{path: "auth.sh", language: "Bash", before: "validate_token() { echo ok; }\n", after: "validate_token() { echo ok; }\nrun_task() { echo run; }\n"},
		{path: "main.c", language: "C", before: "int validate(int token) { return token; }\n", after: "int validate(int token) { return token; }\nint audit(int token) { return token; }\n"},
		{path: "User.cs", language: "C#", before: "class User { public bool Validate(string token) { return true; } }\n", after: "class User { public bool Validate(string token) { return true; } public bool Audit(string token) { return true; } }\n"},
		{path: "main.cpp", language: "C++", before: "class User { public: void run() {} };\n", after: "class User { public: void run() {} void audit() {} };\n"},
		{path: "schema.cue", language: "CUE", before: "#User: { name: string }\n", after: "#User: { name: string }\nvalidate: true\n"},
		{path: "auth.ex", language: "Elixir", before: "defmodule User do\n  def validate(token), do: true\nend\n", after: "defmodule User do\n  def validate(token), do: true\n  def audit(token), do: token\nend\n"},
		{path: "main.go", language: "Go", before: "package main\nfunc Validate(token string) bool { return token != \"\" }\n", after: "package main\nfunc Validate(token string) bool { return token != \"\" }\nfunc Audit(token string) bool { return true }\n"},
		{path: "Auth.groovy", language: "Groovy", before: "class User { boolean validate(String token) { true } }\n", after: "class User { boolean validate(String token) { true } boolean audit(String token) { true } }\n"},
		{path: "main.tf", language: "HCL", before: "resource \"aws_instance\" \"web\" { ami = \"x\" }\n", after: "resource \"aws_instance\" \"web\" { ami = \"x\" }\nvariable \"name\" {}\n"},
		{path: "User.java", language: "Java", before: "class User { boolean validate(String token) { return true; } }\n", after: "class User { boolean validate(String token) { return true; } boolean audit(String token) { return true; } }\n"},
		{path: "app.js", language: "JavaScript", before: "function validate(token) { return Boolean(token); }\n", after: "function validate(token) { return Boolean(token); }\nfunction audit(token) { return token; }\n"},
		{path: "User.kt", language: "Kotlin", before: "class User { fun validate(token: String): Boolean { return true } }\n", after: "class User { fun validate(token: String): Boolean { return true } fun audit(token: String): Boolean { return true } }\n"},
		{path: "auth.lua", language: "Lua", before: "function validate(token) return true end\n", after: "function validate(token) return true end\nfunction audit(token) return token end\n"},
		{path: "auth.ml", language: "OCaml", before: "let validate token = true\n", after: "let validate token = true\nlet audit token = token\n"},
		{path: "auth.php", language: "PHP", before: "<?php\nfunction validate($token) { return true; }\n", after: "<?php\nfunction validate($token) { return true; }\nfunction audit($token) { return $token; }\n"},
		{path: "auth.proto", language: "Protocol Buffers", before: "syntax = \"proto3\";\nmessage User { string name = 1; }\n", after: "syntax = \"proto3\";\nmessage User { string name = 1; }\nmessage Audit { string id = 1; }\n"},
		{path: "auth.py", language: "Python", before: "def validate_token(token):\n    return bool(token)\n", after: "def validate_token(token):\n    return bool(token)\n\ndef audit_token(token):\n    return token\n"},
		{path: "auth.rb", language: "Ruby", before: "def validate(token)\n  true\nend\n", after: "def validate(token)\n  true\nend\ndef audit(token)\n  token\nend\n"},
		{path: "lib.rs", language: "Rust", before: "pub fn validate(value: &str) -> bool { true }\n", after: "pub fn validate(value: &str) -> bool { true }\npub fn audit(value: &str) -> bool { true }\n"},
		{path: "schema.sql", language: "SQL", before: "CREATE TABLE users (id INT);\n", after: "CREATE TABLE users (id INT);\nCREATE TABLE audit_events (id INT);\n"},
		{path: "Auth.scala", language: "Scala", before: "class User { def validate(token: String): Boolean = true }\n", after: "class User { def validate(token: String): Boolean = true; def audit(token: String): Boolean = true }\n"},
		{path: "Auth.swift", language: "Swift", before: "struct User { func validate(token: String) -> Bool { true } }\n", after: "struct User { func validate(token: String) -> Bool { true } func audit(token: String) -> Bool { true } }\n"},
		{path: "app.ts", language: "TypeScript", before: "class User { validate(value: string) { return value } }\n", after: "class User { validate(value: string) { return value } audit(value: string) { return value } }\n"},
	}

	for _, fixture := range fixtures {
		write(t, repo, fixture.path, fixture.before)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial languages")
	base := rev(t, repo, "HEAD")

	for _, fixture := range fixtures {
		write(t, repo, fixture.path, fixture.after)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "update languages")
	head := rev(t, repo, "HEAD")

	var out bytes.Buffer
	err := Run(t.Context(), Options{
		Env:    EntireEnv{RepoRoot: repo},
		Stdout: &out,
		Stderr: &out,
	}, []string{"diff", "--repo", repo, "--base", base, "--head", head, "--json"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Files []struct {
			Path     string `json:"path"`
			Language string `json:"language"`
			Changes  []any  `json:"changes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("diff json invalid:\n%s\n%v", out.String(), err)
	}
	seen := map[string]string{}
	for _, file := range payload.Files {
		if len(file.Changes) == 0 {
			t.Fatalf("%s had no semantic changes: %#v", file.Path, file)
		}
		seen[file.Language] = file.Path
	}
	for _, fixture := range fixtures {
		if seen[fixture.language] == "" {
			t.Fatalf("missing %s diff for %s in %#v", fixture.language, fixture.path, payload.Files)
		}
	}
	if len(seen) != len(fixtures) {
		t.Fatalf("languages = %d, want %d: %#v", len(seen), len(fixtures), seen)
	}
}

func write(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rev(t *testing.T, repo, value string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", value)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v\n%s", value, err, out)
	}
	return strings.TrimSpace(string(out))
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestMaxSecondsFlagValidation pins --max-seconds parsing: non-numeric and
// negative values are rejected, 0 (unlimited) is accepted, and checkpoint
// refuses the flag like it refuses --progress.
func TestMaxSecondsFlagValidation(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change")

	run := func(args ...string) error {
		var stdout, stderr bytes.Buffer
		return Run(t.Context(), Options{Env: EntireEnv{RepoRoot: repo}, Stdout: &stdout, Stderr: &stderr}, args)
	}

	if err := run("diff", "--base", "HEAD~1", "--head", "HEAD", "--max-seconds", "abc"); err == nil {
		t.Fatal("non-numeric --max-seconds must be rejected")
	}
	if err := run("diff", "--base", "HEAD~1", "--head", "HEAD", "--max-seconds", "-5"); err == nil {
		t.Fatal("negative --max-seconds must be rejected")
	}
	if err := run("diff", "--base", "HEAD~1", "--head", "HEAD", "--max-seconds"); err == nil {
		t.Fatal("--max-seconds without a value must be rejected")
	}
	if err := run("diff", "--base", "HEAD~1", "--head", "HEAD", "--max-seconds", "0"); err != nil {
		t.Fatalf("--max-seconds 0 (unlimited) must be accepted, got %v", err)
	}
	if err := run("commit", "--max-seconds", "300"); err != nil {
		t.Fatalf("commit --max-seconds must be accepted, got %v", err)
	}
	if err := run("checkpoint", "some-id", "--max-seconds", "10"); err == nil || !strings.Contains(err.Error(), "--max-seconds") {
		t.Fatalf("checkpoint must reject --max-seconds, got %v", err)
	}
}

func TestParseDiffFlagsSeparatesUnknownFlagsFromPaths(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		base    string
		head    string
		paths   []string
		unknown []string
		json    bool
	}{
		{name: "defaults", args: nil, base: "HEAD~1", head: "HEAD"},
		{
			name: "base and head",
			args: []string{"--base", "main", "--head", "HEAD"},
			base: "main", head: "HEAD",
		},
		{
			name: "bare paths stay paths",
			args: []string{"--base", "main", "internal/cli", "README.md"},
			base: "main", head: "HEAD",
			paths: []string{"internal/cli", "README.md"},
		},
		{
			name: "common flags still parse",
			args: []string{"--json", "--base", "main"},
			base: "main", head: "HEAD", json: true,
		},
		{
			// The regression this parser exists for: an unrecognized flag used to be filed
			// under paths, so the command exited 0 reporting no changes.
			name: "typo'd flag is unknown, not a path",
			args: []string{"--base", "main", "--jsonn"},
			base: "main", head: "HEAD",
			unknown: []string{"--jsonn"},
		},
		{
			// After `--` a flag-shaped argument is a path again, which is what the separator
			// is documented to be for.
			name: "separator makes flag-shaped args literal paths",
			args: []string{"--base", "main", "--", "--weird-path", "-x"},
			base: "main", head: "HEAD",
			paths: []string{"--weird-path", "-x"},
		},
		{
			name: "separator protects a path named like a real flag",
			args: []string{"--", "--base"},
			base: "HEAD~1", head: "HEAD",
			paths: []string{"--base"},
		},
		{
			name: "tag ref beginning with hyphen",
			args: []string{"--base", "-foo", "--head", "HEAD"},
			base: "-foo", head: "HEAD",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, unknown, err := parseDiffFlags(test.args)
			if err != nil {
				t.Fatalf("parseDiffFlags(%q): %v", test.args, err)
			}
			if parsed.base != test.base || parsed.head != test.head {
				t.Errorf("base/head = %q/%q, want %q/%q", parsed.base, parsed.head, test.base, test.head)
			}
			if !reflect.DeepEqual(parsed.paths, test.paths) {
				t.Errorf("paths = %#v, want %#v", parsed.paths, test.paths)
			}
			if !reflect.DeepEqual(unknown, test.unknown) {
				t.Errorf("unknown = %#v, want %#v", unknown, test.unknown)
			}
			if parsed.common.JSON != test.json {
				t.Errorf("JSON = %v, want %v", parsed.common.JSON, test.json)
			}
		})
	}
}

func TestParseDiffFlagsRequiresRevisionValues(t *testing.T) {
	for _, flag := range []string{"--base", "--head"} {
		if _, _, err := parseDiffFlags([]string{flag}); err == nil {
			t.Errorf("parseDiffFlags(%q) succeeded, want a requires-a-value error", flag)
		}
	}
}

// TestDiffRejectsUnknownFlag is the end-to-end half: before parseDiffFlags existed this exited 0
// and printed an empty change list, so a mistyped flag looked like a clean diff.
func TestDiffRejectsUnknownFlag(t *testing.T) {
	repo := t.TempDir()
	var out, errOut bytes.Buffer
	opts := Options{Stdout: &out, Stderr: &errOut, Version: "test-version"}
	err := runDiff(t.Context(), opts, []string{"--repo", repo, "--jsonn"})
	if err == nil {
		t.Fatal("runDiff accepted --jsonn; a typo'd flag must not be filed as a path")
	}
	if !strings.Contains(err.Error(), "--jsonn") {
		t.Errorf("error %q does not name the offending flag", err)
	}
}

// TestParseDiffFlagsRejectsOptionShapedRevision is the unit half of the argument-injection
// fix (CWE-88). --base/--head values used to be copied into `git diff`'s argv verbatim, so a
// value beginning with '-' stopped being a revision and became an option of git itself.
func TestParseDiffFlagsRejectsOptionShapedRevision(t *testing.T) {
	for _, args := range [][]string{
		{"--base", "--output=/tmp/entire-graph-victim", "--head", "HEAD"},
		{"--base", "HEAD~1", "--head", "--output=/tmp/entire-graph-victim"},
		{"--base", "-"},
		{"--head", ""},
	} {
		if _, _, err := parseDiffFlags(args); err == nil {
			t.Errorf("parseDiffFlags(%q) accepted an option-shaped revision, want an error", args)
		}
	}
	// Ordinary path-shaped refs with a single leading hyphen are valid Git revisions.
	if _, _, err := parseDiffFlags([]string{"--base", "-foo", "--head", "HEAD"}); err != nil {
		t.Errorf("parseDiffFlags([--base -foo --head HEAD]) = %v, want nil (-foo is a valid ref)", err)
	}
}

// TestOptionShapedRevisionCannotWriteFiles is the end-to-end half. Before the fix,
//
//	entire graph diff --repo . --base '--output=FILE' --head HEAD
//
// exited 0 and left FILE truncated and replaced by git's own `-z --name-status` output,
// because git parses options anywhere ahead of `--`. The `commit` verb had the same reach:
// `git rev-parse '--output=FILE^'` does not fail, so FirstParent let the value through to the
// same `git diff` argv. The revision here is an ordinary path with a '-' prefix, so nothing
// about the fixture is platform-specific.
func TestOptionShapedRevisionCannotWriteFiles(t *testing.T) {
	repo := twoCommitRepo(t)
	const secret = "victim contents that must survive\n"
	victim := filepath.Join(t.TempDir(), "victim.txt")

	tests := []struct {
		name string
		args func(target string) []string
	}{
		{"diff --base", func(target string) []string {
			return []string{"diff", "--repo", repo, "--base", "--output=" + target, "--head", "HEAD"}
		}},
		{"diff --head", func(target string) []string {
			return []string{"diff", "--repo", repo, "--base", "HEAD~1", "--head", "--output=" + target}
		}},
		{"analyze --base", func(target string) []string {
			return []string{"analyze", "--repo", repo, "--base", "--output=" + target}
		}},
		{"commit revision", func(target string) []string {
			return []string{"commit", "--repo", repo, "--output=" + target}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(victim, []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			err := Run(t.Context(), Options{Stdout: &out, Stderr: &errOut, Version: "test-version"}, test.args(victim))
			if err == nil {
				t.Errorf("%s accepted an option-shaped revision; stdout:\n%s", test.name, out.String())
			} else if !strings.Contains(err.Error(), "revision") {
				t.Errorf("error %q does not explain that the value is not a revision", err)
			}
			got, readErr := os.ReadFile(victim)
			if readErr != nil {
				t.Fatalf("read victim file: %v", readErr)
			}
			if string(got) != secret {
				t.Fatalf("%s let git rewrite a file outside the repository; victim now holds %q", test.name, string(got))
			}
		})
	}
}

// TestDiffAcceptsOrdinaryRevisions is the over-rejection guard for the same fix: every shape
// of revision a caller legitimately passes must still resolve.
func TestDiffAcceptsOrdinaryRevisions(t *testing.T) {
	repo := twoCommitRepo(t)
	git(t, repo, "branch", "feature", "HEAD~1")
	git(t, repo, "tag", "-a", "v1", "-m", "release", "HEAD~1")
	full := rev(t, repo, "HEAD~1")

	for _, base := range []string{"HEAD~1", "feature", "v1", full, full[:7]} {
		var out, errOut bytes.Buffer
		err := Run(t.Context(), Options{Stdout: &out, Stderr: &errOut, Version: "test-version"},
			[]string{"diff", "--repo", repo, "--base", base, "--head", "HEAD", "--json"})
		if err != nil {
			t.Errorf("diff --base %q: %v\n%s", base, err, errOut.String())
			continue
		}
		if !strings.Contains(out.String(), "\"files\"") {
			t.Errorf("diff --base %q produced no result payload:\n%s", base, out.String())
		}
	}
}

// twoCommitRepo builds a git repository whose HEAD~1..HEAD range contains one real semantic
// change, so a diff over it exercises the analysis rather than an empty file list.
func twoCommitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	// An explicit initial branch, not whatever the user's global
	// init.defaultBranch names: this fixture creates a branch literally
	// called "feature", and `git branch feature HEAD~1` fails with "a branch
	// named 'feature' already exists" whenever init.defaultBranch=feature.
	git(t, repo, "init", "-b", "twocommitrepo-trunk")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	// A developer's global signing config would otherwise fail this fixture with
	// "gpg failed to sign the data", the same class of global-config dependence
	// as the init.defaultBranch collision above. Both keys are needed and they
	// are independent: commit.gpgsign covers the two commits below, and
	// tag.gpgSign covers the annotated tag TestDiffAcceptsOrdinaryRevisions
	// creates in this repository ("git tag -a" signs under its own key, so
	// pinning only commit.gpgsign still aborts with "unable to sign the tag").
	git(t, repo, "config", "commit.gpgsign", "false")
	git(t, repo, "config", "tag.gpgSign", "false")
	write(t, repo, "a.go", "package main\n\nfunc validate() bool { return true }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "one")
	write(t, repo, "a.go", "package main\n\nfunc validate() bool { return true }\n\nfunc audit() bool { return true }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "two")
	return repo
}
