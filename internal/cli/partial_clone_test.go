package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/sem"
)

// partialClone is a clone that Git treats exactly like a `--filter=blob:none`
// clone: origin is a blob:none promisor remote and some blobs are absent.
// It is built by cloning normally and repacking without those blobs, because
// a local upload-pack may ignore the filter and send every blob anyway, which
// would leave nothing to lazily fetch.
type partialClone struct {
	src, clone string
	missing    map[string]string // path in its commit -> blob id absent from the clone
}

// fixtureGit runs a fixture command with lazy fetching disabled, so building
// or inspecting the fixture can never repair it by fetching a missing blob.
func fixtureGit(t *testing.T, dir string, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_NO_LAZY_FETCH=1")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newPartialClone builds src with two commits. HEAD renames old.go to beta.go
// and edits it, and edits a.go, so HEAD's parent diff needs the parent's blobs
// for rename detection, as any real history does. The clone lacks both parent
// blobs; withMissingHeadBlob also drops HEAD's gone.go, a file a --head index
// has to read.
func newPartialClone(t *testing.T, withMissingHeadBlob bool) partialClone {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, src, "", "init", "-q")
	fixtureGit(t, src, "", "config", "user.name", "Entire Graph Test")
	fixtureGit(t, src, "", "config", "user.email", "graph@example.com")
	write(t, src, "a.go", "package pkg\n\nfunc Alpha() int { return Beta() }\n")
	write(t, src, "old.go", "package pkg\n\nfunc Beta() int { return 1 }\n\nfunc Extra() int { return 2 }\n")
	fixtureGit(t, src, "", "add", ".")
	fixtureGit(t, src, "", "commit", "-q", "-m", "one")
	missing := map[string]string{
		"HEAD~1:a.go":   fixtureGit(t, src, "", "rev-parse", "HEAD:a.go"),
		"HEAD~1:old.go": fixtureGit(t, src, "", "rev-parse", "HEAD:old.go"),
	}
	write(t, src, "a.go", "package pkg\n\nfunc Alpha() int { return Beta() + 1 }\n")
	fixtureGit(t, src, "", "mv", "old.go", "beta.go")
	write(t, src, "beta.go", "package pkg\n\nfunc Beta() int { return 3 }\n\nfunc Extra() int { return 2 }\n")
	write(t, src, "gone.go", "package pkg\n\nfunc Gone() int { return Alpha() }\n")
	fixtureGit(t, src, "", "add", "-A")
	fixtureGit(t, src, "", "commit", "-q", "-m", "two")
	if withMissingHeadBlob {
		missing["HEAD:gone.go"] = fixtureGit(t, src, "", "rev-parse", "HEAD:gone.go")
	}

	clone := filepath.Join(base, "clone")
	fixtureGit(t, base, "", "clone", "-q", "--no-local", src, clone)
	absent := map[string]bool{}
	for _, oid := range missing {
		absent[oid] = true
	}
	var keep strings.Builder
	for _, line := range strings.Split(fixtureGit(t, clone, "", "rev-list", "--objects", "--all"), "\n") {
		oid, _, _ := strings.Cut(line, " ")
		if oid != "" && !absent[oid] {
			keep.WriteString(oid + "\n")
		}
	}
	packDir := filepath.Join(clone, ".git", "objects", "pack")
	staging := t.TempDir()
	fixtureGit(t, clone, keep.String(), "pack-objects", "-q", filepath.Join(staging, "pack"))
	old, err := filepath.Glob(filepath.Join(packDir, "pack-*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range old {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := filepath.Glob(filepath.Join(staging, "pack-*"))
	if err != nil || len(fresh) == 0 {
		t.Fatalf("pack-objects wrote no pack: %v", err)
	}
	for _, path := range fresh {
		if err := os.Rename(path, filepath.Join(packDir, filepath.Base(path))); err != nil {
			t.Fatal(err)
		}
	}
	for oid := range absent {
		if err := os.Remove(filepath.Join(clone, ".git", "objects", oid[:2], oid[2:])); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	fixtureGit(t, clone, "", "config", "remote.origin.promisor", "true")
	fixtureGit(t, clone, "", "config", "remote.origin.partialclonefilter", "blob:none")
	fc := partialClone{src: src, clone: clone, missing: missing}
	fc.assertMissing(t, "fixture")
	return fc
}

// assertMissing proves every withheld blob is still absent: nothing fetched it.
func (fc partialClone) assertMissing(t *testing.T, stage string) {
	t.Helper()
	listed := fixtureGit(t, fc.clone, "", "rev-list", "--objects", "--all", "--missing=print")
	for path, oid := range fc.missing {
		if !strings.Contains(listed, "?"+oid) {
			t.Fatalf("%s: blob %s (%s) is present in the partial clone; it was fetched or never withheld", stage, oid, path)
		}
	}
}

// requireGitHonoursNoLazyFetch skips on a Git older than 2.45, where the
// provider refuses partial clones (TestIndexHeadRefusesPartialCloneOnGitBefore245
// covers that branch with a stand-in Git).
func requireGitHonoursNoLazyFetch(t *testing.T) {
	t.Helper()
	if status := gitutil.LazyFetchGuard(); !status.Effective {
		t.Skipf("the Git on PATH (%q) predates Git %s and ignores GIT_NO_LAZY_FETCH", status.Version, gitutil.LazyFetchGuardMinimumVersion)
	}
}

func runPartialCloneCommand(t *testing.T, repo, cacheDir string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(t.Context(), Options{
		Version: "test-version",
		Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
		Stdout:  &out,
		Stderr:  &bytes.Buffer{},
	}, args)
	return out.String(), err
}

func indexPartialClone(t *testing.T, repo, cacheDir string) indexResponse {
	t.Helper()
	raw, err := runPartialCloneCommand(t, repo, cacheDir, "index", "--repo", repo, "--head", "--format", "json", "--cache-dir", cacheDir)
	if err != nil {
		t.Fatalf("index --head on a partial clone: %v", err)
	}
	var response indexResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatalf("decode index response %q: %v", raw, err)
	}
	return response
}

func warningCodes(warnings []sem.ProviderWarning) []string {
	codes := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		codes = append(codes, warning.Code)
	}
	return codes
}

// (a) A blob:none clone whose remote no longer exists indexes its committed
// HEAD completely and offline. The only blobs it lacks are history blobs,
// which is the normal state of a blob:none clone after checkout.
func TestIndexHeadIndexesAPartialCloneOffline(t *testing.T) {
	requireGitHonoursNoLazyFetch(t)
	fc := newPartialClone(t, false)
	if err := os.RemoveAll(fc.src); err != nil {
		t.Fatal(err)
	}

	response := indexPartialClone(t, fc.clone, t.TempDir())
	if response.Commit != fixtureGit(t, fc.clone, "", "rev-parse", "HEAD") {
		t.Fatalf("index commit = %q, want the clone's HEAD", response.Commit)
	}
	if response.Counts.Files != 3 || len(response.PartialFailures) != 0 {
		t.Fatalf("partial clone index counts = %#v, partial failures %#v; want all 3 HEAD files and none failed",
			response.Counts, response.PartialFailures)
	}
	for _, code := range warningCodes(response.Warnings) {
		if code == "E_NO_GIT_HEAD" || code == "W_GIT_WORKTREE_FALLBACK" {
			t.Fatalf("index fell back to the working tree: %v", warningCodes(response.Warnings))
		}
	}
	fc.assertMissing(t, "after index")
}

// (b) A blob the clone lacks is an unreadable file, never a fetch. The
// promisor remote is still reachable here, so a lazy fetch would succeed if
// one were started. GIT_EXEC_PATH points Git's child commands at a spy `git`
// that records any invocation: the lazy fetch is a child `git fetch`, so an
// empty log means no fetch was attempted -- stronger than "attempted and
// blocked by GIT_ALLOW_PROTOCOL".
func TestPartialCloneMissingBlobIsAnErrorNotALazyFetch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the spy Git is a POSIX shell script")
	}
	requireGitHonoursNoLazyFetch(t)
	fc := newPartialClone(t, true)

	spyDir := t.TempDir()
	spyLog := filepath.Join(t.TempDir(), "child-git.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(spyLog) + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(spyDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	spied := func() string {
		data, err := os.ReadFile(spyLog)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(data)
	}

	// Control: the spy sees a lazy fetch when GIT_NO_LAZY_FETCH is absent,
	// even with every transport denied, so its silence below means something.
	control := exec.Command("git", "cat-file", "-p", fc.missing["HEAD:gone.go"])
	control.Dir = fc.clone
	control.Env = append(control.Environ(), "GIT_EXEC_PATH="+spyDir, "GIT_ALLOW_PROTOCOL=")
	control.Env = withoutEnv(control.Env, "GIT_NO_LAZY_FETCH")
	_ = control.Run()
	if !strings.Contains(spied(), "fetch") {
		t.Fatalf("control: the spy did not observe the lazy fetch Git starts without GIT_NO_LAZY_FETCH; log %q", spied())
	}
	if err := os.Remove(spyLog); err != nil {
		t.Fatal(err)
	}
	fc.assertMissing(t, "after control")

	t.Setenv("GIT_EXEC_PATH", spyDir)
	response := indexPartialClone(t, fc.clone, t.TempDir())
	if log := spied(); log != "" {
		t.Fatalf("indexing started Git child commands (a lazy fetch) for the missing blob:\n%s", log)
	}
	fc.assertMissing(t, "after index")
	var unreadable []string
	for _, failure := range response.PartialFailures {
		if failure.Code == "E_FILE_READ" {
			unreadable = append(unreadable, failure.FilePath)
		}
	}
	if len(unreadable) != 1 || unreadable[0] != "gone.go" {
		t.Fatalf("partial failures = %#v, want exactly gone.go reported unreadable", response.PartialFailures)
	}
	if response.Counts.Files != 2 {
		t.Fatalf("files = %d, want the 2 readable HEAD files indexed beside the unreadable one", response.Counts.Files)
	}
}

func withoutEnv(env []string, key string) []string {
	kept := env[:0:0]
	for _, entry := range env {
		if name, _, _ := strings.Cut(entry, "="); !strings.EqualFold(name, key) {
			kept = append(kept, entry)
		}
	}
	return kept
}

// writeGitShim puts a POSIX `git` first on PATH. It answers `git version` with
// version when that is non-empty and runs the real Git for everything else,
// logging each call's no-egress guards and argv when log is non-empty.
func writeGitShim(t *testing.T, version, log string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	if log != "" {
		script.WriteString("printf '%s|%s|%s\\n' \"${GIT_NO_LAZY_FETCH-<unset>}\" \"${GIT_ALLOW_PROTOCOL-<unset>}\" \"$*\" >> " + strconv.Quote(log) + "\n")
	}
	if version != "" {
		script.WriteString("if [ \"$1\" = version ]; then echo 'git version " + version + "'; exit 0; fi\n")
	}
	script.WriteString("exec " + strconv.Quote(realGit) + " \"$@\"\n")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// (c) On a Git that ignores GIT_NO_LAZY_FETCH the refusal stays, and its text
// is true for index --head, which has no filesystem fallback.
func TestIndexHeadRefusesPartialCloneOnGitBefore245(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in Git is a POSIX shell script")
	}
	fc := newPartialClone(t, false)
	writeGitShim(t, "2.44.0", "")

	cacheDir := t.TempDir()
	_, err := runPartialCloneCommand(t, fc.clone, cacheDir, "index", "--repo", fc.clone, "--head", "--format", "json", "--cache-dir", cacheDir)
	if err == nil {
		t.Fatal("index --head admitted a partial clone on Git 2.44, which ignores GIT_NO_LAZY_FETCH")
	}
	message := err.Error()
	for _, want := range []string{
		"refuse Git subprocesses for unsafe or unreadable repository metadata",
		"declares a promisor remote (a partial clone), and the Git on PATH (2.44.0) predates Git 2.45",
		"Git subprocesses are refused for this repository",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal %q lacks %q", message, want)
		}
	}
	if strings.Contains(message, "falls back") {
		t.Errorf("refusal %q promises a fallback index --head never reaches", message)
	}
	fc.assertMissing(t, "after refusal")
}

// (d) N2: a query on a partial clone used to fall back to an uncached
// working-tree build on every call. It now resolves HEAD and reuses the
// committed-tree cache.
func TestQueryHeadReusesTheCacheOnAPartialClone(t *testing.T) {
	requireGitHonoursNoLazyFetch(t)
	fc := newPartialClone(t, false)
	if err := os.RemoveAll(fc.src); err != nil {
		t.Fatal(err)
	}
	type queryResponse struct {
		Commit string `json:"commit"`
		Stats  struct {
			IndexCacheHit bool `json:"index_cache_hit"`
		} `json:"stats"`
		Warnings []sem.ProviderWarning `json:"warnings"`
	}
	cacheDir := t.TempDir()
	query := func() queryResponse {
		t.Helper()
		raw, err := runPartialCloneCommand(t, fc.clone, cacheDir, "query", "--repo", fc.clone, "--head", "--format", "json", "--cache-dir", cacheDir, "--query", "Alpha returns Beta")
		if err != nil {
			t.Fatalf("query --head on a partial clone: %v", err)
		}
		var response queryResponse
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatalf("decode query response %q: %v", raw, err)
		}
		for _, code := range warningCodes(response.Warnings) {
			if code == "E_NO_GIT_HEAD" {
				t.Fatalf("query fell back to the working tree: %v", warningCodes(response.Warnings))
			}
		}
		return response
	}
	first, second := query(), query()
	if first.Commit == "" || first.Stats.IndexCacheHit {
		t.Fatalf("first query = commit %q cache hit %v, want a committed-tree build", first.Commit, first.Stats.IndexCacheHit)
	}
	if !second.Stats.IndexCacheHit {
		t.Fatal("second query on a partial clone did not hit the committed-tree cache")
	}
	fc.assertMissing(t, "after queries")
}

// Every Git process the provider starts must carry both no-egress guards:
// GIT_NO_LAZY_FETCH=1 (no fetch is ever started) and an empty
// GIT_ALLOW_PROTOCOL (no transport could run if one were). A logging `git`
// first on PATH records the environment of each real invocation across the
// index, query, snapshot, relation, analysis and store commands.
// TestEveryGitSubprocessIsBuiltByTheGuardedConstructors is the static half.
func TestEveryGitInvocationCarriesTheNoEgressGuards(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the logging Git is a POSIX shell script")
	}
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "a.go", "package pkg\n\nfunc Alpha() int { return Beta() }\n")
	write(t, repo, "b.go", "package pkg\n\nfunc Beta() int { return 1 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "one")
	write(t, repo, "b.go", "package pkg\n\nfunc Beta() int { return 2 }\n")
	git(t, repo, "commit", "-q", "-am", "two")
	head := rev(t, repo, "HEAD") // before the shim: fixture commands are not under test

	log := filepath.Join(t.TempDir(), "git.log")
	writeGitShim(t, "", log)
	cacheDir := t.TempDir()
	for _, args := range [][]string{
		{"index", "--repo", repo, "--head", "--cache-dir", cacheDir},
		{"query", "--repo", repo, "--head", "--cache-dir", cacheDir, "--query", "Alpha"},
		{"query", "--repo", repo, "--cache-dir", cacheDir, "--query", "Alpha"},
		{"snapshot", "--repo", repo, "--format", "ndjson"},
		{"impact", "--repo", repo, "--head", "--cache-dir", cacheDir, "--symbol", "Beta"},
		{"neighbors", "--repo", repo, "--head", "--cache-dir", cacheDir, "--symbol", "Beta", "--direction", "in"},
		{"def", "--repo", repo, "--head", "--cache-dir", cacheDir, "Beta"},
		{"diff", "--repo", repo, "--base", "HEAD~1", "--head", "HEAD", "--json"},
		{"commit", "--repo", repo, "--json", "HEAD"},
		{"store", "--repo", repo, "--cache-dir", cacheDir},
		{"health", "--repo", repo, "--cache-dir", cacheDir},
	} {
		if _, err := runPartialCloneCommand(t, repo, cacheDir, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	// git grep runs through the caller-locale constructor; drive it directly.
	if _, err := gitutil.GrepTreePaths(t.Context(), repo, head, []string{"Beta"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	subcommands := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		noLazyFetch, rest, _ := strings.Cut(line, "|")
		allowProtocol, argv, _ := strings.Cut(rest, "|")
		if noLazyFetch != "1" || allowProtocol != "" {
			t.Errorf("git %s ran with GIT_NO_LAZY_FETCH=%q GIT_ALLOW_PROTOCOL=%q, want 1 and empty", argv, noLazyFetch, allowProtocol)
		}
		subcommands[gitSubcommand(argv)]++
	}
	for _, want := range []string{"rev-parse", "log", "ls-files", "ls-tree", "cat-file", "diff", "grep", "config"} {
		if subcommands[want] == 0 {
			t.Errorf("no `git %s` was observed (saw %v); the sweep no longer covers it", want, subcommands)
		}
	}
}

// gitSubcommand returns the first argv word that is not a global option.
func gitSubcommand(argv string) string {
	fields := strings.Fields(argv)
	for index := 0; index < len(fields); index++ {
		field := fields[index]
		if field == "-c" || field == "-C" {
			index++
			continue
		}
		if !strings.HasPrefix(field, "-") {
			return field
		}
	}
	return ""
}
