package sem

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

const ancestorTestVersion = "ancestor-test"

var ancestorTestOptions = ProviderSnapshotOptions{NoNetwork: true, Profile: ProfileFull}

// ancestorTestRepo builds a committed fixture with 85 indexed Go files, so the
// 5% gate admits exactly 4 changed paths: widget.go is the code that survives
// a commit untouched, gadget.go, old_name.go and doomed.go are the files the
// next commit modifies, renames and deletes, and 80 filler files set the
// denominator.
func ancestorTestRepo(t *testing.T) string {
	t.Helper()
	t.Setenv(searchStoreEnv, "1")
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Ancestor Test")
	git(t, repo, "config", "user.email", "ancestor@example.invalid")
	for index := range 80 {
		write(t, repo, fmt.Sprintf("pkg/filler_%02d.go", index), fmt.Sprintf("package pkg\n\nfunc Filler%02d() int { return %d }\n", index, index))
	}
	write(t, repo, "widget.go", "package sample\n\ntype Widget struct{ Name string }\n\nfunc NewWidget(name string) *Widget { return &Widget{Name: name} }\n\nfunc RenderWidget(w *Widget) string { return w.Name }\n")
	write(t, repo, "widget_test.go", "package sample\n\nimport \"testing\"\n\nfunc TestRenderWidget(t *testing.T) {\n\tif RenderWidget(NewWidget(\"x\")) != \"x\" {\n\t\tt.Fatal(\"render\")\n\t}\n}\n")
	write(t, repo, "gadget.go", "package sample\n\nfunc RenderGadget() string { return RenderWidget(NewWidget(\"gadget\")) }\n")
	write(t, repo, "old_name.go", "package sample\n\nfunc RenderLegacy() string { return RenderWidget(NewWidget(\"legacy\")) }\n")
	write(t, repo, "doomed.go", "package sample\n\nfunc RenderDoomed() string { return \"doomed\" }\n")
	ancestorTestCommit(t, repo, "base")
	return repo
}

func ancestorTestCommit(t *testing.T, repo, message string) {
	t.Helper()
	git(t, repo, "add", "-A")
	git(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", message)
}

func ancestorTestRev(t *testing.T, repo, rev string) string {
	t.Helper()
	return gitInput(t, repo, "", "rev-parse", "--verify", rev)
}

// ancestorTestBuildStore builds the store for the checkout's current HEAD.
func ancestorTestBuildStore(t *testing.T, repo, cacheDir, version string, options ProviderSnapshotOptions) {
	t.Helper()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, version, options, cacheDir); err != nil {
		t.Fatal(err)
	}
	if _, err := DescribeSearchStore(t.Context(), repo, version, cacheDir, options); err != nil {
		t.Fatalf("store was not built for HEAD: %v", err)
	}
}

// ancestorTestRefactorCommit modifies gadget.go, renames old_name.go and
// deletes doomed.go: four changed paths under rename-free comparison.
func ancestorTestRefactorCommit(t *testing.T, repo string) {
	t.Helper()
	write(t, repo, "gadget.go", "package sample\n\nfunc RenderGadget() string { return RenderWidget(NewWidget(\"gadget v2\")) }\n")
	git(t, repo, "mv", "old_name.go", "new_name.go")
	git(t, repo, "rm", "-q", "doomed.go")
	ancestorTestCommit(t, repo, "refactor")
}

var ancestorTestChangedFiles = []string{"doomed.go", "gadget.go", "new_name.go", "old_name.go"}

func refusalReason(err error) string {
	var refusal *ancestorStoreRefusal
	if errors.As(err, &refusal) {
		return refusal.reason
	}
	return ""
}

// An ancestor one commit back serves, withholding every record in the four
// changed files and listing all four of them: both sides of the rename and
// the deleted path. ExcludedRecords is checked against an independent count
// over the served store, so a filter that silently matched nothing (0 == 0)
// cannot pass.
func TestAncestorStoreServesHeadMinusOneWithoutChangedFiles(t *testing.T) {
	repo := ancestorTestRepo(t)
	cacheDir := t.TempDir()
	ancestorTestBuildStore(t, repo, cacheDir, ancestorTestVersion, ancestorTestOptions)
	ancestorCommit := ancestorTestRev(t, repo, "HEAD")
	ancestorTree := ancestorTestRev(t, repo, "HEAD^{tree}")
	ancestorTestRefactorCommit(t, repo)

	if _, err := LoadSearchStoreContext(t.Context(), repo, ancestorTestVersion, cacheDir, "render", ancestorTestOptions, 100, 100, 0); !errors.Is(err, ErrSearchStoreMissing) {
		t.Fatalf("exact lookup at the new commit should miss, got %v", err)
	}
	got, err := LoadSearchStoreContextAllowingAncestor(t.Context(), repo, ancestorTestVersion, cacheDir, "render", ancestorTestOptions, 100, 100, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stale == nil {
		t.Fatal("ancestor answer carries no stale block")
	}
	stale := *got.Stale
	if stale.AncestorCommit != ancestorCommit || stale.AncestorTree != ancestorTree || stale.CommitsBehind != 1 {
		t.Fatalf("stale identity = %+v, want commit %s tree %s 1 behind", stale, ancestorCommit, ancestorTree)
	}
	if got.Tree != ancestorTree {
		t.Fatalf("top-level tree %s is not the served store's tree %s", got.Tree, ancestorTree)
	}
	if !slices.Equal(stale.ChangedFiles, ancestorTestChangedFiles) {
		t.Fatalf("changed_files = %q, want %q", stale.ChangedFiles, ancestorTestChangedFiles)
	}

	changed := map[string]bool{}
	for _, path := range ancestorTestChangedFiles {
		changed[path] = true
	}
	foundWidget := false
	for _, symbol := range append(append([]StoreContextSymbol(nil), got.Symbols...), got.Tests...) {
		if changed[symbol.FilePath] {
			t.Fatalf("symbol from changed file served: %+v", symbol)
		}
		if symbol.Name == "RenderWidget" {
			foundWidget = true
		}
	}
	if !foundWidget {
		t.Fatalf("the unchanged file's symbol was not served: %+v", got.Symbols)
	}

	path, digest := ancestorTestStorePath(t, repo, cacheDir, ancestorTree)
	db, err := sql.Open("sqlite", "file:"+sqliteURLPath(path)+"?immutable=1&mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	changedIDs := ancestorTestChangedEndpointIDs(t, db, ancestorTestChangedFiles)
	for _, relation := range got.Relations {
		if changedIDs[relation.FromID] || changedIDs[relation.ToID] {
			t.Fatalf("relation touching a changed file served: %+v", relation)
		}
	}
	wantExcluded := ancestorTestCountMatchesIn(t, db, "render", ancestorTestChangedFiles) +
		ancestorTestCountRelationsInto(t, db, got.Symbols, changedIDs)
	if wantExcluded == 0 {
		t.Fatal("fixture withholds nothing, so the exclusion count pins nothing")
	}
	if stale.ExcludedRecords != wantExcluded {
		t.Fatalf("excluded_records = %d, independent count = %d", stale.ExcludedRecords, wantExcluded)
	}

	// The unfiltered store really holds what the answer withheld.
	exact, err := QuerySearchStoreContext(t.Context(), path, digest, "render", 100, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	served := 0
	for _, symbol := range exact.Symbols {
		if changed[symbol.FilePath] {
			served++
		}
	}
	if served == 0 {
		t.Fatalf("unfiltered ancestor store has no changed-file symbols to withhold: %+v", exact.Symbols)
	}
}

func ancestorTestStorePath(t *testing.T, repo, cacheDir, tree string) (string, string) {
	t.Helper()
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	options := ancestorTestOptions
	options, err = CaptureProviderCachePolicy(absRepo, options)
	if err != nil {
		t.Fatal(err)
	}
	key, err := searchSnapshotKey(absRepo, repoKey(t.Context(), absRepo), ancestorTestVersion, tree, options, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store, db, err := openExactSearchStore(t.Context(), cacheDir, key, tree)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	return store.path, store.entryDigest
}

func ancestorTestChangedEndpointIDs(t *testing.T, db *sql.DB, files []string) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for _, file := range files {
		rows, err := db.Query(`SELECT id FROM symbols WHERE file_path = ? UNION ALL SELECT json_extract(record, '$.id') FROM files WHERE path = ?`, file, file)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids[id] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return ids
}

func ancestorTestCountMatchesIn(t *testing.T, db *sql.DB, token string, files []string) int {
	t.Helper()
	total := 0
	for _, file := range files {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM symbols WHERE file_path = ? AND
			(instr(lower(name), ?) > 0 OR instr(lower(qualified_name), ?) > 0 OR instr(lower(file_path), ?) > 0)`,
			file, token, token, token).Scan(&count); err != nil {
			t.Fatal(err)
		}
		total += count
	}
	return total
}

func ancestorTestCountRelationsInto(t *testing.T, db *sql.DB, returned []StoreContextSymbol, changedIDs map[string]bool) int {
	t.Helper()
	ids := map[string]bool{}
	for _, symbol := range returned {
		ids[symbol.ID] = true
	}
	rows, err := db.Query(`SELECT from_id, to_id FROM relations`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			t.Fatal(err)
		}
		if (ids[from] || ids[to]) && (changedIDs[from] || changedIDs[to]) {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return count
}

// Every refusal is the exact miss, unchanged: the same empty result and the
// same error text the exact call returns. Each case also names the refusal
// it must have hit and, where the fixture allows, shows the same history
// serving under a looser setting, so a miss for an unrelated reason cannot
// pass.
func TestAncestorStoreRefusalsAreTheExactMiss(t *testing.T) {
	assertExactMiss := func(t *testing.T, repo, version, cacheDir string, options ProviderSnapshotOptions, maxBehind int) {
		t.Helper()
		exact, exactErr := LoadSearchStoreContext(t.Context(), repo, version, cacheDir, "render", options, 12, 6, 0)
		got, err := LoadSearchStoreContextAllowingAncestor(t.Context(), repo, version, cacheDir, "render", options, 12, 6, 0, maxBehind)
		if !errors.Is(exactErr, ErrSearchStoreMissing) {
			t.Fatalf("exact call should miss, got %v", exactErr)
		}
		if err == nil || err.Error() != exactErr.Error() || !reflect.DeepEqual(got, exact) {
			t.Fatalf("refused ancestor differs from the exact miss:\n got %+v, %v\nwant %+v, %v", got, err, exact, exactErr)
		}
	}

	t.Run("beyond N commits", func(t *testing.T) {
		repo := ancestorTestRepo(t)
		cacheDir := t.TempDir()
		ancestorTestBuildStore(t, repo, cacheDir, ancestorTestVersion, ancestorTestOptions)
		write(t, repo, "gadget.go", "package sample\n\nfunc RenderGadget() string { return \"one\" }\n")
		ancestorTestCommit(t, repo, "one")
		write(t, repo, "gadget.go", "package sample\n\nfunc RenderGadget() string { return \"two\" }\n")
		ancestorTestCommit(t, repo, "two")

		_, err := resolveAncestorSearchStore(t.Context(), repo, ancestorTestVersion, cacheDir, ancestorTestOptions, 1)
		if reason := refusalReason(err); reason != "no ancestor store" {
			t.Fatalf("refusal = %q (%v), want no ancestor store", reason, err)
		}
		assertExactMiss(t, repo, ancestorTestVersion, cacheDir, ancestorTestOptions, 1)
		served, err := resolveAncestorSearchStore(t.Context(), repo, ancestorTestVersion, cacheDir, ancestorTestOptions, 2)
		if err != nil || served.stale.CommitsBehind != 2 {
			t.Fatalf("control: N=2 should serve 2 behind, got %+v, %v", served.stale, err)
		}
	})

	t.Run("over five percent changed", func(t *testing.T) {
		repo := ancestorTestRepo(t)
		cacheDir := t.TempDir()
		ancestorTestBuildStore(t, repo, cacheDir, ancestorTestVersion, ancestorTestOptions)
		ancestorTestRefactorCommit(t, repo)
		// Four of 85 is within 5%: the control serves.
		if served, err := resolveAncestorSearchStore(t.Context(), repo, ancestorTestVersion, cacheDir, ancestorTestOptions, 1); err != nil || len(served.stale.ChangedFiles) != 4 {
			t.Fatalf("control: 4 changed of 85 should serve, got %+v, %v", served.stale, err)
		}
		// A fifth path (on top of the same ancestor, now 2 behind) is not.
		write(t, repo, "pkg/filler_00.go", "package pkg\n\nfunc Filler00() int { return -1 }\n")
		ancestorTestCommit(t, repo, "fifth path")
		_, err := resolveAncestorSearchStore(t.Context(), repo, ancestorTestVersion, cacheDir, ancestorTestOptions, 5)
		if reason := refusalReason(err); reason != "too many changed files" {
			t.Fatalf("refusal = %q (%v), want too many changed files", reason, err)
		}
		assertExactMiss(t, repo, ancestorTestVersion, cacheDir, ancestorTestOptions, 5)
	})

	t.Run("diff failure", func(t *testing.T) {
		repo := ancestorTestRepo(t)
		cacheDir := t.TempDir()
		ancestorTestBuildStore(t, repo, cacheDir, ancestorTestVersion, ancestorTestOptions)
		missing := ancestorTestRev(t, repo, "HEAD:pkg")
		write(t, repo, "pkg/filler_00.go", "package pkg\n\nfunc Filler00() int { return -1 }\n")
		ancestorTestCommit(t, repo, "change pkg")
		write(t, repo, "gadget.go", "package sample\n\nfunc RenderGadget() string { return \"two\" }\n")
		ancestorTestCommit(t, repo, "change gadget")
		if _, err := resolveAncestorSearchStore(t.Context(), repo, ancestorTestVersion, cacheDir, ancestorTestOptions, 2); err != nil {
			t.Fatalf("control: intact history should serve, got %v", err)
		}
		// The ancestor's side of the changed subtree disappears, as in a
		// partial clone that never fetched it. Only the ancestor-to-HEAD
		// comparison needs it: HEAD's own commit and its parent still read,
		// so the exact path still reaches its ordinary miss.
		objectPath := filepath.Join(repo, ".git", "objects", missing[:2], missing[2:])
		// Git writes objects read-only, which Windows refuses to delete.
		if err := os.Chmod(objectPath, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(objectPath); err != nil {
			t.Fatal(err)
		}
		_, err := resolveAncestorSearchStore(t.Context(), repo, ancestorTestVersion, cacheDir, ancestorTestOptions, 2)
		if reason := refusalReason(err); reason != "changed paths" {
			t.Fatalf("refusal = %q (%v), want changed paths", reason, err)
		}
		assertExactMiss(t, repo, ancestorTestVersion, cacheDir, ancestorTestOptions, 2)
	})

	t.Run("provider version mismatch", func(t *testing.T) {
		repo := ancestorTestRepo(t)
		cacheDir := t.TempDir()
		ancestorTestBuildStore(t, repo, cacheDir, "ancestor-test-old", ancestorTestOptions)
		ancestorTestRefactorCommit(t, repo)
		if _, err := resolveAncestorSearchStore(t.Context(), repo, "ancestor-test-old", cacheDir, ancestorTestOptions, 1); err != nil {
			t.Fatalf("control: the building version should serve, got %v", err)
		}
		_, err := resolveAncestorSearchStore(t.Context(), repo, "ancestor-test-new", cacheDir, ancestorTestOptions, 1)
		if reason := refusalReason(err); reason != "no ancestor store" {
			t.Fatalf("refusal = %q (%v), want no ancestor store", reason, err)
		}
		assertExactMiss(t, repo, "ancestor-test-new", cacheDir, ancestorTestOptions, 1)
	})

	t.Run("ignore policy mismatch", func(t *testing.T) {
		repo := ancestorTestRepo(t)
		cacheDir := t.TempDir()
		ancestorTestBuildStore(t, repo, cacheDir, ancestorTestVersion, ancestorTestOptions)
		ancestorTestRefactorCommit(t, repo)
		ignore := filepath.Join(t.TempDir(), "extra.ignore")
		if err := os.WriteFile(ignore, []byte("pkg/filler_79.go\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		withIgnore := ancestorTestOptions
		withIgnore.IgnoreFiles = []string{ignore}
		_, err := resolveAncestorSearchStore(t.Context(), repo, ancestorTestVersion, cacheDir, withIgnore, 1)
		if reason := refusalReason(err); reason != "no ancestor store" {
			t.Fatalf("refusal = %q (%v), want no ancestor store", reason, err)
		}
		assertExactMiss(t, repo, ancestorTestVersion, cacheDir, withIgnore, 1)
	})
}

// An exact hit never consults ancestors and never carries a stale block.
func TestAncestorFallbackLeavesAnExactHitUnchanged(t *testing.T) {
	repo := ancestorTestRepo(t)
	cacheDir := t.TempDir()
	ancestorTestBuildStore(t, repo, cacheDir, ancestorTestVersion, ancestorTestOptions)
	ancestorTestRefactorCommit(t, repo)
	ancestorTestBuildStore(t, repo, cacheDir, ancestorTestVersion, ancestorTestOptions)
	exact, err := LoadSearchStoreContext(t.Context(), repo, ancestorTestVersion, cacheDir, "render", ancestorTestOptions, 12, 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadSearchStoreContextAllowingAncestor(t.Context(), repo, ancestorTestVersion, cacheDir, "render", ancestorTestOptions, 12, 6, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stale != nil || !reflect.DeepEqual(got, exact) {
		t.Fatalf("exact hit changed under --allow-ancestor:\n got %+v\nwant %+v", got, exact)
	}
}

// The structural half of "graph store stays exact": no value the ancestor
// path produces has, or contains, a type that a store writer accepts or the
// descriptor carries. Handing an ancestor to either therefore does not
// compile. The writer and descriptor types are read from the real function
// signatures, so a new writer parameter type is covered automatically.
func TestAncestorStoreValuesCannotReachStoreWriterOrDescriptor(t *testing.T) {
	forbidden := map[reflect.Type]string{}
	addParams := func(name string, fn any) {
		fnType := reflect.TypeOf(fn)
		for index := range fnType.NumIn() {
			param := fnType.In(index)
			for param.Kind() == reflect.Pointer {
				param = param.Elem()
			}
			if param.PkgPath() == reflect.TypeOf(cacheEntry{}).PkgPath() {
				forbidden[param] = name
			}
		}
	}
	addParams("writeSearchStoreSidecar", writeSearchStoreSidecar)
	addParams("repairSearchStoreSidecarForEntry", repairSearchStoreSidecarForEntry)
	addParams("buildSearchStore", buildSearchStore)
	addParams("writeSearchSnapshotAndStore", writeSearchSnapshotAndStore)
	addParams("persistVerifiedSearchSnapshot", persistVerifiedSearchSnapshot)
	forbidden[reflect.TypeOf(SearchStoreDescriptor{})] = "DescribeSearchStore"
	forbidden[reflect.TypeOf(exactSearchStore{})] = "openExactSearchStore"
	for _, want := range []reflect.Type{reflect.TypeOf(cacheEntry{}), reflect.TypeOf(cachedSearchSnapshot{})} {
		if _, ok := forbidden[want]; !ok {
			t.Fatalf("writer signatures no longer mention %v; update this guard", want)
		}
	}

	for _, root := range []reflect.Type{reflect.TypeOf(ancestorSearchStore{}), reflect.TypeOf(StoreContextStale{}), reflect.TypeOf(SearchStoreContext{})} {
		visited := map[reflect.Type]bool{}
		var walk func(path string, typ reflect.Type)
		walk = func(path string, typ reflect.Type) {
			if visited[typ] {
				return
			}
			visited[typ] = true
			if owner, bad := forbidden[typ]; bad {
				t.Errorf("%s has type %v, which %s accepts or produces", path, typ, owner)
			}
			switch typ.Kind() {
			case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
				walk(path+"[]", typ.Elem())
			case reflect.Struct:
				for index := range typ.NumField() {
					field := typ.Field(index)
					walk(path+"."+field.Name, field.Type)
				}
			}
			if typ.ConvertibleTo(reflect.TypeOf(SearchStoreDescriptor{})) {
				t.Errorf("%s (%v) converts to SearchStoreDescriptor", path, typ)
			}
		}
		walk(root.Name(), root)
	}
}

// The runtime half: if code ever keyed one tree's store under another tree
// (here an ancestor's gz entry and sidecar planted at HEAD's address, which
// passes every digest check), the descriptor and the exact context path fail
// loudly instead of describing or serving it, and the failure is not the
// quiet ErrSearchStoreMissing a consumer would treat as "build later".
func TestStoreDescriptorRefusesAStoreKeyedUnderAnotherTree(t *testing.T) {
	repo := ancestorTestRepo(t)
	cacheDir := t.TempDir()
	ancestorTestBuildStore(t, repo, cacheDir, ancestorTestVersion, ancestorTestOptions)
	ancestorTree := ancestorTestRev(t, repo, "HEAD^{tree}")
	ancestorTestRefactorCommit(t, repo)
	headTree := ancestorTestRev(t, repo, "HEAD^{tree}")

	from := ancestorTestEntry(t, repo, cacheDir, ancestorTree)
	to := ancestorTestEntry(t, repo, cacheDir, headTree)
	for _, pair := range [][2]cacheEntry{{from, to}, {searchStoreEntryFor(from), searchStoreEntryFor(to)}} {
		content, err := os.ReadFile(filepath.Join(pair[0].root, pair[0].relative))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(pair[1].root, pair[1].relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err := DescribeSearchStore(t.Context(), repo, ancestorTestVersion, cacheDir, ancestorTestOptions)
	if !errors.Is(err, errSearchStoreNotExact) || errors.Is(err, ErrSearchStoreMissing) {
		t.Fatalf("descriptor accepted or quietly missed a store keyed under another tree: %v", err)
	}
	_, err = LoadSearchStoreContext(t.Context(), repo, ancestorTestVersion, cacheDir, "render", ancestorTestOptions, 12, 6, 0)
	if !errors.Is(err, errSearchStoreNotExact) {
		t.Fatalf("exact context served a store keyed under another tree: %v", err)
	}
	_, err = LoadSearchStoreContextAllowingAncestor(t.Context(), repo, ancestorTestVersion, cacheDir, "render", ancestorTestOptions, 12, 6, 0, 5)
	if !errors.Is(err, errSearchStoreNotExact) {
		t.Fatalf("--allow-ancestor turned a mis-keyed store into an answer: %v", err)
	}
}

func ancestorTestEntry(t *testing.T, repo, cacheDir, tree string) cacheEntry {
	t.Helper()
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	options, err := CaptureProviderCachePolicy(absRepo, ancestorTestOptions)
	if err != nil {
		t.Fatal(err)
	}
	key, err := searchSnapshotKey(absRepo, repoKey(context.Background(), absRepo), ancestorTestVersion, tree, options, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}
