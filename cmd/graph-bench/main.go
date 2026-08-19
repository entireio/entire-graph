// Command graph-bench clones popular repositories per language and measures the
// semantic provider over them, emitting a machine-readable performance and
// quality report. Cloning (network) is a distinct phase from measurement, which
// runs the provider with NoNetwork so the measured path stays no-egress.
//
// Usage:
//
//	go run ./cmd/graph-bench -update-lock          # resolve and pin repo commits
//	go run ./cmd/graph-bench                        # full run using the lock file
//	go run ./cmd/graph-bench -languages Go,Rust -limit 3
//	go run ./cmd/graph-bench -skip-clone            # offline: measure existing clones
//
// Cloned repositories live under -cache (gitignored) and never enter our own
// commits. Pinning is via bench/repos.lock.json: commit it to make runs
// reproducible across work phases.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/entireio/entire-graph/internal/bench"
	"github.com/entireio/entire-graph/internal/sem"
)

type manifest struct {
	Languages map[string][]string `json:"languages"`
}

type repoSpec struct {
	language string
	repoPath string // owner/name
	ref      string // optional manifest-pinned ref
}

func parseProfile(value string) (sem.Profile, error) {
	switch value {
	case "", "full":
		return sem.ProfileFull, nil
	case "fast":
		return sem.ProfileFast, nil
	case "syntax-only":
		return sem.ProfileSyntaxOnly, nil
	default:
		return "", fmt.Errorf("unknown -profile %q (want full, fast, or syntax-only)", value)
	}
}

func (r repoSpec) cloneURL() string { return "https://github.com/" + r.repoPath + ".git" }
func (r repoSpec) dirName() string  { return strings.ReplaceAll(r.repoPath, "/", "__") }

func main() {
	var (
		manifestPath  = flag.String("manifest", "bench/repos.json", "path to the repo manifest")
		cacheDir      = flag.String("cache", "bench/.cache", "directory for cloned repos (gitignored)")
		outDir        = flag.String("out", "bench/results", "directory for the JSON report, or - for stdout")
		lockPath      = flag.String("lock", "bench/repos.lock.json", "path to the commit lock file")
		languages     = flag.String("languages", "", "comma-separated language filter (default: all)")
		limit         = flag.Int("limit", 0, "max repos per language (0 = all)")
		jobs          = flag.Int("jobs", 4, "concurrent clone jobs")
		depth         = flag.Int("depth", 1, "git clone depth")
		skipClone     = flag.Bool("skip-clone", false, "do not clone; measure repos already in cache")
		updateLock    = flag.Bool("update-lock", false, "resolve current commits and rewrite the lock file")
		providerVer   = flag.String("provider-version", "dev", "provider version label recorded in the report")
		profile       = flag.String("profile", "full", "indexing profile to measure: full, fast, or syntax-only")
		progress      = flag.Bool("progress", false, "print provider phase progress to stderr")
		minLOCPerSec  = flag.Float64("min-loc-per-sec", 0, "fail if successful aggregate LOC/s is below this floor")
		maxRSSBytes   = flag.Uint64("max-rss-bytes", 0, "fail if any repository cold peak RSS exceeds this ceiling")
		exactOutput   = flag.Bool("exact-output-bytes", false, "marshal every streamed record for exact NDJSON output bytes; slower on large repos")
		cpuProfile    = flag.String("cpuprofile", "", "unsupported with mandatory isolated measurement workers")
		measureWorker = flag.Bool("measure-worker", false, "serve one isolated measurement request on stdin")
	)
	flag.Parse()
	if *measureWorker {
		os.Exit(bench.RunMeasureWorker(context.Background(), os.Stdin, os.Stdout))
	}
	if err := validateExecutionMode(*cpuProfile); err != nil {
		fmt.Fprintln(os.Stderr, "graph-bench:", err)
		os.Exit(1)
	}

	if err := run(*manifestPath, *cacheDir, *outDir, *lockPath, *languages, *profile, *limit, *jobs, *depth, *skipClone, *updateLock, *providerVer, *progress, *minLOCPerSec, *maxRSSBytes, *exactOutput); err != nil {
		fmt.Fprintln(os.Stderr, "graph-bench:", err)
		os.Exit(1)
	}
}

func validateExecutionMode(cpuProfile string) error {
	if strings.TrimSpace(cpuProfile) != "" {
		return fmt.Errorf("-cpuprofile is not supported with mandatory isolated measurement workers; parent-only profiles would omit provider work")
	}
	return nil
}

func run(manifestPath, cacheDir, outDir, lockPath, languages, profileName string, limit, jobs, depth int, skipClone, updateLock bool, providerVer string, progress bool, minLOCPerSec float64, maxRSSBytes uint64, exactOutputBytes bool) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve graph-bench executable: %w", err)
	}
	return runWithWorkerCommand(manifestPath, cacheDir, outDir, lockPath, languages, profileName, limit, jobs, depth, skipClone, updateLock, providerVer, progress, minLOCPerSec, maxRSSBytes, exactOutputBytes, []string{executable, "-measure-worker"})
}

func runWithWorkerCommand(manifestPath, cacheDir, outDir, lockPath, languages, profileName string, limit, jobs, depth int, skipClone, updateLock bool, providerVer string, progress bool, minLOCPerSec float64, maxRSSBytes uint64, exactOutputBytes bool, workerCommand []string) error {
	profile, err := parseProfile(profileName)
	if err != nil {
		return err
	}
	specs, err := loadSpecs(manifestPath, languages, limit)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return fmt.Errorf("no repositories selected")
	}
	lock, err := loadLock(lockPath)
	if err != nil {
		return err
	}

	ctx := context.Background()
	resolved := map[string]string{} // repoPath -> sha
	var resolvedMu sync.Mutex

	if !skipClone {
		fmt.Fprintf(os.Stderr, "Cloning %d repositories into %s...\n", len(specs), cacheDir)
		cloneAll(ctx, specs, cacheDir, lock, depth, updateLock, jobs, func(repoPath, sha string) {
			resolvedMu.Lock()
			resolved[repoPath] = sha
			resolvedMu.Unlock()
		})
		if updateLock {
			for k, v := range resolved {
				lock[k] = v
			}
			if err := writeLock(lockPath, lock); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Wrote %d pinned commits to %s\n", len(lock), lockPath)
		}
	}

	fmt.Fprintf(os.Stderr, "Measuring (no-egress, profile=%s)...\n", profile)
	var metrics []bench.RepoMetrics
	for _, spec := range specs {
		dir := filepath.Join(cacheDir, spec.language, spec.dirName())
		if _, statErr := os.Stat(dir); statErr != nil {
			metrics = append(metrics, bench.RepoMetrics{Name: spec.repoPath, Language: spec.language, Profile: string(profile), Error: "not cloned"})
			fmt.Fprintf(os.Stderr, "  skip %-40s (not cloned)\n", spec.repoPath)
			continue
		}
		opts := bench.MeasureOptions{MaxRSSBytes: maxRSSBytes, ExactOutputBytes: exactOutputBytes}
		if progress {
			opts.Progress = func(event sem.ProgressEvent) {
				fmt.Fprint(os.Stderr, formatProgress(spec.repoPath, event))
			}
		}
		m, measureErr := bench.MeasureRepoIsolated(ctx, spec.repoPath, spec.language, dir, providerVer, profile, opts, workerCommand)
		if measureErr != nil {
			fmt.Fprintf(os.Stderr, "  FAIL %-40s %v\n", spec.repoPath, measureErr)
		} else {
			fmt.Fprintf(os.Stderr, "  ok   %-40s %6d files  %8d LOC  %7.0f LOC/s\n", spec.repoPath, m.Files, m.LOC, m.LOCPerSec)
		}
		metrics = append(metrics, m)
	}

	report := bench.BuildReport(time.Now().UTC().Format(time.RFC3339), providerVer, profile, metrics)
	if err := emitReport(report, outDir); err != nil {
		return err
	}
	printSummary(report)
	if minLOCPerSec > 0 && report.Totals.LOCPerSec < minLOCPerSec {
		return fmt.Errorf("performance guardrail failed: total LOC/s %.2f below floor %.2f", report.Totals.LOCPerSec, minLOCPerSec)
	}
	maxObservedRSS := uint64(0)
	for _, metric := range metrics {
		if metric.MaxRSSBytes > maxObservedRSS {
			maxObservedRSS = metric.MaxRSSBytes
		}
	}
	if maxRSSBytes > 0 && maxObservedRSS > maxRSSBytes {
		return fmt.Errorf("memory guardrail failed: max cold RSS %d exceeds ceiling %d", maxObservedRSS, maxRSSBytes)
	}
	return nil
}

func formatProgress(repoPath string, event sem.ProgressEvent) string {
	return fmt.Sprintf("  progress %-40s phase=%s files=%d/%d symbols=%d relations=%d heap=%d rss=%d phase_elapsed=%s elapsed=%s\n",
		repoPath,
		event.Phase,
		event.FilesDone,
		event.FilesTotal,
		event.Symbols,
		event.Relations,
		event.HeapAlloc,
		event.MaxRSSBytes,
		event.PhaseElapsed.Round(time.Millisecond),
		event.Elapsed.Round(time.Millisecond),
	)
}

func loadSpecs(manifestPath, languages string, limit int) ([]repoSpec, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	var filter map[string]bool
	if strings.TrimSpace(languages) != "" {
		filter = map[string]bool{}
		for _, language := range strings.Split(languages, ",") {
			filter[strings.TrimSpace(language)] = true
		}
	}

	langNames := make([]string, 0, len(m.Languages))
	for language := range m.Languages {
		langNames = append(langNames, language)
	}
	sort.Strings(langNames)

	var specs []repoSpec
	for _, language := range langNames {
		if filter != nil && !filter[language] {
			continue
		}
		entries := m.Languages[language]
		if limit > 0 && len(entries) > limit {
			entries = entries[:limit]
		}
		for _, entry := range entries {
			repoPath, ref := entry, ""
			if at := strings.LastIndex(entry, "@"); at > 0 {
				repoPath, ref = entry[:at], entry[at+1:]
			}
			specs = append(specs, repoSpec{language: language, repoPath: repoPath, ref: ref})
		}
	}
	return specs, nil
}

func cloneAll(ctx context.Context, specs []repoSpec, cacheDir string, lock map[string]string, depth int, updateLock bool, jobs int, record func(repoPath, sha string)) {
	if jobs < 1 {
		jobs = 1
	}
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for _, spec := range specs {
		wg.Add(1)
		sem <- struct{}{}
		go func(spec repoSpec) {
			defer wg.Done()
			defer func() { <-sem }()
			ref := spec.ref
			if !updateLock {
				if pinned, ok := lock[spec.repoPath]; ok && pinned != "" {
					ref = pinned
				}
			}
			dir := filepath.Join(cacheDir, spec.language, spec.dirName())
			sha, err := ensureRepo(ctx, spec.cloneURL(), ref, dir, depth)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  clone FAIL %-40s %v\n", spec.repoPath, err)
				return
			}
			record(spec.repoPath, sha)
		}(spec)
	}
	wg.Wait()
}

// validateRef rejects refs Git would parse as command-line options or as a
// fetch refspec.
//
// Git's parse-options permutes arguments, so a value in a positional slot is
// still parsed as an option if it looks like one. ensureRepo puts the ref in a
// positional slot of git fetch and git checkout, so an option-shaped ref was
// applied as an option instead of being fetched: the fetch error is discarded
// here, and a ref such as --detach is then accepted by the checkout, leaving
// the cached repo on something other than the requested commit while the run
// reports success.
//
// The classic escalation of this shape, --upload-pack=<cmd>, executes <cmd>
// only when the remote uses a transport that runs upload-pack on this machine
// (a filesystem path or file:// URL). cloneURL always builds an https URL, for
// which Git ignores the option, so the reachable impact here is a wrong
// checkout rather than command execution.
//
// The same slot is also a *refspec* slot. `git fetch origin <ref>` treats
// `+refs/heads/evil:refs/heads/injected` as a write: the fetch succeeds and
// creates refs/heads/injected inside the cached clone, only the checkout of the
// literal string fails, and the FETCH_HEAD fallback below then reports success
// for whatever that refspec happened to fetch (verified against git 2.54.0:
// `ls .git/refs/heads` gains `injected`, and HEAD lands on the evil branch tip
// while ensureRepo returns a nil error). A wildcard refspec such as
// `refs/heads/*:refs/remotes/origin/*` does the same for every branch at once.
// Git ref names cannot contain `:` or `*` (git check-ref-format), and a leading
// `+` is meaningful only as a refspec's force marker, so rejecting those three
// costs no legitimate ref and leaves `git fetch origin <ref>` able to fetch
// exactly one ref -- which is what makes FETCH_HEAD trustworthy afterwards.
//
// Refs reach ensureRepo from two ordinary repo files -- the manifest
// (`owner/name@<ref>`) and the commit lock -- so their contents are argv input
// to validate, not trusted configuration. Mirrors the leading-dash/NUL guards
// in internal/gitutil (git.go:168, :237, :419).
func validateRef(ref string) error {
	if strings.HasPrefix(ref, "-") || strings.ContainsRune(ref, '\x00') {
		return fmt.Errorf("invalid git ref %q", ref)
	}
	if strings.HasPrefix(ref, "+") || strings.ContainsAny(ref, ":*") {
		return fmt.Errorf("invalid git ref %q: refspec syntax is not a ref", ref)
	}
	return nil
}

// gitEndOfOptions returns the --end-of-options argument when the git on PATH
// understands it, and nothing when it does not.
//
// parse-options learned --end-of-options in Git 2.24 (2019-11). On 2.23 and
// earlier every invocation carrying it dies with "unknown option", which would
// break every benchmark clone, including ordinary trusted refs, on those
// versions. So the flag is defence in depth that is applied only where it
// exists; validateRef is the guard that always runs and is what actually stops
// an option-shaped ref.
//
// `--` is not a substitute. For git checkout it means "everything after this is
// a pathspec", so `git checkout --quiet -- main` looks for a *file* named main
// and fails with "pathspec 'main' did not match any file(s) known to git"
// (verified against git 2.54.0). --end-of-options exists precisely because `--`
// already has that meaning.
func gitEndOfOptions(ctx context.Context) []string {
	out, err := runGit(ctx, "", "version")
	if err != nil || !gitVersionHasEndOfOptions(out) {
		return nil
	}
	return []string{"--end-of-options"}
}

// gitVersionHasEndOfOptions reports whether `git version <v>` output names a Git
// that has --end-of-options, i.e. 2.24 or newer. It tolerates the suffixed forms
// distributions ship, such as "git version 2.39.5 (Apple Git-154)" and
// "git version 2.45.2.windows.1", and treats anything it cannot parse as too old
// so an unrecognised git still gets a working command line.
func gitVersionHasEndOfOptions(out string) bool {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return false
	}
	parts := strings.Split(fields[2], ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return major > 2 || (major == 2 && minor >= 24)
}

func ensureRepo(ctx context.Context, url, ref, dir string, depth int) (string, error) {
	if err := validateRef(ref); err != nil {
		return "", err
	}
	endOfOptions := gitEndOfOptions(ctx)
	// looksLikeSHA only guesses, and a lowercase-hex string can name an object
	// *and* a branch in the same repository, pointing at different commits. Ask
	// the remote which it actually publishes, so the clone and the checkout
	// cannot resolve the same ref two different ways.
	remoteRef := ""
	if ref != "" && looksLikeSHA(ref) {
		remoteRef = remoteRefFor(ctx, url, ref, endOfOptions)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", err
		}
		args := []string{"clone", "--quiet", "--depth", strconv.Itoa(depth)}
		if ref != "" && (!looksLikeSHA(ref) || remoteRef != "") {
			args = append(args, "--branch", ref)
		}
		args = append(args, endOfOptions...)
		args = append(args, url, dir)
		if out, err := runGit(ctx, "", args...); err != nil {
			return "", fmt.Errorf("%v: %s", err, out)
		}
	}
	if ref != "" {
		// Best-effort fetch of the exact ref so a pinned SHA is available even
		// when it is not on the default branch; a fetch failure is not fatal on
		// its own, the checkout below surfaces the real failure.
		// --end-of-options stops Git parsing anything after it as an option, so
		// the ref can only ever be a refspec/revision even if the shape guard
		// above is ever relaxed.
		fetchRef := ref
		if remoteRef != "" {
			// Fully qualified, so the remote resolves it as the ref it is and
			// never as the object that shares its name.
			fetchRef = remoteRef
		}
		fetchArgs := append([]string{"fetch", "--quiet", "--depth", strconv.Itoa(depth)}, endOfOptions...)
		fetchArgs = append(fetchArgs, "origin", fetchRef)
		fetchOut, fetchErr := runGit(ctx, dir, fetchArgs...)
		if remoteRef != "" {
			// The remote publishes this name as a ref, so FETCH_HEAD is that
			// ref's tip. Checking the name out instead would resolve it to the
			// object of the same name -- a different commit, reported as a
			// success. Only the fetch can fail here, and it is fatal.
			if fetchErr != nil {
				return "", fmt.Errorf("fetch %s: %v: %s", fetchRef, fetchErr, fetchOut)
			}
			detachArgs := append([]string{"checkout", "--quiet", "--detach"}, endOfOptions...)
			detachArgs = append(detachArgs, "FETCH_HEAD")
			if out, err := runGit(ctx, dir, detachArgs...); err != nil {
				return "", fmt.Errorf("checkout %s: %v: %s", fetchRef, err, out)
			}
			sha, err := runGit(ctx, dir, "rev-parse", "HEAD")
			return strings.TrimSpace(sha), err
		}
		checkoutArgs := append([]string{"checkout", "--quiet"}, endOfOptions...)
		checkoutArgs = append(checkoutArgs, ref)
		if out, err := runGit(ctx, dir, checkoutArgs...); err != nil {
			// A ref the remote publishes but this clone does not carry a
			// local branch for -- a cached clone made for a different ref,
			// say -- fails checkout by name even though the fetch just
			// resolved it. The fetch asked for exactly one ref, so FETCH_HEAD
			// is exactly that ref's tip: check it out detached rather than
			// failing a manifest that names a valid ref. Any other checkout
			// failure keeps its original error.
			if fetchErr != nil {
				return "", fmt.Errorf("checkout %s: %v: %s", ref, err, out)
			}
			// validateRef rejects refspec syntax, so the fetch above asked for
			// exactly one ref and FETCH_HEAD holds exactly that ref's tip.
			// Re-check the file rather than trusting that reasoning: a
			// multi-entry FETCH_HEAD means the ref was not a single ref, and
			// its first entry is not what the manifest asked for.
			if n, err := fetchHeadEntries(ctx, dir); err != nil || n != 1 {
				return "", fmt.Errorf("checkout %s: %v: %s (FETCH_HEAD holds %d entries, want 1: %v)", ref, err, out, n, err)
			}
			fallbackArgs := append([]string{"checkout", "--quiet", "--detach"}, endOfOptions...)
			fallbackArgs = append(fallbackArgs, "FETCH_HEAD")
			if fallbackOut, fallbackErr := runGit(ctx, dir, fallbackArgs...); fallbackErr != nil {
				return "", fmt.Errorf("checkout %s: %v: %s (FETCH_HEAD fallback: %v: %s)", ref, err, out, fallbackErr, fallbackOut)
			}
		}
	}
	sha, err := runGit(ctx, dir, "rev-parse", "HEAD")
	return strings.TrimSpace(sha), err
}

// looksLikeSHA reports whether a ref is an object id rather than a branch name,
// which decides whether ensureRepo may pass it to `git clone --branch`.
//
// Git has two object formats: SHA-1 (40 hex chars) and SHA-256 (64), the latter
// selected by `git init --object-format=sha256`, init.defaultObjectFormat or
// GIT_DEFAULT_HASH. Capping the length at 40 misclassified a pinned full
// SHA-256 commit id as a branch name, so the clone became
// `git clone --branch <64 hex>` and died with
// "fatal: Remote branch <id> not found in upstream origin" (verified against
// git 2.54.0). The upper bound is therefore the longer format's width, which
// keeps every previously accepted abbreviation accepted.
//
// The classification is a guess in both directions: a lowercase-hex branch name
// looks exactly like an object id, and the two can coexist in one repository
// pointing at different commits. It is therefore not load-bearing -- ensureRepo
// asks the remote (remoteRefFor) whenever this returns true, and a name the
// remote publishes as a branch or tag is treated as that ref, not as an object.
func looksLikeSHA(ref string) bool {
	if len(ref) < 7 || len(ref) > 64 {
		return false
	}
	for _, r := range ref {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// remoteRefFor returns the fully-qualified ref the remote publishes under name,
// preferring a branch over a tag, and "" when the remote publishes neither (the
// ordinary case for a pinned commit id). ls-remote is a read-only query, so a
// name that is not a ref costs one listing and nothing else.
func remoteRefFor(ctx context.Context, url, name string, endOfOptions []string) string {
	args := append([]string{"ls-remote", "--quiet"}, endOfOptions...)
	args = append(args, url, "refs/heads/"+name, "refs/tags/"+name)
	out, err := runGit(ctx, "", args...)
	if err != nil {
		return ""
	}
	tag := ""
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch {
		case fields[1] == "refs/heads/"+name:
			return fields[1]
		case fields[1] == "refs/tags/"+name && tag == "":
			tag = fields[1]
		}
	}
	return tag
}

// fetchHeadEntries counts the refs the last fetch recorded in FETCH_HEAD.
func fetchHeadEntries(ctx context.Context, dir string) (int, error) {
	path, err := runGit(ctx, dir, "rev-parse", "--git-path", "FETCH_HEAD")
	if err != nil {
		return 0, fmt.Errorf("locate FETCH_HEAD: %w", err)
	}
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read FETCH_HEAD: %w", err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n, nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func loadLock(path string) (map[string]string, error) {
	lock := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return lock, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parse lock: %w", err)
	}
	return lock, nil
}

func writeLock(path string, lock map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func emitReport(report bench.Report, outDir string) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if outDir == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("result-%d.json", time.Now().Unix())
	path := filepath.Join(outDir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote report to %s\n", path)
	return nil
}

func printSummary(report bench.Report) {
	writeSummary(os.Stderr, report)
}

func writeSummary(output io.Writer, report bench.Report) {
	w := tabwriter.NewWriter(output, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "\nLANGUAGE\tREPOS\tFILES\tLOC\tSYMBOLS\tRELATIONS\tLOC/S\tPARSE_FAIL")
	languages := make([]string, 0, len(report.ByLanguage))
	for language := range report.ByLanguage {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	for _, language := range languages {
		a := report.ByLanguage[language]
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%.0f\t%d\n", language, a.Repos, a.Files, a.LOC, a.Symbols, a.Relations, a.LOCPerSec, a.ParseFailures)
	}
	t := report.Totals
	fmt.Fprintf(w, "TOTAL\t%d\t%d\t%d\t%d\t%d\t%.0f\t%d\n", t.Repos, t.Files, t.LOC, t.Symbols, t.Relations, t.LOCPerSec, t.ParseFailures)
	fmt.Fprintln(w, "\nPHASE\tMS\tSHARE")
	phaseTotal := 0.0
	for _, elapsed := range t.PhaseMS {
		phaseTotal += elapsed
	}
	for _, phase := range []string{"inventory", "parse", "relations", "finalize"} {
		elapsed := t.PhaseMS[phase]
		share := 0.0
		if phaseTotal > 0 {
			share = elapsed * 100 / phaseTotal
		}
		fmt.Fprintf(w, "%s\t%.2f\t%.2f%%\n", phase, elapsed, share)
	}
	w.Flush()
	fmt.Fprintf(output, "ARTIFACT native_raw=%d compact_raw=%d compact_dictionary=%d projected_facts=%d native_bytes/fact=%.2f compact_bytes/fact=%.2f\n",
		t.NDJSONRawBytes,
		t.CompactRawBytes,
		t.CompactDictionaryBytes,
		t.ProjectedFacts,
		t.NDJSONBytesPerProjectedFact,
		t.CompactBytesPerProjectedFact,
	)
}
