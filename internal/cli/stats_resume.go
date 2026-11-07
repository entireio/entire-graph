package cli

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"path/filepath"
)

// transcriptResume keeps one checkpoint per transcript under <cache>/statusline, so the render
// after an append parses only the appended lines. Claude Code appends to a transcript after every
// message, which misses the figures key and the memo (both keyed on size and mtime) every time.
// Only a transcript of statsCacheMinTranscriptBytes or more gets one: a smaller one parses in a few
// milliseconds, and a checkpoint per small subagent transcript would only add files to sweep.
type transcriptResume struct {
	dir  string // <cache>/statusline
	salt string // statsCacheSalt: a checkpoint other parsing code wrote is never resumed
}

// newTranscriptResume returns nil, which parses every transcript in full, when there is no cache
// directory.
func newTranscriptResume(cacheDir, version string) *transcriptResume {
	if cacheDir == "" {
		return nil
	}
	return &transcriptResume{dir: filepath.Join(cacheDir, "statusline"), salt: statsCacheSalt(version)}
}

// transcriptCheckpoint is a fileScanner paused at Offset, the end of a transcript's last complete
// line, stored as the scanner itself (its fields are its whole state). Prefix is the SHA-256 of the
// file's first Offset bytes.
type transcriptCheckpoint struct {
	Salt     string       `json:"salt"`
	Identity string       `json:"identity"`
	Offset   int64        `json:"offset"`
	Prefix   string       `json:"prefix"`
	Scanner  *fileScanner `json:"scanner"`
}

func (r *transcriptResume) path(identity string) string {
	return filepath.Join(r.dir, "resume-"+statsCacheKey(identity)+".json")
}

// summarise returns what summariseTranscript returns for path's current bytes. When path's
// checkpoint was written by this code and the file still starts with the bytes it covered, it
// parses only what follows; otherwise (a rewrite, a truncation, no checkpoint) it parses from the
// start. When this call read at least one new complete line and the scan did not stop (on a line
// over transcriptMaxLineBytes or a read error), it writes a checkpoint at the end of the last
// complete line, before the unterminated tail (if any) is fed to the scanner; otherwise the
// existing checkpoint, if any, is left as it was.
func (r *transcriptResume) summarise(path string) (fileSummary, bool) {
	handle, err := os.Open(path) //nolint:gosec // the path comes from listing the session's own transcripts
	if err != nil {
		return fileSummary{}, false
	}
	defer func() { _ = handle.Close() }()
	digest := sha256.New()
	scanner, offset := r.resumeAt(handle, path, digest)
	if scanner == nil {
		if _, err := handle.Seek(0, io.SeekStart); err != nil {
			return fileSummary{}, false
		}
		digest.Reset()
		scanner, offset = newFileScanner(), 0
	}
	consumed, tail, stopped := consumeTranscriptLines(scanner, bufio.NewReaderSize(handle, 64*1024), digest)
	if !stopped && consumed > 0 {
		// Saved now, encoded before the tail below changes the scanner: a later append may
		// complete that line.
		saveStatuslineJSON(r.path(path), transcriptCheckpoint{
			Salt: r.salt, Identity: path, Offset: offset + consumed,
			Prefix: hex.EncodeToString(digest.Sum(nil)), Scanner: scanner,
		})
	}
	scanner.consumeLine(tail)
	scanner.finishFollowUp()
	return scanner.Summary, true
}

// resumeAt returns the scanner path's checkpoint holds and its offset, with handle positioned at
// that offset and digest holding the hash of the bytes before it. It returns nil when there is no
// usable checkpoint: none, another binary's, another file's, or one whose leading bytes the file
// no longer has.
func (r *transcriptResume) resumeAt(handle *os.File, path string, digest hash.Hash) (*fileScanner, int64) {
	checkpoint := transcriptCheckpoint{Scanner: newFileScanner()}
	if !loadStatuslineJSON(r.path(path), &checkpoint) || checkpoint.Salt != r.salt ||
		checkpoint.Identity != path || checkpoint.Offset <= 0 || checkpoint.Scanner == nil {
		return nil, 0
	}
	if copied, err := io.CopyN(digest, handle, checkpoint.Offset); err != nil || copied != checkpoint.Offset {
		return nil, 0
	}
	if hex.EncodeToString(digest.Sum(nil)) != checkpoint.Prefix {
		return nil, 0
	}
	return checkpoint.Scanner, checkpoint.Offset
}
