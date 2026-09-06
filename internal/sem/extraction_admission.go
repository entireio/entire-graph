package sem

import (
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

type extractionAdmissionItem struct {
	size     int64
	modified int64
}

// extractionAdmissionSession is an operation-scoped quota view. Every fact in
// the view is observed or changed through held.directory while held.file owns
// the nonblocking admission lock. Closing discards the view; it is never a
// persistent quota ledger.
type extractionAdmissionSession struct {
	held       *extractionAdmissionLock
	items      map[string]extractionAdmissionItem
	totalBytes int64
	maxBytes   int64
	maxEntries int
}

func beginExtractionAdmissionSession(ctx context.Context, entry cacheEntry, maxBytes int64, maxEntries int) (*extractionAdmissionSession, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	held, err := lockExtractionAdmission(entry)
	if err != nil {
		return nil, err
	}
	session := &extractionAdmissionSession{
		held:       held,
		items:      make(map[string]extractionAdmissionItem),
		maxBytes:   maxBytes,
		maxEntries: maxEntries,
	}
	if err := session.inventory(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func (session *extractionAdmissionSession) Close() error {
	if session == nil {
		return nil
	}
	err := session.held.Close()
	session.held = nil
	session.items = nil
	session.totalBytes = 0
	session.maxBytes = 0
	session.maxEntries = 0
	return err
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (session *extractionAdmissionSession) inventory(ctx context.Context) error {
	file, err := session.held.directory.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	entries, err := file.ReadDir(extractionEntryLimit + 2)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) > extractionEntryLimit+1 {
		return os.ErrInvalid
	}
	for _, candidate := range entries {
		if err := contextErr(ctx); err != nil {
			return err
		}
		name := candidate.Name()
		temporary := strings.TrimSuffix(strings.TrimPrefix(name, ".extract-"), ".json.gz")
		orphan := strings.HasPrefix(name, ".extract-") && strings.HasSuffix(name, ".json.gz") && len(temporary) == 32 && validSHA256Hex(temporary+temporary)
		if !orphan && !validExtractionCacheFilename(name) {
			continue
		}
		info, err := session.held.directory.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if orphan {
			if err := session.held.directory.Remove(name); err != nil {
				return err
			}
			continue
		}
		if info.Size() < 0 || session.totalBytes > math.MaxInt64-info.Size() {
			return os.ErrInvalid
		}
		session.items[name] = extractionAdmissionItem{size: info.Size(), modified: info.ModTime().UnixNano()}
		session.totalBytes += info.Size()
	}
	return nil
}

func validExtractionCacheFilename(name string) bool {
	return strings.HasSuffix(name, ".json.gz") && validSHA256Hex(strings.TrimSuffix(name, ".json.gz")) && filepath.Base(name) == name
}

func (session *extractionAdmissionSession) publish(ctx context.Context, pending extractionPending) (int64, bool, error) {
	writtenBytes, written, err := session.publishBatch(ctx, []extractionPending{pending})
	return writtenBytes, written == 1, err
}

func (session *extractionAdmissionSession) publishBatch(ctx context.Context, pending []extractionPending) (int64, int, error) {
	if err := contextErr(ctx); err != nil {
		return 0, 0, err
	}
	if session == nil || session.held == nil {
		return 0, 0, os.ErrInvalid
	}
	targets := make(map[string]struct{}, len(pending))
	for _, item := range pending {
		if !session.held.holds(item.entry) {
			return 0, 0, os.ErrInvalid
		}
		name := filepath.Base(item.entry.relative)
		if !validExtractionCacheFilename(name) || item.bound <= 0 || item.bound > session.maxBytes {
			return 0, 0, nil
		}
		targets[name] = struct{}{}
	}
	// Reserve every pending gzip bound on top of current persistent occupancy.
	// Atomic replacement temporarily holds both the old entry and its new temp,
	// so a later shrink cannot fund an earlier write. Summing duplicate-key
	// bounds is deliberately conservative for the same reason. Entry quota
	// counts only distinct new persistent names; temporary files are covered by
	// the byte reservation and removed or renamed before the next write.
	prospectiveBytes := session.totalBytes
	prospectiveEntries := len(session.items)
	newNames := make(map[string]struct{}, len(targets))
	for _, item := range pending {
		name := filepath.Base(item.entry.relative)
		if prospectiveBytes > math.MaxInt64-item.bound {
			return 0, 0, os.ErrInvalid
		}
		prospectiveBytes += item.bound
		if _, exists := session.items[name]; !exists {
			if _, counted := newNames[name]; !counted {
				newNames[name] = struct{}{}
				prospectiveEntries++
			}
		}
	}
	byteTarget := session.maxBytes * 9 / 10
	entryTarget := max(1, session.maxEntries*9/10)
	needsEviction := prospectiveBytes > byteTarget || prospectiveEntries >= entryTarget
	if needsEviction {
		candidates := make([]extractionMaintenanceItem, 0, len(session.items))
		for candidateName, item := range session.items {
			if _, targeted := targets[candidateName]; targeted {
				continue
			}
			candidates = append(candidates, extractionMaintenanceItem{name: candidateName, size: item.size, modified: item.modified})
		}
		extractionMaintenanceSort(candidates)
		for _, candidate := range candidates {
			if prospectiveBytes <= byteTarget && prospectiveEntries < entryTarget {
				break
			}
			if err := contextErr(ctx); err != nil {
				return 0, 0, err
			}
			if err := session.held.directory.Remove(candidate.name); err != nil {
				return 0, 0, err
			}
			delete(session.items, candidate.name)
			if candidate.size < 0 || candidate.size > session.totalBytes || candidate.size > prospectiveBytes {
				return 0, 0, os.ErrInvalid
			}
			session.totalBytes -= candidate.size
			prospectiveBytes -= candidate.size
			prospectiveEntries--
		}
	}
	if prospectiveBytes > session.maxBytes || prospectiveEntries > session.maxEntries {
		return 0, 0, nil
	}
	var writtenBytes int64
	written := 0
	for _, item := range pending {
		if err := contextErr(ctx); err != nil {
			return writtenBytes, written, err
		}
		name := filepath.Base(item.entry.relative)
		size, modified, err := item.entry.writeEncodedHeld(session.held.directory, "extract", item.encoded, item.bound)
		if err != nil {
			return writtenBytes, written, err
		}
		if previous, replacing := session.items[name]; replacing {
			if previous.size < 0 || previous.size > session.totalBytes {
				return writtenBytes, written, os.ErrInvalid
			}
			session.totalBytes -= previous.size
		}
		if size < 0 || session.totalBytes > math.MaxInt64-size {
			return writtenBytes, written, os.ErrInvalid
		}
		session.items[name] = extractionAdmissionItem{size: size, modified: modified}
		session.totalBytes += size
		writtenBytes += size
		written++
	}
	return writtenBytes, written, nil
}
