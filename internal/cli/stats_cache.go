package cli

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
const statsCacheSchema = "v1"

// statsCacheMinTranscripts is the scope size below which the memo is skipped: reading, decoding
// and rewriting the scope file costs more than re-parsing a handful of transcripts.
const statsCacheMinTranscripts = 8

// statsCacheMaxBytes bounds how much compressed cache is read back. A memo is a convenience;
// it is not worth unbounded memory if the file on disk is not what this code wrote.
const statsCacheMaxBytes = 256 << 20

type statsCache struct {
	path    string
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
func openStatsCache(dir, scope, version string, candidates int) *statsCache {
	if dir == "" || candidates < statsCacheMinTranscripts {
		return nil
	}
	cache := &statsCache{
		path:    filepath.Join(dir, "stats", statsCacheSchema, statsCacheKey(scope)+".json.gz"),
		salt:    statsCacheSalt(version),
		entries: map[string]fileSummary{},
	}
	cache.load()
	return cache
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
	return statsCacheKey(c.salt, file.path,
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
	handle, err := os.Open(c.path) //nolint:gosec // path is derived from the resolved cache directory
	if err != nil {
		return
	}
	defer func() { _ = handle.Close() }()
	reader, err := gzip.NewReader(io.LimitReader(handle, statsCacheMaxBytes))
	if err != nil {
		return
	}
	defer func() { _ = reader.Close() }()
	var document statsCacheDocument
	if err := json.NewDecoder(reader).Decode(&document); err != nil {
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
	directory := filepath.Dir(c.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return
	}
	temporary, err := os.CreateTemp(directory, ".stats-*.tmp")
	if err != nil {
		return
	}
	name := temporary.Name()
	writer := gzip.NewWriter(temporary)
	encodeErr := json.NewEncoder(writer).Encode(statsCacheDocument{
		Schema:  statsCacheSchema,
		Entries: c.entries,
	})
	closeErr := writer.Close()
	syncErr := temporary.Sync()
	if err := temporary.Close(); err != nil || encodeErr != nil || closeErr != nil || syncErr != nil {
		_ = os.Remove(name)
		return
	}
	if err := os.Rename(name, c.path); err != nil {
		_ = os.Remove(name)
	}
}
