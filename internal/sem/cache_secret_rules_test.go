package sem

import (
	"testing"
)

// setBuiltinSecretPatterns swaps the built-in credential-store taxonomy — the
// pattern block and the rules parsed from it, so the stand-in both filters and
// keys consistently — for the rest of the test, restoring both afterwards.
//
// It exists so one test can stand in for "a different build of this binary"
// without shelling out to one, which is the case that matters here: the two
// persistent caches are keyed on inputs that are byte-identical across such
// builds (the provider version is the release string, and the repository's
// standard `mise run build` leaves it at "dev"), so the taxonomy is the only
// thing that differs between them and therefore the only thing their keys can
// tell them apart by.
//
// Mutates package-level state: tests using it must not call t.Parallel().
func setBuiltinSecretPatterns(t *testing.T, patterns string) {
	t.Helper()
	setBuiltinSecretPolicy(t, patterns, builtinSecretFileOnlyPatterns)
}

// setBuiltinSecretPolicy swaps every input that determines the effective
// built-in credential-store matcher. It returns an idempotent restore function
// so one test can model two builds in sequence while keeping the raw pattern
// text byte-identical.
//
// Mutates package-level state: tests using it must not call t.Parallel().
func setBuiltinSecretPolicy(t *testing.T, patterns string, fileOnlyPatterns map[string]struct{}) func() {
	t.Helper()
	previousPatterns := builtinSecretIgnorePatterns
	previousFileOnlyPatterns := builtinSecretFileOnlyPatterns
	previousRules := builtinSecretIgnoreRules
	builtinSecretIgnorePatterns = patterns
	builtinSecretFileOnlyPatterns = fileOnlyPatterns
	builtinSecretIgnoreRules = parseBuiltinSecretIgnoreRules()
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		builtinSecretIgnorePatterns = previousPatterns
		builtinSecretFileOnlyPatterns = previousFileOnlyPatterns
		builtinSecretIgnoreRules = previousRules
	}
	t.Cleanup(restore)
	return restore
}

func assertCredentialsDirectoryPolicy(t *testing.T, wantIgnored bool) {
	t.Helper()
	var matcher ignoreMatcher
	matcher.loadBuiltinSecretRules()
	if got := matcher.Ignored("credentials/config.go", false); got != wantIgnored {
		t.Fatalf("credentials/config.go ignored=%v, want %v", got, wantIgnored)
	}
}

// A record-stream cache entry written by a build with a different
// credential-store taxonomy lists files this build excludes, and serving it
// re-emits those records. Reproduced end to end with two real binaries over a
// repository holding .env, .npmrc, credentials.json and
// deploy/secrets/prod-secrets.yaml: the pre-deny binary warmed the cache and the
// post-deny binary hit it and printed symbol, file and relation records for all
// four, plus wrote them into `index --report`. The entry must therefore miss.
//
// The control below is load-bearing: it stores and loads under ONE taxonomy and
// requires a hit, so this test cannot pass on a build whose cache never serves
// anything at all.
//
// Mutates package-level state: no t.Parallel().
func TestProviderRecordsCacheMissesEntryFromDifferentSecretRules(t *testing.T) {
	repo := t.TempDir()
	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	records := []byte(`{"record_type":"symbol","file_path":".env"}` + "\n")

	livePatterns := builtinSecretIgnorePatterns
	liveRules := builtinSecretIgnoreRules

	// The pre-deny build: a taxonomy that denied nothing.
	setBuiltinSecretPatterns(t, "")
	if err := StoreProviderRecords(t.Context(), repo, "dev", "tree-1", "symbols", cacheDir, options, records, nil); err != nil {
		t.Fatalf("store records under the pre-deny taxonomy: %v", err)
	}
	control, _, hit, err := LoadProviderRecords(t.Context(), repo, "dev", "tree-1", "symbols", cacheDir, options)
	if err != nil {
		t.Fatalf("control load under the pre-deny taxonomy: %v", err)
	}
	if !hit || string(control) != string(records) {
		t.Fatalf("control: same taxonomy must serve its own entry, got hit=%v records=%q", hit, control)
	}

	// This build, same repo, same provider version, same tree, same mode.
	builtinSecretIgnorePatterns = livePatterns
	builtinSecretIgnoreRules = liveRules
	cached, _, hit, err := LoadProviderRecords(t.Context(), repo, "dev", "tree-1", "symbols", cacheDir, options)
	if err != nil {
		t.Fatalf("load records under the live taxonomy: %v", err)
	}
	if hit {
		t.Fatalf("records cache served an entry built under a different credential-store taxonomy: %s", cached)
	}
}

// Same argument for the search-snapshot cache, stated at its key: the snapshot
// decides which files the ranked payload, the snippets and the context blocks may
// quote, so an entry built under a different taxonomy must not be reachable from
// this build's key. This key already hashes .graphignore for exactly this reason;
// the built-in block is the same kind of input.
//
// Mutates package-level state: no t.Parallel().
func TestSearchSnapshotKeyBindsBuiltinSecretRules(t *testing.T) {
	repo := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}

	liveKey, err := searchSnapshotKey(repo, "local/victim", "dev", "tree-1", options)
	if err != nil {
		t.Fatalf("live key: %v", err)
	}
	// Control: the key is a function of its inputs, not of anything ambient, so an
	// unchanged taxonomy must reproduce it. Without this, the inequality below
	// would also pass for a key that simply changed every call.
	repeatKey, err := searchSnapshotKey(repo, "local/victim", "dev", "tree-1", options)
	if err != nil {
		t.Fatalf("repeat key: %v", err)
	}
	if liveKey != repeatKey {
		t.Fatalf("control: search-snapshot key is not stable across calls: %s vs %s", liveKey, repeatKey)
	}

	setBuiltinSecretPatterns(t, "")
	priorKey, err := searchSnapshotKey(repo, "local/victim", "dev", "tree-1", options)
	if err != nil {
		t.Fatalf("prior key: %v", err)
	}
	if liveKey == priorKey {
		t.Fatalf("search-snapshot key %s is identical under two different credential-store taxonomies", liveKey)
	}
}

// The same binding for the records key, stated directly at the key rather than
// through a round trip: any future edit to the taxonomy moves the key by
// construction, so there is no version constant anyone has to remember to bump.
//
// Mutates package-level state: no t.Parallel().
func TestProviderRecordsKeyBindsBuiltinSecretRules(t *testing.T) {
	repo := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}

	liveKey, err := providerRecordsKey(repo, "local/victim", "dev", "tree-1", "symbols", options)
	if err != nil {
		t.Fatalf("live key: %v", err)
	}
	repeatKey, err := providerRecordsKey(repo, "local/victim", "dev", "tree-1", "symbols", options)
	if err != nil {
		t.Fatalf("repeat key: %v", err)
	}
	if liveKey != repeatKey {
		t.Fatalf("control: provider-records key is not stable across calls: %s vs %s", liveKey, repeatKey)
	}

	setBuiltinSecretPatterns(t, "")
	priorKey, err := providerRecordsKey(repo, "local/victim", "dev", "tree-1", "symbols", options)
	if err != nil {
		t.Fatalf("prior key: %v", err)
	}
	if liveKey == priorKey {
		t.Fatalf("provider-records key %s is identical under two different credential-store taxonomies", liveKey)
	}
}

// A metadata-only policy change can alter corpus membership without changing a
// byte of the raw pattern block. The search cache key must bind to the effective
// parsed semantics rather than only to that block.
//
// Mutates package-level state: no t.Parallel().
func TestSearchSnapshotKeyBindsBuiltinSecretRuleSemantics(t *testing.T) {
	repo := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	const patterns = "credentials\n"

	restore := setBuiltinSecretPolicy(t, patterns, map[string]struct{}{"credentials": {}})
	assertCredentialsDirectoryPolicy(t, false)
	fileOnlyKey, err := searchSnapshotKey(repo, "local/victim", "dev", "tree-1", options)
	if err != nil {
		t.Fatalf("file-only key: %v", err)
	}
	repeatKey, err := searchSnapshotKey(repo, "local/victim", "dev", "tree-1", options)
	if err != nil {
		t.Fatalf("repeat file-only key: %v", err)
	}
	if fileOnlyKey != repeatKey {
		t.Fatalf("control: unchanged effective policy changed the search key: %s vs %s", fileOnlyKey, repeatKey)
	}
	restore()

	setBuiltinSecretPolicy(t, patterns, nil)
	assertCredentialsDirectoryPolicy(t, true)
	ordinaryKey, err := searchSnapshotKey(repo, "local/victim", "dev", "tree-1", options)
	if err != nil {
		t.Fatalf("ordinary Git-semantics key: %v", err)
	}
	if fileOnlyKey == ordinaryKey {
		t.Fatalf("search-snapshot key %s ignored a file-only metadata change with identical raw patterns", fileOnlyKey)
	}
}

// The records cache has the same corpus-membership dependency as search and
// must therefore bind to the same effective rule semantics.
//
// Mutates package-level state: no t.Parallel().
func TestProviderRecordsKeyBindsBuiltinSecretRuleSemantics(t *testing.T) {
	repo := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	const patterns = "credentials\n"

	restore := setBuiltinSecretPolicy(t, patterns, map[string]struct{}{"credentials": {}})
	assertCredentialsDirectoryPolicy(t, false)
	fileOnlyKey, err := providerRecordsKey(repo, "local/victim", "dev", "tree-1", "symbols", options)
	if err != nil {
		t.Fatalf("file-only key: %v", err)
	}
	repeatKey, err := providerRecordsKey(repo, "local/victim", "dev", "tree-1", "symbols", options)
	if err != nil {
		t.Fatalf("repeat file-only key: %v", err)
	}
	if fileOnlyKey != repeatKey {
		t.Fatalf("control: unchanged effective policy changed the records key: %s vs %s", fileOnlyKey, repeatKey)
	}
	restore()

	setBuiltinSecretPolicy(t, patterns, nil)
	assertCredentialsDirectoryPolicy(t, true)
	ordinaryKey, err := providerRecordsKey(repo, "local/victim", "dev", "tree-1", "symbols", options)
	if err != nil {
		t.Fatalf("ordinary Git-semantics key: %v", err)
	}
	if fileOnlyKey == ordinaryKey {
		t.Fatalf("provider-records key %s ignored a file-only metadata change with identical raw patterns", fileOnlyKey)
	}
}

// Prove the key inequality above is load-bearing at the cache boundary. A build
// whose file-only rule admits credentials/config.go must not lend that record to
// a build whose ordinary Git-style rule excludes the ancestor directory.
//
// The same-policy load is a non-vacuous control: this cannot pass merely because
// the cache never serves records.
//
// Mutates package-level state: no t.Parallel().
func TestProviderRecordsCacheMissesEntryFromDifferentSecretRuleSemantics(t *testing.T) {
	repo := t.TempDir()
	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	const patterns = "credentials\n"
	records := []byte(`{"record_type":"symbol","file_path":"credentials/config.go"}` + "\n")

	restore := setBuiltinSecretPolicy(t, patterns, map[string]struct{}{"credentials": {}})
	assertCredentialsDirectoryPolicy(t, false)
	if err := StoreProviderRecords(t.Context(), repo, "dev", "tree-1", "symbols", cacheDir, options, records, nil); err != nil {
		t.Fatalf("store records under file-only policy: %v", err)
	}
	control, _, hit, err := LoadProviderRecords(t.Context(), repo, "dev", "tree-1", "symbols", cacheDir, options)
	if err != nil {
		t.Fatalf("control load under file-only policy: %v", err)
	}
	if !hit || string(control) != string(records) {
		t.Fatalf("control: same effective policy must serve its own entry, got hit=%v records=%q", hit, control)
	}
	restore()

	setBuiltinSecretPolicy(t, patterns, nil)
	assertCredentialsDirectoryPolicy(t, true)
	cached, _, hit, err := LoadProviderRecords(t.Context(), repo, "dev", "tree-1", "symbols", cacheDir, options)
	if err != nil {
		t.Fatalf("load under ordinary Git semantics: %v", err)
	}
	if hit {
		t.Fatalf("records cache served an entry from different effective rule semantics: %s", cached)
	}
}
