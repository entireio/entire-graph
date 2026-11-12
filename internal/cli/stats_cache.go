package cli

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strconv"

	"github.com/entireio/entire-graph/internal/sem"
)

// The stats memo. Transcripts are append-mostly and a finished one never changes again, so the
// expensive half of `entire graph stats` — JSON-parsing gigabytes of session log — is pure
// repeat work on every run but the first. This caches ONE fileSummary per transcript file.
//
// Three properties are load-bearing:
//
//   - The key is the file's IDENTITY (path + size + mtime), never its name alone, and it is
//     salted with this binary's own identity and the summary schema. A sibling change in this
//     repo keyed a digest cache on path alone and served a stale hash after a rebuild; a memo
//     whose value depends on parsing code MUST invalidate when that code changes, and "the
//     executable's size and mtime" is the cheapest honest proxy for "the parsing code changed".
//   - Every failure recomputes. A missing, truncated, wrong-schema, or unparseable cache is
//     treated as an empty cache, never as an error and never as a reason to serve what it
//     happened to contain.
//   - The window is NOT part of the key. A summary is a whole-file fact with no notion of
//     --since; windowing happens after merge, from record timestamps. So one warm cache serves
//     every window, and no --since can ever read another's answer.
//
// v2: summaryCall carries a follow-up class (Disp), request-relative (never released).
// v3: the class is delivery-relative with error/censored/ineligible outcomes. A v2 memo's classes
// mean something else and must never be served alongside v3 ones.
// v4: named paths come from result rows only (a zero-result answer is ineligible), and a read
// span-attributed to a sibling result is a shared read ("s"), not skipped. v3 "n" over-credits.
// v5: a successful locate call carries its named paths ("p") and resolved root ("o") for the
// gross estimate. A v4 memo has neither and would price every cached result at 0.
const statsCacheSchema = "v5"

// Small scopes skip memo overhead only when both their file count and total size are small.
const statsCacheMinTranscripts = 8
const statsCacheMinTranscriptBytes = 1 << 20

// statsCacheMaxBytes bounds both compressed and decompressed cache data. A memo is a convenience;
// it is not worth unbounded memory if the file on disk is not what this code wrote.
const statsCacheMaxBytes = 256 << 20

type statsCache struct {
	root    string
	dirs    []string
	name    string
	salt    string
	entries map[string]fileSummary // what the next save writes
	changed bool
}

// statsCacheDir decides where the memo lives, following the same precedence as every other
// cache in this binary: --cache-dir, then the data directory Entire hands a plugin, then the
// platform per-user cache root. --no-cache disables it outright.
func statsCacheDir(flags statsFlags, env EntireEnv) string {
	if flags.NoCache {
		return ""
	}
	return resolveCacheDir(flags.CacheDir, env.PluginDataDir)
}

// openStatsCache loads the memo for one scope (a project transcript directory, or a single
// transcript). A nil *statsCache is a working no-op cache, so callers never branch.
func openStatsCache(dir, scope, version string, files []transcriptFile) *statsCache {
	return openStatsCacheIn(dir, "", scope, version, files)
}

// openStatsCacheIn keeps the status line memo beneath its own family without treating that
// descendant as a new trust root. All directory components use the shared confinement checks.
func openStatsCacheIn(dir, family, scope, version string, files []transcriptFile) *statsCache {
	if dir == "" || !statsCacheWorthwhile(files) {
		return nil
	}
	dirs := []string{"stats", statsCacheSchema}
	if family != "" {
		dirs = append([]string{family}, dirs...)
	}
	cache := &statsCache{
		root:    dir,
		dirs:    dirs,
		name:    statsCacheKey(scope) + ".json.gz",
		salt:    statsCacheSalt(version),
		entries: map[string]fileSummary{},
	}
	cache.load()
	return cache
}

// statsCacheWorthwhile includes large single transcripts used by status-line callers.
func statsCacheWorthwhile(files []transcriptFile) bool {
	if len(files) >= statsCacheMinTranscripts {
		return true
	}
	remaining := int64(statsCacheMinTranscriptBytes)
	for _, file := range files {
		if file.size >= remaining {
			return true
		}
		if file.size > 0 {
			remaining -= file.size
		}
	}
	return false
}

// statsCacheSalt mixes everything that can change a summary without changing a transcript: the
// summary schema, the CLI version, and the running executable's own size and modification time.
// The last of those is what makes a rebuilt development binary miss rather than trust a memo
// written by different parsing code.
func statsCacheSalt(version string) string {
	salt := statsCacheSchema + "\x00" + version
	if executable, err := os.Executable(); err == nil {
		if info, err := os.Stat(executable); err == nil {
			salt += "\x00" + executable +
				"\x00" + strconv.FormatInt(info.Size(), 10) +
				"\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
		}
	}
	return salt
}

func statsCacheKey(values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// entryKey identifies one transcript by content-relevant identity, not by name.
func (c *statsCache) entryKey(file transcriptFile) string {
	identity := file.identity
	if identity == "" {
		identity = file.path
	}
	return statsCacheKey(c.salt, identity,
		strconv.FormatInt(file.size, 10),
		strconv.FormatInt(file.modTime.UnixNano(), 10))
}

type statsCacheDocument struct {
	Schema  string                 `json:"schema"`
	Entries map[string]fileSummary `json:"entries"`
}

// load reads the memo. Every error path leaves an empty cache: recomputing is always correct,
// and serving something this code cannot fully parse never is.
func (c *statsCache) load() {
	directory, err := sem.OpenCacheDirectory(c.root, c.dirs...)
	if err != nil {
		return
	}
	defer directory.Close()
	handle, err := sem.OpenCacheRegularFile(directory, c.name)
	if err != nil {
		return
	}
	defer func() { _ = handle.Close() }()
	c.loadFrom(handle, statsCacheMaxBytes)
}

// loadFrom bounds decompression as well as disk input before accepting the memo.
func (c *statsCache) loadFrom(handle io.Reader, maxBytes int64) {
	reader, err := gzip.NewReader(io.LimitReader(handle, maxBytes))
	if err != nil {
		return
	}
	defer func() { _ = reader.Close() }()
	var document statsCacheDocument
	limited := &io.LimitedReader{R: reader, N: maxBytes + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&document); err != nil {
		return
	}
	// Decode can return a complete JSON object before gzip checks its footer.
	// Require a clean EOF, rejecting checksum errors, truncation and trailing data.
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF || limited.N == 0 {
		return
	}
	if document.Schema != statsCacheSchema || document.Entries == nil {
		return
	}
	c.entries = document.Entries
}

func (c *statsCache) lookup(file transcriptFile) (fileSummary, bool) {
	if c == nil {
		return fileSummary{}, false
	}
	summary, ok := c.entries[c.entryKey(file)]
	return summary, ok
}

func (c *statsCache) store(file transcriptFile, summary fileSummary) {
	if c == nil {
		return
	}
	c.entries[c.entryKey(file)] = summary
	c.changed = true
}

// retain drops every entry that no longer belongs to a file in scope, which is what keeps the
// memo from growing without bound as sessions are added and deleted. It is driven by the FULL
// candidate list rather than the parsed one, so a narrow --since does not evict the entries a
// later --since all will want.
func (c *statsCache) retain(files []transcriptFile) {
	if c == nil {
		return
	}
	live := make(map[string]bool, len(files))
	for _, file := range files {
		live[c.entryKey(file)] = true
	}
	for key := range c.entries {
		if !live[key] {
			delete(c.entries, key)
			c.changed = true
		}
	}
}

// save rewrites the memo atomically. It is best-effort throughout: a cache that cannot be
// written is a slower next run, never a failed report.
func (c *statsCache) save() {
	if c == nil || !c.changed {
		return
	}
	directory, err := sem.OpenCacheDirectory(c.root, c.dirs...)
	if err != nil {
		return
	}
	defer directory.Close()
	temporary, name, err := sem.CreateCacheTemp(directory, "stats")
	if err != nil {
		return
	}
	defer directory.Remove(name)
	writer := gzip.NewWriter(temporary)
	encodeErr := json.NewEncoder(writer).Encode(statsCacheDocument{
		Schema:  statsCacheSchema,
		Entries: c.entries,
	})
	closeErr := writer.Close()
	syncErr := temporary.Sync()
	if err := temporary.Close(); err != nil || encodeErr != nil || closeErr != nil || syncErr != nil {
		return
	}
	_ = directory.Rename(name, c.name)
}
