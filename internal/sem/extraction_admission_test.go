package sem

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func pendingWithEncoded(entry cacheEntry, encoded []byte) extractionPending {
	bound := int64(len(encoded) + len(encoded)/16384*5 + 1024)
	return extractionPending{entry: entry, bound: bound, encoded: encoded}
}

func deterministicAdmissionBytes(size int, seed uint64) []byte {
	data := make([]byte, size)
	state := seed
	for index := range data {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		data[index] = byte(state)
	}
	return data
}

func freshGzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func readGzipBytes(t *testing.T, path string) []byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	return got
}

func TestExtractionAdmissionReusableWriterMatchesFreshWriter(t *testing.T) {
	root := t.TempDir()
	first, err := newCacheEntry(root, "extraction-writer-reuse", "v1", strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	held, err := lockExtractionAdmission(first)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	writer := gzip.NewWriter(io.Discard)
	payloads := [][]byte{
		[]byte("first payload with repeated repeated words"),
		deterministicAdmissionBytes(8192, 17),
		{0xff, 0xfe, 0x00, '{', '}', 0x80},
	}
	for index, payload := range payloads {
		writer.Name = "must-not-leak"
		writer.Comment = "reset must restore the default header"
		entry, err := newCacheEntry(root, "extraction-writer-reuse", "v1", fmt.Sprintf("%064x", index+1))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := entry.writeEncodedHeldWithWriter(held.directory, "extract", payload, int64(len(payload)+2048), writer); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(entry.root, entry.relative))
		if err != nil {
			t.Fatal(err)
		}
		if fresh := freshGzipBytes(t, payload); !bytes.Equal(raw, fresh) {
			t.Fatalf("payload %d reused gzip differs from fresh writer", index)
		}
		if got := readGzipBytes(t, filepath.Join(entry.root, entry.relative)); !bytes.Equal(got, payload) {
			t.Fatalf("payload %d contains cross-file bytes: got %x, want %x", index, got, payload)
		}
		if writer.Name != "" || writer.Comment != "" {
			t.Fatalf("payload %d retained gzip header state after reset: name=%q comment=%q", index, writer.Name, writer.Comment)
		}
	}
}

func TestExtractionAdmissionReusableWriterResetsAfterFailure(t *testing.T) {
	root := t.TempDir()
	bad, err := newCacheEntry(root, "extraction-writer-failure", "v1", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	held, err := lockExtractionAdmission(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := os.Mkdir(filepath.Join(root, bad.relative), 0o700); err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(io.Discard)
	if _, _, err := bad.writeEncodedHeldWithWriter(held.directory, "extract", []byte("failed payload"), 2048, writer); err == nil {
		t.Fatal("rename failure unexpectedly succeeded")
	}
	good, err := newCacheEntry(root, "extraction-writer-failure", "v1", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("successful payload after failed rename")
	if _, _, err := good.writeEncodedHeldWithWriter(held.directory, "extract", payload, 2048, writer); err != nil {
		t.Fatalf("writer was not reusable after failure: %v", err)
	}
	if got := readGzipBytes(t, filepath.Join(good.root, good.relative)); !bytes.Equal(got, payload) {
		t.Fatalf("successful payload after failure = %q, want %q", got, payload)
	}
}

func TestExtractionAdmissionSessionLazilyReusesOneWriter(t *testing.T) {
	root := t.TempDir()
	first, err := newCacheEntry(root, "extraction-writer-session", "v1", strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	session, err := beginExtractionAdmissionSession(context.Background(), first, 4096, extractionEntryLimit)
	if err != nil {
		t.Fatal(err)
	}
	if session.compressor != nil {
		t.Fatal("admission inventory eagerly allocated a compressor")
	}
	rejected := pendingWithEncoded(first, deterministicAdmissionBytes(4096, 19))
	if _, ok, err := session.publish(context.Background(), rejected); err != nil || ok {
		t.Fatalf("oversized publication = ok %v, err %v", ok, err)
	}
	if session.compressor != nil {
		t.Fatal("rejected publication allocated a compressor")
	}
	if _, ok, err := session.publish(context.Background(), pendingWithEncoded(first, []byte("first"))); err != nil || !ok {
		t.Fatalf("first publication = ok %v, err %v", ok, err)
	}
	writer := session.compressor
	if writer == nil {
		t.Fatal("successful publication did not allocate compressor")
	}
	second, err := newCacheEntry(root, "extraction-writer-session", "v1", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := session.publish(context.Background(), pendingWithEncoded(second, []byte("second"))); err != nil || !ok {
		t.Fatalf("second publication = ok %v, err %v", ok, err)
	}
	if session.compressor != writer {
		t.Fatal("session replaced its compressor between sequential writes")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if session.compressor != nil {
		t.Fatal("session close retained compressor")
	}
}

func TestExtractionAdmissionFlushReleasesAndLaterBatchRescans(t *testing.T) {
	cache := &extractionCache{
		ctx:         context.Background(),
		directory:   t.TempDir(),
		repository:  "flush-rescan",
		build:       "test",
		maxBytes:    extractionDiskLimit,
		maxEntries:  extractionEntryLimit,
		limitsReady: true,
	}
	spec := resolveProfile(ProfileFull)
	language, _ := languageForPath("a.go")
	for index := 0; index < 2; index++ {
		source := captureSource(fmt.Sprintf("f%d.go", index), "package p\nfunc A(){}\n")
		cache.extract(spec, language, source, 4096)
		cache.flush()
		cache.flush()
		if cache.admission != nil {
			t.Fatal("flush retained an admission session")
		}
		if got := cache.inventoryCalls.Load(); got != int64(index+1) {
			t.Fatalf("inventory calls after operation %d = %d, want %d", index+1, got, index+1)
		}
	}
}

func TestExtractionAdmissionReplacementUsesActualSizeAndKeepsCount(t *testing.T) {
	entry, err := newCacheEntry(t.TempDir(), "extraction-replacement", "v1", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	session, err := beginExtractionAdmissionSession(context.Background(), entry, extractionDiskLimit, extractionEntryLimit)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	first := pendingWithEncoded(entry, bytes.Repeat([]byte("a"), 32<<10))
	firstSize, ok, err := session.publish(context.Background(), first)
	if err != nil || !ok {
		t.Fatalf("first publication = size %d, ok %v, err %v", firstSize, ok, err)
	}
	secondBytes := make([]byte, 32<<10)
	for index := range secondBytes {
		secondBytes[index] = byte(index*31 + index/7)
	}
	secondSize, ok, err := session.publish(context.Background(), pendingWithEncoded(entry, secondBytes))
	if err != nil || !ok {
		t.Fatalf("replacement = size %d, ok %v, err %v", secondSize, ok, err)
	}
	if firstSize == secondSize {
		t.Fatalf("fixture did not produce materially different gzip sizes: %d", firstSize)
	}
	if len(session.items) != 1 || session.totalBytes != secondSize {
		t.Fatalf("replacement accounting = entries %d, bytes %d; want 1, %d", len(session.items), session.totalBytes, secondSize)
	}
}

func TestExtractionAdmissionReservesEveryOrderedIntermediateState(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		firstPath string
	}{
		{name: "new entry before later shrink", firstPath: "new"},
		{name: "same key growth before later shrink", firstPath: "old"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := t.TempDir()
			oldEntry, err := newCacheEntry(root, "extraction-ordered-peak", "v1", strings.Repeat("1", 64))
			if err != nil {
				t.Fatal(err)
			}
			newEntry, err := newCacheEntry(root, "extraction-ordered-peak", "v1", strings.Repeat("2", 64))
			if err != nil {
				t.Fatal(err)
			}
			oldEncoded := deterministicAdmissionBytes(5000, 1)
			if err := oldEntry.writeEncoded("extract", oldEncoded); err != nil {
				t.Fatal(err)
			}
			oldPath := filepath.Join(oldEntry.root, oldEntry.relative)
			oldBefore, err := os.ReadFile(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			oldInfo, err := os.Stat(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			firstEntry := newEntry
			if fixture.firstPath == "old" {
				firstEntry = oldEntry
			}
			first := pendingWithEncoded(firstEntry, deterministicAdmissionBytes(3000, 2))
			shrink := pendingWithEncoded(oldEntry, deterministicAdmissionBytes(32, 3))
			quota := max(oldInfo.Size(), first.bound+shrink.bound)
			if fixture.firstPath == "old" {
				quota = max(oldInfo.Size(), first.bound, shrink.bound)
			}
			reservedPeak := oldInfo.Size() + first.bound + shrink.bound
			if quota >= reservedPeak {
				t.Fatalf("invalid fixture reservations: quota=%d peak=%d", quota, reservedPeak)
			}
			session, err := beginExtractionAdmissionSession(context.Background(), oldEntry, quota, extractionEntryLimit)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			writtenBytes, written, err := session.publishBatch(context.Background(), []extractionPending{first, shrink})
			if err != nil {
				t.Fatal(err)
			}
			if written != 0 || writtenBytes != 0 {
				t.Fatalf("over-quota intermediate state published: entries=%d bytes=%d", written, writtenBytes)
			}
			oldAfter, err := os.ReadFile(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(oldBefore, oldAfter) {
				t.Fatal("refused ordered batch changed existing entry")
			}
			if fixture.firstPath == "new" {
				if _, err := os.Stat(filepath.Join(newEntry.root, newEntry.relative)); !os.IsNotExist(err) {
					t.Fatalf("new entry was written before future shrink: %v", err)
				}
			}
		})
	}
}

func TestExtractionAdmissionFailedPublicationInvalidatesAccounting(t *testing.T) {
	directory := t.TempDir()
	cache := &extractionCache{
		ctx:         context.Background(),
		directory:   directory,
		repository:  "failed-publication",
		build:       "test",
		maxBytes:    extractionDiskLimit,
		maxEntries:  extractionEntryLimit,
		limitsReady: true,
	}
	entry, err := newCacheEntry(directory, "extraction-failure", "v1", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(entry.root, entry.relative)
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	cache.publishBatch([]extractionPending{pendingWithEncoded(entry, []byte("first"))})
	if cache.admission != nil || cache.cacheWriteBytes.Load() != 0 {
		t.Fatalf("failed rename retained session or success stats: session=%v bytes=%d", cache.admission != nil, cache.cacheWriteBytes.Load())
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".extract-*.json.gz")); err != nil || len(matches) != 0 {
		t.Fatalf("failed rename left temporary files: %v, %v", matches, err)
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	cache.publishBatch([]extractionPending{pendingWithEncoded(entry, []byte("second"))})
	defer cache.releaseAdmission()
	if cache.cacheWriteBytes.Load() <= 0 || cache.inventoryCalls.Load() != 2 {
		t.Fatalf("retry did not reacquire and publish: inventories=%d bytes=%d", cache.inventoryCalls.Load(), cache.cacheWriteBytes.Load())
	}
}

func TestExtractionAdmissionInsufficientReservationPreservesReplacement(t *testing.T) {
	entry, err := newCacheEntry(t.TempDir(), "extraction-reservation", "v1", strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := entry.writeEncoded("extract", []byte("old")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(entry.root, entry.relative))
	if err != nil {
		t.Fatal(err)
	}
	cache := &extractionCache{ctx: context.Background(), maxBytes: extractionDiskLimit, maxEntries: extractionEntryLimit, limitsReady: true}
	cache.publishBatch([]extractionPending{{entry: entry, bound: 1, encoded: bytes.Repeat([]byte("new"), 1024)}})
	after, err := os.ReadFile(filepath.Join(entry.root, entry.relative))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || cache.cacheWriteBytes.Load() != 0 || cache.admission != nil {
		t.Fatalf("insufficient reservation changed replacement or retained accounting: equal=%v bytes=%d session=%v", bytes.Equal(before, after), cache.cacheWriteBytes.Load(), cache.admission != nil)
	}
}

func TestExtractionAdmissionHeldDirectoryAndNextSessionRevalidation(t *testing.T) {
	for _, replacement := range []string{"directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			entry, err := newCacheEntry(t.TempDir(), "extraction-held", "v1", strings.Repeat("d", 64))
			if err != nil {
				t.Fatal(err)
			}
			session, err := beginExtractionAdmissionSession(context.Background(), entry, extractionDiskLimit, extractionEntryLimit)
			if err != nil {
				t.Fatal(err)
			}
			versionPath := filepath.Join(entry.root, filepath.Dir(entry.relative))
			movedPath := versionPath + "-moved"
			if err := os.Rename(versionPath, movedPath); err != nil {
				t.Fatal(err)
			}
			var replacementTarget string
			if replacement == "directory" {
				if err := os.Mkdir(versionPath, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				replacementTarget = t.TempDir()
				if err := os.Symlink(replacementTarget, versionPath); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok, err := session.publish(context.Background(), pendingWithEncoded(entry, []byte("held"))); err != nil || !ok {
				t.Fatalf("held publication = ok %v, err %v", ok, err)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			name := filepath.Base(entry.relative)
			if _, err := os.Stat(filepath.Join(movedPath, name)); err != nil {
				t.Fatalf("held directory did not receive publication: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(versionPath, name)); !os.IsNotExist(err) {
				t.Fatalf("replacement path received held publication: %v", err)
			}
			next, err := beginExtractionAdmissionSession(context.Background(), entry, extractionDiskLimit, extractionEntryLimit)
			if replacement == "symlink" {
				if err == nil {
					next.Close()
					t.Fatal("next session followed replacement symlink")
				}
				if _, err := os.ReadDir(replacementTarget); err != nil {
					t.Fatal(err)
				}
			} else {
				if err != nil {
					t.Fatalf("next session did not revalidate ordinary replacement: %v", err)
				}
				next.Close()
			}
		})
	}
}

func TestExtractionAdmissionCancellationWithEmptyPendingReleases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cache := &extractionCache{ctx: ctx, maxBytes: extractionDiskLimit, maxEntries: extractionEntryLimit, limitsReady: true}
	entry, err := newCacheEntry(t.TempDir(), "extraction-cancel-release", "v1", strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	cache.publishBatch([]extractionPending{pendingWithEncoded(entry, []byte("published"))})
	if cache.admission == nil {
		t.Fatal("publication did not hold admission session")
	}
	cancel()
	cache.flush()
	lock, err := lockExtractionAdmission(entry)
	if err != nil {
		t.Fatalf("cancelled empty flush retained lock: %v", err)
	}
	lock.Close()
}

func TestExtractionAdmissionSubprocessContentionAndExitRelease(t *testing.T) {
	if root := os.Getenv("ENTIRE_GRAPH_TEST_ADMISSION_ROOT"); root != "" {
		entry, err := newCacheEntry(root, "extraction-process", "v1", strings.Repeat("f", 64))
		if err != nil {
			t.Fatal(err)
		}
		lock, err := lockExtractionAdmission(entry)
		if os.Getenv("ENTIRE_GRAPH_TEST_ADMISSION_EXPECT") == "blocked" {
			if err == nil {
				lock.Close()
				t.Fatal("contending child acquired admission")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		orphan := ".extract-" + strings.Repeat("a", 32) + ".json.gz"
		if err := os.WriteFile(filepath.Join(root, "extraction-process", "v1", orphan), []byte("crashed"), 0o600); err != nil {
			t.Fatal(err)
		}
		return // Process exit releases the lock without Close.
	}
	root := t.TempDir()
	entry, err := newCacheEntry(root, "extraction-process", "v1", strings.Repeat("f", 64))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := lockExtractionAdmission(entry)
	if err != nil {
		t.Fatal(err)
	}
	runChild := func(expect string) {
		t.Helper()
		command := exec.Command(os.Args[0], "-test.run=^TestExtractionAdmissionSubprocessContentionAndExitRelease$", "-test.count=1")
		command.Env = append(os.Environ(), "ENTIRE_GRAPH_TEST_ADMISSION_ROOT="+root, "ENTIRE_GRAPH_TEST_ADMISSION_EXPECT="+expect)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("child %s: %v\n%s", expect, err, output)
		}
	}
	runChild("blocked")
	owner.Close()
	runChild("available")
	session, err := beginExtractionAdmissionSession(context.Background(), entry, extractionDiskLimit, extractionEntryLimit)
	if err != nil {
		t.Fatalf("child exit retained lock: %v", err)
	}
	defer session.Close()
	orphan := filepath.Join(root, "extraction-process", "v1", ".extract-"+strings.Repeat("a", 32)+".json.gz")
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("owned orphan survived next inventory: %v", err)
	}
}
