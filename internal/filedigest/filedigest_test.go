package filedigest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStreamMatchesWholeContentHashAndLineCount(t *testing.T) {
	cases := map[string]string{
		"empty":               "",
		"no trailing newline": "one\ntwo",
		"trailing newline":    "one\ntwo\n",
		"binary":              "\x00\x01\x02\n\x00",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			digest, err := Stream(strings.NewReader(content))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(content))
			if digest.Hash != hex.EncodeToString(sum[:]) {
				t.Fatalf("hash = %s, want %s", digest.Hash, hex.EncodeToString(sum[:]))
			}
			if digest.Bytes != int64(len(content)) {
				t.Fatalf("bytes = %d, want %d", digest.Bytes, len(content))
			}
			wantLines := strings.Count(content, "\n")
			if len(content) > 0 && !strings.HasSuffix(content, "\n") {
				wantLines++
			}
			if digest.Lines != wantLines {
				t.Fatalf("lines = %d, want %d", digest.Lines, wantLines)
			}
		})
	}
}

// TestFileDigestsWithoutHoldingTheFile is the property the package exists for:
// digesting a large file must not allocate anything close to its size.
func TestFileDigestsWithoutHoldingTheFile(t *testing.T) {
	const size = 64 << 20
	path := filepath.Join(t.TempDir(), "huge.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("header\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	digest, err := File(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)

	if digest.Bytes != size {
		t.Fatalf("bytes = %d, want %d", digest.Bytes, size)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("digesting a %d-byte file allocated %d bytes, want under 1 MiB", size, allocated)
	}
}

func TestFileAcceptsArbitraryCallerPath(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	nested := filepath.Join(base, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "..payload.txt")
	if err := os.WriteFile(target, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	separator := string(filepath.Separator)
	callerPath := nested + separator + ".." + separator + "..payload.txt"
	digest, err := File(callerPath)
	if err != nil {
		t.Fatalf("File(%q): %v", callerPath, err)
	}
	if digest.Bytes != 8 || digest.Lines != 2 {
		t.Fatalf("digest = %+v, want 8 bytes and 2 lines", digest)
	}
}
