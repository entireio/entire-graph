// Package filedigest computes a file's content identity (SHA-256) and line
// count in constant memory.
//
// It exists for one purpose: a file too large to materialize must still be
// describable. Reading such a file to hash it is what makes a snapshot's memory
// scale with the largest file in the repository, so every consumer that only
// needs the file's identity streams it through here instead.
package filedigest

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// streamBufferBytes is the fixed window a digest holds at any instant,
// independent of the file's size.
const streamBufferBytes = 64 * 1024

// Digest is a file's content identity and shape, computed without holding the
// content.
type Digest struct {
	Bytes int64
	Hash  string
	Lines int
}

// Stream reads r to EOF and returns its digest. Memory stays at
// streamBufferBytes regardless of how much r yields.
//
// Lines matches the snapshot's line count: the number of newline-terminated
// lines, plus a final unterminated line when one is present, and zero for empty
// input.
func Stream(r io.Reader) (Digest, error) {
	hasher := sha256.New()
	buf := make([]byte, streamBufferBytes)
	digest := Digest{}
	var last byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = hasher.Write(chunk)
			digest.Bytes += int64(n)
			for _, b := range chunk {
				if b == '\n' {
					digest.Lines++
				}
			}
			last = chunk[n-1]
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return Digest{}, err
		}
	}
	if digest.Bytes > 0 && last != '\n' {
		digest.Lines++
	}
	digest.Hash = hex.EncodeToString(hasher.Sum(nil))
	return digest, nil
}

// File digests the file at path.
func File(path string) (Digest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Digest{}, err
	}
	defer func() { _ = file.Close() }()
	return Stream(file)
}
