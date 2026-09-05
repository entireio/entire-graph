package sem

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const extractionDecodeLimit = 64 << 20
const extractionEntryLimit = 100000
const extractionDiskLimit int64 = 1 << 30

var extractionBuildIdentity = sync.OnceValue(func() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
})

type extractionCache struct {
	importsParsed, importsReused, importsNS                               atomic.Int64
	maxBytes                                                              int64
	maxEntries                                                            int
	limitsReady                                                           bool
	directory, repository, build                                          string
	parsed, reused, sourceBytes, cacheReadBytes, cacheWriteBytes, parseNS atomic.Int64
	quotaMu                                                               sync.Mutex
	quotaBytes                                                            int64
	quotaEntries                                                          int
	quotaReady                                                            bool
}
type ExtractionStats struct {
	RawImportsParsed  int64 `json:"raw_imports_parsed"`
	RawImportsReused  int64 `json:"raw_imports_reused"`
	RawImportsNS      int64 `json:"raw_imports_ns"`
	FilesParsed       int64 `json:"files_parsed"`
	FilesReused       int64 `json:"files_reused"`
	SourceBytesRead   int64 `json:"source_bytes_read"`
	CacheBytesRead    int64 `json:"cache_bytes_read"`
	CacheBytesWritten int64 `json:"cache_bytes_written"`
	ExtractionNS      int64 `json:"extraction_ns"`
}

func (cache *extractionCache) stats() *ExtractionStats {
	if cache == nil {
		return nil
	}
	return &ExtractionStats{RawImportsParsed: cache.importsParsed.Load(), RawImportsReused: cache.importsReused.Load(), RawImportsNS: cache.importsNS.Load(), FilesParsed: cache.parsed.Load(), FilesReused: cache.reused.Load(), SourceBytesRead: cache.sourceBytes.Load(), CacheBytesRead: cache.cacheReadBytes.Load(), CacheBytesWritten: cache.cacheWriteBytes.Load(), ExtractionNS: cache.parseNS.Load()}
}
func (cache *extractionCache) reserve(entry cacheEntry, bytes int64) bool {
	cache.quotaMu.Lock()
	defer cache.quotaMu.Unlock()
	if !cache.limitsReady {
		cache.maxBytes = extractionConfiguredLimit("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_BYTES", extractionDiskLimit)
		cache.maxEntries = int(extractionConfiguredLimit("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_ENTRIES", extractionEntryLimit))
		cache.limitsReady = true
	}
	if bytes > cache.maxBytes {
		return false
	}
	if !cache.quotaReady || cache.quotaBytes < bytes || cache.quotaEntries < 1 {
		extractionMaintenance.Lock()
		freeBytes, freeEntries, ok := maintainExtractionCache(entry, bytes, cache.maxBytes, cache.maxEntries)
		extractionMaintenance.Unlock()
		if !ok {
			return false
		}
		cache.quotaBytes, cache.quotaEntries, cache.quotaReady = freeBytes, freeEntries, true
	}
	cache.quotaBytes -= bytes
	cache.quotaEntries--
	return true
}

type extractionEnvelope struct {
	Key           string
	PayloadDigest string
	Record        extractionRecord
}

func extractionIdentity(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		h.Write(size[:])
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (cache *extractionCache) entry(spec profileSpec, language languageSpec, source capturedSource, limit int) (cacheEntry, string, error) {
	key := extractionIdentity(cache.repository, source.path, source.digest, language.language, string(spec.name), strconv.Itoa(limit), strconv.Itoa(extractionFormatVersion), cache.build)
	entry, err := newCacheEntry(cache.directory, "extraction-"+extractionIdentity(cache.repository), "v1", key)
	return entry, key, err
}
func (cache *extractionCache) extract(spec profileSpec, language languageSpec, source capturedSource, limit int) (fileExtraction, bool) {
	if cache == nil || cache.build == "" || cache.directory == "" {
		if cache != nil {
			cache.parsed.Add(1)
		}
		return extractCapturedSource(spec, language, source), false
	}
	entry, key, err := cache.entry(spec, language, source, limit)
	if err == nil {
		if record, ok := loadExtraction(entry, key, cache); ok {
			cache.reused.Add(1)
			if record.RelationFamilies&extractionRawImports != 0 {
				cache.importsReused.Add(1)
			}
			return fileExtraction{entities: record.entities(), language: record.Language, status: record.Status, relationFamilies: record.RelationFamilies, rawImports: cloneExtractionStrings(record.RawImports)}, true
		}
	}
	cache.parsed.Add(1)
	start := time.Now()
	extraction := extractCapturedSource(spec, language, source)
	cache.parseNS.Add(time.Since(start).Nanoseconds())
	if rawImportsEligible(spec, source.path, extraction.language) {
		start := time.Now()
		extraction.rawImports = importsFor(source.path, source.content)
		extraction.relationFamilies |= extractionRawImports
		cache.importsParsed.Add(1)
		cache.importsNS.Add(time.Since(start).Nanoseconds())
	}
	if err == nil && extraction.language != "" && cacheableExtractionStatus(extraction.status) {
		record := recordExtraction(extraction.entities, extraction.language, extraction.status)
		record.RelationFamilies = extraction.relationFamilies
		record.RawImports = cloneExtractionStrings(extraction.rawImports)
		// Avoid allocating an unbounded serialized derivative of a bounded input.
		payload, encodeErr := json.Marshal(record)
		var roundTrip extractionRecord
		if encodeErr != nil || json.Unmarshal(payload, &roundTrip) != nil || !reflect.DeepEqual(record, roundTrip) {
			return extraction, false
		}
		envelope := extractionEnvelope{Key: key, PayloadDigest: contentHash(payload), Record: record}
		bytes, marshalErr := json.Marshal(envelope)
		if encodeErr == nil {
			encodeErr = marshalErr
		}
		if encodeErr == nil && len(bytes) < extractionDecodeLimit {
			if cache.reserve(entry, int64(len(bytes))+256) {
				if entry.write("extract", envelope) == nil {
					if file, err := entry.open(); err == nil {
						if info, err := file.Stat(); err == nil {
							cache.cacheWriteBytes.Add(info.Size())
						}
						file.Close()
					}
				}
			}
		}
	}
	return extraction, false
}
func loadExtraction(entry cacheEntry, key string, cache *extractionCache) (extractionRecord, bool) {
	file, err := openExtractionEntry(entry)
	if err != nil {
		return extractionRecord{}, false
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil {
		cache.cacheReadBytes.Add(min(info.Size(), int64(extractionDecodeLimit+1)))
	}
	zip, err := gzip.NewReader(io.LimitReader(file, extractionDecodeLimit+1))
	if err != nil {
		return extractionRecord{}, false
	}
	defer zip.Close()
	data, err := io.ReadAll(io.LimitReader(zip, extractionDecodeLimit+1))
	if err != nil || len(data) > extractionDecodeLimit {
		return extractionRecord{}, false
	}
	var envelope extractionEnvelope
	if json.Unmarshal(data, &envelope) != nil || envelope.Key != key || envelope.Record.Version != extractionFormatVersion || envelope.Record.Language == "" || (envelope.Record.RelationFamilies & ^extractionRawImports) != 0 || (envelope.Record.RelationFamilies == 0 && envelope.Record.RawImports != nil) || !cacheableExtractionStatus(envelope.Record.Status) {
		return extractionRecord{}, false
	}
	payload, err := json.Marshal(envelope.Record)
	if err != nil || contentHash(payload) != envelope.PayloadDigest {
		return extractionRecord{}, false
	}
	return envelope.Record, true
}

var extractionMaintenance sync.Mutex

// Maintenance follows held directory capabilities, and removes only internally
// named regular entries. Admission fails closed when bounded scanning is exceeded.
func maintainExtractionCache(entry cacheEntry, incoming, maxBytes int64, maxEntries int) (int64, int, bool) {
	if os.MkdirAll(entry.root, 0700) != nil {
		return 0, 0, false
	}
	root, err := os.OpenRoot(entry.root)
	if err != nil {
		return 0, 0, false
	}
	defer root.Close()
	dir, err := openCacheDirectory(root, filepath.Dir(entry.relative))
	if err != nil {
		return 0, 0, false
	}
	defer dir.Close()
	file, err := dir.Open(".")
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	entries, err := file.ReadDir(extractionEntryLimit + 1)
	if err != nil && err != io.EOF {
		return 0, 0, false
	}
	if len(entries) > extractionEntryLimit {
		return 0, 0, false
	}
	type item struct {
		name     string
		size     int64
		modified int64
	}
	var items []item
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json.gz") || !validSHA256Hex(strings.TrimSuffix(name, ".json.gz")) {
			continue
		}
		info, err := dir.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		items = append(items, item{name, info.Size(), info.ModTime().UnixNano()})
		total += info.Size()
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].modified != items[j].modified {
			return items[i].modified < items[j].modified
		}
		return items[i].name < items[j].name
	})
	remaining := len(items)
	for _, item := range items {
		if total+incoming <= maxBytes*9/10 && remaining < max(1, maxEntries*9/10) {
			break
		}
		if dir.Remove(item.name) != nil {
			return 0, 0, false
		}
		total -= item.size
		remaining--
	}
	return maxBytes - total, maxEntries - remaining, total+incoming <= maxBytes && remaining < maxEntries
}

// Reads refuse redirected descendants as writes do, without creating directories.
func openExtractionEntry(entry cacheEntry) (*os.File, error) {
	root, err := os.OpenRoot(entry.root)
	if err != nil {
		return nil, err
	}
	current := root
	defer func() { current.Close() }()
	for _, component := range strings.Split(filepath.ToSlash(filepath.Dir(entry.relative)), "/") {
		next, err := current.OpenRoot(component)
		if err != nil {
			return nil, err
		}
		if err := refuseRedirectingCacheComponent(current, component, next); err != nil {
			next.Close()
			return nil, err
		}
		current.Close()
		current = next
	}
	name := filepath.Base(entry.relative)
	named, err := current.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !named.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	file, err := current.Open(name)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(named, actual) {
		file.Close()
		return nil, os.ErrInvalid
	}
	return file, nil
}

// Cache only fully computed syntax results, including explicitly certified
// malformed-input diagnostics. Generic E_PARSE_ERROR alone is insufficient.
func cacheableExtractionStatus(status ParseStatus) bool {
	if status.Partial || status.DepthExceeded {
		return false
	}
	if status.DeterministicSyntaxError {
		return status.ParseError && status.Code == "E_PARSE_ERROR"
	}
	return !status.ParseError
}

// Limit the first family to the measured languages and existing import scanner.
// Other languages and syntax-only profiles explicitly leave the family absent.
func rawImportsEligible(spec profileSpec, path, language string) bool {
	if !spec.emits("IMPORTS") || (language != "Go" && language != "TypeScript" && language != "Python") {
		return false
	}
	_, ok := importScanners[strings.ToLower(filepath.Ext(path))]
	return ok
}

// Overrides can only tighten the hard safety ceilings. Invalid, zero, negative,
// and above-ceiling values conservatively select the established default.
func extractionConfiguredLimit(name string, ceiling int64) int64 {
	value, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || value <= 0 || value > ceiling {
		return ceiling
	}
	return value
}
