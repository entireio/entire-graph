package sem

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// headIndexedProfiles are the profiles whose committed-tree entry counts as "HEAD is indexed".
// `index` builds full by default and `query --head` builds fast by default, so either one means a
// later lookup at this HEAD is served from the cache rather than built.
var headIndexedProfiles = []Profile{ProfileFull, ProfileFast}

// IndexEntry is an opaque, JSON-serialisable handle on one file whose presence means
// "HEAD is indexed". Only HeadIndexEntries builds it; M3 changes only that function's body.
type IndexEntry struct{ Root, Relative string }

// HeadIndexEntries returns the entries whose presence means HEAD is indexed, and, for each
// of HEAD's first-parent ancestors up to maxBehind commits back, nearest first, the same
// for that ancestor's tree (same repo key, scope and policy inputs). The ancestors come
// from one gitutil.FirstParentCommitTrees(ctx, repo, head, maxBehind+1) walk, whose first
// element must be HEAD itself (as search_store_ancestor.go:179 checks). A shorter or
// shallow history returns fewer. walkErr reports a failed walk separately from err.
//
// HEAD's entries are the committed-tree search cache entries under each of headIndexedProfiles,
// with the repository's default policy inputs (its .graphignore, no explicit ignore or include
// files). Any one of them existing means HEAD is indexed. Deriving them resolves HEAD, the remote
// URLs and the checkout scope through git, and any failure among them is an error rather than a
// fallback key term; checking them (AnyIndexEntryExists) runs no git, which is what lets the status
// line keep the entries and re-check them every render.
//
// An empty cacheDir has no entries. An unresolvable HEAD, or metadata git may not be run on, is an
// error: the caller decides whether that is "not indexed", and must not keep the answer. A failed
// walk leaves HEAD's entries valid and behind nil; the caller must not keep that answer either.
//
// Entry paths come from newCacheEntry, the helper every search cache reader and writer addresses
// entries through, so a change to the on-disk layout changes this check with it.
func HeadIndexEntries(ctx context.Context, repo, providerVersion, cacheDir string, maxBehind int) (head []IndexEntry, behind [][]IndexEntry, walkErr, err error) {
	if cacheDir == "" {
		return nil, nil, nil, nil
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return nil, nil, nil, err
	}
	commit, tree, err := resolveCommittedHEAD(ctx, absRepo)
	if err != nil {
		return nil, nil, nil, err
	}
	policy, err := CaptureProviderCachePolicy(absRepo, ProviderSnapshotOptions{})
	if err != nil {
		return nil, nil, nil, err
	}
	// Strictly derived: repoKey and cacheCheckoutScope turn a failed git call into a fallback term,
	// which would address other entries and be kept as "not indexed" under an unchanged key.
	repositoryKey, err := repoKeyStrict(ctx, absRepo)
	if err != nil {
		return nil, nil, nil, err
	}
	scope, err := cacheCheckoutScopeStrict(ctx, absRepo)
	if err != nil {
		return nil, nil, nil, err
	}
	entriesFor := func(tree string) ([]IndexEntry, error) {
		entries := make([]IndexEntry, 0, len(headIndexedProfiles))
		for _, profile := range headIndexedProfiles {
			options := policy
			options.Profile = profile
			// searchSnapshotKey's terms, with the strict scope in place of cacheCheckoutScope.
			key, err := searchSnapshotKeyForScope(searchSnapshotCacheVersion, SchemaVersion, absRepo, repositoryKey,
				providerVersion, tree, options, func() string { return scope })
			if err != nil {
				return nil, err
			}
			entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, key)
			if err != nil {
				return nil, err
			}
			entries = append(entries, IndexEntry{Root: entry.root, Relative: entry.relative})
		}
		return entries, nil
	}
	if head, err = entriesFor(tree); err != nil {
		return nil, nil, nil, err
	}
	if maxBehind <= 0 {
		return head, nil, nil, nil
	}
	chain, walkErr := gitutil.FirstParentCommitTrees(ctx, absRepo, commit, maxBehind+1)
	if walkErr == nil && (len(chain) == 0 || chain[0].Tree != tree) {
		walkErr = errors.New("first-parent walk does not start at HEAD's tree")
	}
	if walkErr != nil {
		return head, nil, walkErr, nil
	}
	for _, ancestor := range chain[1:] {
		entries, err := entriesFor(ancestor.Tree)
		if err != nil {
			// HEAD's entries were derived the same way, so this is the walk's failure alone.
			return head, nil, err, nil
		}
		behind = append(behind, entries)
	}
	return head, behind, nil, nil
}

// AnyIndexEntryExists stats each entry through os.OpenRoot(Root), the way every cache reader opens
// it. It never opens or hashes an entry or its sidecar, which is why DescribeSearchStore (a SHA-256
// of the whole entry) is not used. Anything that stops the stat (a missing cache directory or
// entry, a component that escapes the root) is absence: a reader would miss on the same entry.
func AnyIndexEntryExists(entries []IndexEntry) bool {
	for _, entry := range entries {
		if (cacheEntry{root: entry.Root, relative: entry.Relative}).exists() {
			return true
		}
	}
	return false
}

// IndexEntryCounts reads the size recorded beside entry: the symbol and relation counts that
// `index` reports for it as counts.symbols and counts.relations, from the counts file every
// complete entry writer leaves next to the entry and every `index` brings up to date. The read is
// one bounded (4 KiB) file read through the cache root, with no git and no decode of the entry.
// Any failure, a missing file included, is ok=false.
func IndexEntryCounts(entry IndexEntry) (symbols, relations int, ok bool) {
	return readSearchSnapshotCounts(cacheEntry{root: entry.Root, relative: entry.Relative})
}

// exists stats the entry through the same confined root its readers open it through.
func (entry cacheEntry) exists() bool {
	root, err := os.OpenRoot(entry.root)
	if err != nil {
		return false
	}
	defer root.Close()
	info, err := root.Stat(entry.relative)
	return err == nil && info.Mode().IsRegular()
}

const (
	// headKeyMaxFileBytes bounds every head key input except packed-refs.
	headKeyMaxFileBytes = 1 << 20
	// headKeyMaxPackedRefsBytes bounds the streaming packed-refs scan.
	headKeyMaxPackedRefsBytes = 256 << 20
)

// HeadKeyWithoutGit digests, without running git, the on-disk inputs that decide HEAD's tree and
// the HeadIndexEntries of repo: .git/HEAD, the loose ref it names (or, when that is absent, the
// packed-refs header, the ref's line and its peeled line), .git/config (remote URLs and the
// checkout scope), the repo's .graphignore, .git/shallow (which bounds the ancestor walk), and the
// resolved max-files cap. The caller adds what
// else keys its answer (the repo and cache paths, the binary). Contents are hashed rather than
// statted, so a same-size rewrite within one mtime tick still changes the key. Git's user and
// system config are not inputs: gitutil runs git with both set to /dev/null.
//
// unborn reports that HEAD names a ref with no loose file and no packed-refs line.
//
// ok is false when the layout needs git to interpret, and the caller must take its full path:
// .git that is not a plain directory (a linked worktree, a submodule, a link), that has a
// commondir, or that lacks a HEAD file or plain refs and objects directories; a HEAD that is
// neither `ref: refs/...` nor a 40- or 64-hex id; a loose ref that is itself symbolic; a config
// with an [include] or [includeIf] section or a worktreeConfig or refStorage key
// (configNeedsGit); and any input that is a link, is not a regular file, or exceeds its read
// bound. Every read goes through an os.Root of repo with Lstat. A missing input hashes as a
// stable "absent".
func HeadKeyWithoutGit(repo string) (key string, unborn, ok bool) {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return "", false, false
	}
	defer root.Close()
	// A missing .git falls back too: git may discover a repository above repo, which none of the
	// inputs below would see change.
	if info, err := root.Lstat(".git"); err != nil || !info.IsDir() {
		return "", false, false
	}
	if _, err := root.Lstat(filepath.Join(".git", "commondir")); !isMissingPathError(err) {
		return "", false, false
	}
	// A directory git would not take for a repository falls back too: git may then discover one
	// above repo. refs and objects must be plain directories, and HEAD must be present (below).
	for _, directory := range []string{"refs", "objects"} {
		if info, err := root.Lstat(filepath.Join(".git", directory)); err != nil || !info.IsDir() {
			return "", false, false
		}
	}

	hash := sha256.New()
	writeCacheKeyString(hash, "max-files", strconv.Itoa(resolveMaxSourceFiles(0)))
	head, present, ok := readHeadKeyInput(root, filepath.Join(".git", "HEAD"))
	if !ok || !present {
		return "", false, false
	}
	writeHeadKeyInput(hash, "head", head, present)
	value := strings.TrimRight(string(head), " \t\r\n")
	if target, symbolic := strings.CutPrefix(value, "ref:"); symbolic {
		target = strings.TrimLeft(target, " \t")
		if !validHeadKeyRefName(target) {
			return "", false, false
		}
		if unborn, ok = writeHeadKeyRef(hash, root, target); !ok {
			return "", false, false
		}
	} else if !validHeadKeyObjectID(value) {
		return "", false, false
	}
	config, present, ok := readHeadKeyInput(root, filepath.Join(".git", "config"))
	if !ok {
		return "", false, false
	}
	if configNeedsGit(config) {
		return "", false, false
	}
	writeHeadKeyInput(hash, "config", config, present)
	graphIgnore, present, ok := readHeadKeyInput(root, graphIgnoreFileName)
	if !ok {
		return "", false, false
	}
	writeHeadKeyInput(hash, "graphignore", graphIgnore, present)
	// .git/shallow bounds the first-parent walk HeadIndexEntries makes, so a deepen or unshallow at
	// the same HEAD changes which ancestors it returns.
	shallow, present, ok := readHeadKeyInput(root, filepath.Join(".git", "shallow"))
	if !ok {
		return "", false, false
	}
	writeHeadKeyInput(hash, "shallow", shallow, present)
	return hex.EncodeToString(hash.Sum(nil)), unborn, true
}

// configNeedsGit reports whether a .git/config uses something the key cannot follow without git:
// an [include] or [includeIf ...] section, which reads other files, or a worktreeConfig or
// refStorage key, which moves the config or the refs. Only section headers and key names count,
// so a value, a comment or a subsection name that merely contains one of those words (a branch
// named feature/include-paths) does not. Text after a header's section name is searched for the
// two keys rather than parsed, since a quoted subsection can itself hold a `]`; a misread there
// only refuses the key, which is safe. Git reads several headers on one line, so every `[` on a
// header line is checked, and it skips a leading UTF-8 byte order mark. A line git itself rejects
// (a bare `include.path = x` key, or `[ include ]`) is not refused here, because git fails on
// such a config anyway.
func configNeedsGit(config []byte) bool {
	text := strings.TrimPrefix(string(config), string(rune(0xFEFF)))
	for _, raw := range strings.Split(text, "\n") {
		line := strings.ToLower(strings.TrimSpace(raw))
		if strings.HasPrefix(line, "[") {
			if strings.Contains(line, "worktreeconfig") || strings.Contains(line, "refstorage") {
				return true
			}
			for rest := line; ; {
				_, after, found := strings.Cut(rest, "[")
				if !found {
					break
				}
				rest = after
				end := strings.IndexAny(after, "]. \t\"")
				if end < 0 {
					end = len(after)
				}
				if section := after[:end]; section == "include" || section == "includeif" {
					return true
				}
			}
			continue
		}
		key := line
		if end := strings.IndexAny(key, "= \t#;"); end >= 0 {
			key = key[:end]
		}
		if key == "worktreeconfig" || key == "refstorage" {
			return true
		}
	}
	return false
}

// writeHeadKeyRef hashes the loose ref, or when it is absent the packed-refs lines that can name
// its value. unborn is true when neither names it.
func writeHeadKeyRef(hash io.Writer, root *os.Root, ref string) (unborn, ok bool) {
	loose, present, ok := readHeadKeyInput(root, filepath.Join(".git", filepath.FromSlash(ref)))
	if !ok {
		return false, false
	}
	writeHeadKeyInput(hash, "loose-ref", loose, present)
	if present {
		return false, !bytes.HasPrefix(bytes.TrimLeft(loose, " \t"), []byte("ref:"))
	}
	lines, found, ok := scanPackedRefs(root, ref)
	if !ok {
		return false, false
	}
	writeCacheKeyString(hash, "packed-refs", strings.Join(lines, "\n"))
	return !found, true
}

// scanPackedRefs streams .git/packed-refs and returns its header, ref's line and the `^` peeled
// line that directly follows it. An absent file has no lines.
func scanPackedRefs(root *os.Root, ref string) (lines []string, found, ok bool) {
	file, ok := openHeadKeyInput(root, filepath.Join(".git", "packed-refs"), headKeyMaxPackedRefsBytes)
	if !ok {
		return nil, false, false
	}
	if file == nil {
		return []string{"absent"}, false, true
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, headKeyMaxPackedRefsBytes+1))
	scanner.Buffer(make([]byte, 0, 64<<10), headKeyMaxFileBytes)
	read, first, previousMatched := int64(0), true, false
	for scanner.Scan() {
		line := scanner.Text()
		read += int64(len(line)) + 1
		matched := false
		switch {
		case first && strings.HasPrefix(line, "#"):
			lines = append(lines, line)
		case strings.HasPrefix(line, "^"):
			if previousMatched {
				lines = append(lines, line)
			}
		default:
			if _, name, cut := strings.Cut(line, " "); cut && name == ref {
				lines = append(lines, line)
				found, matched = true, true
			}
		}
		first, previousMatched = false, matched
	}
	if scanner.Err() != nil || read > headKeyMaxPackedRefsBytes {
		return nil, false, false
	}
	return lines, found, true
}

// readHeadKeyInput reads one key input through root. present is false for a missing input.
func readHeadKeyInput(root *os.Root, name string) (content []byte, present, ok bool) {
	file, ok := openHeadKeyInput(root, name, headKeyMaxFileBytes)
	if !ok || file == nil {
		return nil, false, ok
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, headKeyMaxFileBytes+1))
	if err != nil || len(content) > headKeyMaxFileBytes {
		return nil, false, false
	}
	return content, true, true
}

// openHeadKeyInput opens name through root after an Lstat, and only when it is a regular file with
// one name, no larger than limit, and the same file the Lstat saw. A missing input is (nil, true).
func openHeadKeyInput(root *os.Root, name string, limit int64) (*os.File, bool) {
	info, err := root.Lstat(name)
	if isMissingPathError(err) {
		return nil, true
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, false
	}
	file, err := openRootBoundedRegularFile(root, name)
	if err != nil {
		return nil, false
	}
	opened, err := file.Stat()
	if err == nil && os.SameFile(info, opened) && opened.Size() <= limit {
		if regular, err := openedFileIsRegular(file, opened); err == nil && regular {
			if links, known := openedFileLinkCount(file, opened); known && links == 1 {
				return file, true
			}
		}
	}
	_ = file.Close()
	return nil, false
}

func writeHeadKeyInput(hash io.Writer, label string, content []byte, present bool) {
	if !present {
		writeCacheKeyString(hash, label, "absent")
		return
	}
	writeCacheKeyString(hash, label, "present:"+string(content))
}

// validHeadKeyRefName accepts a ref name that git's check-ref-format would, under refs/, so it is
// safe to address as a path below .git.
func validHeadKeyRefName(ref string) bool {
	if !strings.HasPrefix(ref, "refs/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") {
		return false
	}
	for _, character := range ref {
		if character < 0x20 || character == 0x7f || strings.ContainsRune(" ~^:?*[\\", character) {
			return false
		}
	}
	for _, component := range strings.Split(ref, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

// validHeadKeyObjectID accepts a SHA-1 or SHA-256 object id.
func validHeadKeyObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
