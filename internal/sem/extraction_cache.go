package sem

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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

// A cold extraction can produce one cache record per source file. Keep only a
// bounded batch in memory, then perform one quota scan for that batch instead
// of rescanning the whole extraction directory for every file.
const extractionPublishBatchEntries = 128
const extractionPublishBatchBytes int64 = 16 << 20

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
	ctx                                                                   context.Context
	parsed, reused, sourceBytes, cacheReadBytes, cacheWriteBytes, parseNS atomic.Int64
	quotaMu                                                               sync.Mutex
	pendingMu                                                             sync.Mutex
	pending                                                               []extractionPending
	pendingBytes                                                          int64
	maintenanceCalls                                                      atomic.Int64
}

type extractionPending struct {
	entry   cacheEntry
	bound   int64
	encoded []byte
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

// limits resolves the per-cache quota once. Admission itself is held across a
// bounded publication batch below; the reservation never outlives that lock.
func (cache *extractionCache) limits() (int64, int) {
	cache.quotaMu.Lock()
	defer cache.quotaMu.Unlock()
	if !cache.limitsReady {
		cache.maxBytes = extractionConfiguredLimit("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_BYTES", extractionDiskLimit)
		cache.maxEntries = int(extractionConfiguredLimit("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_ENTRIES", extractionEntryLimit))
		cache.limitsReady = true
	}
	return cache.maxBytes, cache.maxEntries
}

func (cache *extractionCache) enqueue(entry cacheEntry, bound int64, encoded []byte) {
	if cache == nil {
		return
	}
	cache.pendingMu.Lock()
	cache.pending = append(cache.pending, extractionPending{entry: entry, bound: bound, encoded: encoded})
	cache.pendingBytes += bound
	flush := len(cache.pending) >= extractionPublishBatchEntries || cache.pendingBytes >= extractionPublishBatchBytes
	var batch []extractionPending
	if flush {
		batch = cache.takePendingLocked()
	}
	cache.pendingMu.Unlock()
	if len(batch) > 0 {
		cache.publishBatch(batch)
	}
}

func (cache *extractionCache) takePendingLocked() []extractionPending {
	batch := cache.pending
	cache.pending = nil
	cache.pendingBytes = 0
	return batch
}

// flush publishes the final bounded batch at the operation boundary. The
// provider calls this after workers have stopped, so no producer can append
// while the drain is in progress.
func (cache *extractionCache) flush() {
	if cache == nil {
		return
	}
	for {
		cache.pendingMu.Lock()
		batch := cache.takePendingLocked()
		cache.pendingMu.Unlock()
		if len(batch) == 0 {
			return
		}
		cache.publishBatch(batch)
	}
}

func (cache *extractionCache) publishBatch(batch []extractionPending) {
	if cache == nil || len(batch) == 0 || (cache.ctx != nil && cache.ctx.Err() != nil) {
		return
	}
	maxBytes, maxEntries := cache.limits()
	extractionMaintenance.Lock()
	defer extractionMaintenance.Unlock()
	lock, err := lockExtractionAdmission(batch[0].entry)
	if err != nil {
		return
	}
	defer lock.Close() // Kernel releases the lock, including after process failure.

	// Keep each quota reservation exact and bounded. A configured quota may be
	// smaller than the normal batch, so split before maintenance rather than
	// admitting a batch that cannot fit the configured entry or byte limits.
	for start := 0; start < len(batch); {
		end := start
		var bytes int64
		for end < len(batch) && end-start < maxEntries {
			bound := batch[end].bound
			if bound > maxBytes {
				end++ // This record can never fit; retain the rest of the batch.
				continue
			}
			if end > start && bytes > maxBytes-bound {
				break
			}
			bytes += bound
			end++
		}
		if end == start {
			end++
		}
		chunk := make([]extractionPending, 0, end-start)
		for _, item := range batch[start:end] {
			if item.bound <= maxBytes {
				chunk = append(chunk, item)
			}
		}
		start = end
		if len(chunk) == 0 || (cache.ctx != nil && cache.ctx.Err() != nil) {
			continue
		}
		var incoming int64
		for _, item := range chunk {
			incoming += item.bound
		}
		cache.maintenanceCalls.Add(1)
		_, _, ok := maintainExtractionCache(batch[0].entry, incoming, len(chunk), maxBytes, maxEntries)
		if !ok || (cache.ctx != nil && cache.ctx.Err() != nil) {
			continue
		}
		for _, item := range chunk {
			if err := item.entry.writeEncoded("extract", item.encoded); err != nil {
				continue
			}
			if file, err := item.entry.open(); err == nil {
				if info, err := file.Stat(); err == nil {
					cache.cacheWriteBytes.Add(info.Size())
				}
				file.Close()
			}
		}
	}
}

type extractionEnvelope struct {
	Key           string
	PayloadDigest string
	Record        extractionRecord
}

func marshalCacheJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
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
		// JSON replaces invalid UTF-8 silently. Admit only lossless records;
		// exhaustive shape and round-trip tests protect this private contract.
		if validateExtractionRecord(record) != nil {
			return extraction, false
		}
		payload, encodeErr := json.Marshal(record)
		if encodeErr != nil {
			return extraction, false
		}
		envelope := extractionEnvelope{Key: key, PayloadDigest: contentHash(payload), Record: record}
		encoded, marshalErr := marshalCacheJSON(envelope)
		if encodeErr == nil {
			encodeErr = marshalErr
		}
		if encodeErr == nil && len(encoded) < extractionDecodeLimit {
			// DEFLATE can expand incompressible data; include block and framing
			// overhead rather than assuming gzip is always smaller than JSON.
			bound := int64(len(encoded) + len(encoded)/16384*5 + 1024)
			cache.enqueue(entry, bound, encoded)
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

type extractionMaintenanceItem struct {
	name     string
	size     int64
	modified int64
}

// extractionMaintenanceSort is a narrow test seam for proving that the
// eviction ordering work is skipped while a cache remains below both quota
// thresholds. Production always uses the deterministic age/name ordering.
var extractionMaintenanceSort = func(items []extractionMaintenanceItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].modified != items[j].modified {
			return items[i].modified < items[j].modified
		}
		return items[i].name < items[j].name
	})
}

// Maintenance follows held directory capabilities, and removes only internally
// named regular entries. Admission fails closed when bounded scanning is exceeded.
func maintainExtractionCache(entry cacheEntry, incomingBytes int64, incomingEntries int, maxBytes int64, maxEntries int) (int64, int, bool) {
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
	entries, err := file.ReadDir(extractionEntryLimit + 2)
	if err != nil && err != io.EOF {
		return 0, 0, false
	}
	if len(entries) > extractionEntryLimit+1 {
		return 0, 0, false
	}
	var items []extractionMaintenanceItem
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		temporary := strings.TrimSuffix(strings.TrimPrefix(name, ".extract-"), ".json.gz")
		orphan := strings.HasPrefix(name, ".extract-") && strings.HasSuffix(name, ".json.gz") && len(temporary) == 32 && validSHA256Hex(temporary+temporary)
		if !orphan && (!strings.HasSuffix(name, ".json.gz") || !validSHA256Hex(strings.TrimSuffix(name, ".json.gz"))) {
			continue
		}
		info, err := dir.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		// The caller holds admission, so no cooperating writer has a live
		// temporary file here. Remove only exact internally generated names.
		if orphan {
			if dir.Remove(name) != nil {
				return 0, 0, false
			}
			continue
		}
		items = append(items, extractionMaintenanceItem{name, info.Size(), info.ModTime().UnixNano()})
		total += info.Size()
	}
	needsEviction := total+incomingBytes > maxBytes*9/10 || len(items)+incomingEntries >= max(1, maxEntries*9/10)
	if needsEviction {
		extractionMaintenanceSort(items)
	}
	remaining := len(items)
	for _, item := range items {
		if total+incomingBytes <= maxBytes*9/10 && remaining+incomingEntries < max(1, maxEntries*9/10) {
			break
		}
		if dir.Remove(item.name) != nil {
			return 0, 0, false
		}
		total -= item.size
		remaining--
	}
	return maxBytes - total, maxEntries - remaining, total+incomingBytes <= maxBytes && remaining+incomingEntries <= maxEntries
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
