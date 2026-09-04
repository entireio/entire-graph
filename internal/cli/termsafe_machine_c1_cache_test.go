package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSnapshotCacheReplayEscapesC1 covers the sink that does not go through an
// encoder at all.
//
// runProviderRecords answers a warm cache by replaying the stored record stream
// straight to stdout (see the sem.LoadProviderRecords branch in root.go), so
// wrapping the ENCODER leaves that path unprotected: an entry written by any
// build older than the C1 rule keeps streaming the repository's raw control on
// every hit, and the entry only expires when the provider version in its key
// changes. The replay is wrapped for that reason, and this test is what says so.
//
// The cache is poisoned on disk rather than by running an old binary, so the test
// needs nothing but this tree. It reads the entry the first run wrote, turns the
// escape back into the raw code point, and puts it back.
func TestSnapshotCacheReplayEscapesC1(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	repo, hostilePath := c1HostileRepo(t)
	if !hostilePath {
		// A snapshot carries no source text, so with no hostile PATHNAME there is no
		// C1 in the stream to poison the cache with. See c1HostileRepo.
		t.Skip("pathname channel unavailable on this platform")
	}
	cacheDir := t.TempDir()

	// First run: a miss, which populates the cache with the escaped stream.
	stdout, _ := runVerb(t, repo, cacheDir, []string{"snapshot", "--format", "ndjson"})
	assertNoRawC1(t, stdout)
	if !strings.Contains(stdout, "\\u009d") {
		t.Fatalf("first run wrote no escape, so there is nothing to poison:\n%s", stdout)
	}

	poisonProviderRecordsCache(t, cacheDir)

	// Second run: a hit, answered entirely from the poisoned entry.
	replayed, _ := runVerb(t, repo, cacheDir, []string{"snapshot", "--format", "ndjson"})
	assertNoRawC1(t, replayed)
	assertCarriesC1AfterDecoding(t, replayed)
	if replayed != stdout {
		t.Errorf("replayed stream differs from the stream that was cached:\n got  %q\n want %q", replayed, stdout)
	}
}

// poisonProviderRecordsCache rewrites the one cached record stream under cacheDir
// so its escaped C1 code points are raw again — the shape an entry written before
// this rule existed has on disk.
func poisonProviderRecordsCache(t *testing.T, cacheDir string) {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(cacheDir, "records", "*", "*.json.gz"))
	if err != nil {
		t.Fatalf("glob cache: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one cached record stream, found %d: %v", len(entries), entries)
	}

	raw := readGzip(t, entries[0])
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("decode cache entry: %v", err)
	}
	var encoded string
	if err := json.Unmarshal(entry["records"], &encoded); err != nil {
		t.Fatalf("decode records field: %v", err)
	}
	records, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode records payload: %v", err)
	}

	poisoned := bytes.ReplaceAll(records, []byte("\\u009d"), []byte("\u009d"))
	poisoned = bytes.ReplaceAll(poisoned, []byte("\\u009c"), []byte("\u009c"))
	if bytes.Equal(poisoned, records) {
		t.Fatal("cached stream held no escape to turn back into a raw control")
	}
	if indexC1(string(poisoned)) < 0 {
		t.Fatal("poisoned stream holds no raw C1, so the replay would prove nothing")
	}

	repoisoned, err := json.Marshal(base64.StdEncoding.EncodeToString(poisoned))
	if err != nil {
		t.Fatalf("re-encode records: %v", err)
	}
	entry["records"] = repoisoned
	rewritten, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("re-encode cache entry: %v", err)
	}
	writeGzip(t, entries[0], append(rewritten, '\n'))
}

func readGzip(t *testing.T, path string) []byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gunzip %s: %v", path, err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func writeGzip(t *testing.T, path string, content []byte) {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(content); err != nil {
		t.Fatalf("gzip %s: %v", path, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close gzip %s: %v", path, err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
