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
// bounded batch in memory; the operation admission session inventories quota
// once and publishes every batch through the same locked directory capability.
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
	publicationOnce                                                       sync.Once
	publicationGate                                                       chan struct{}
	admissionMu                                                           sync.Mutex
	admission                                                             *extractionAdmissionSession
	inventoryCalls                                                        atomic.Int64
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
	if !cache.acquirePublication(true) {
		return
	}
	defer cache.releasePublication()
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

func (cache *extractionCache) publicationToken() chan struct{} {
	cache.publicationOnce.Do(func() {
		cache.publicationGate = make(chan struct{}, 1)
		cache.publicationGate <- struct{}{}
	})
	return cache.publicationGate
}

// acquirePublication serializes ownership before an encoded item can join or
// detach the sole operation-wide pending batch. Producers honor cancellation;
// flush passes cancellable=false so cleanup always waits for the current owner.
func (cache *extractionCache) acquirePublication(cancellable bool) bool {
	gate := cache.publicationToken()
	if !cancellable || cache.ctx == nil {
		<-gate
		return true
	}
	select {
	case <-cache.ctx.Done():
		return false
	case <-gate:
		// Cancellation and the token can become ready together. Once ownership
		// is acquired, cancellation wins before any pending state is changed.
		if cache.ctx.Err() != nil {
			gate <- struct{}{}
			return false
		}
		return true
	}
}

func (cache *extractionCache) releasePublication() {
	cache.publicationToken() <- struct{}{}
}

// flush publishes the final bounded batch at the operation boundary. The
// provider calls this after workers have stopped, so no producer can append
// while the drain is in progress.
func (cache *extractionCache) flush() {
	if cache == nil {
		return
	}
	cache.acquirePublication(false)
	// Release the held directory and admission lock before another producer can
	// acquire publication ownership for a later reuse of this cache object.
	defer func() {
		cache.releaseAdmission()
		cache.releasePublication()
	}()
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
	if cache == nil || len(batch) == 0 {
		return
	}
	cache.admissionMu.Lock()
	defer cache.admissionMu.Unlock()
	if cache.ctx != nil && cache.ctx.Err() != nil {
		cache.releaseAdmissionLocked()
		return
	}
	maxBytes, maxEntries := cache.limits()
	if maxBytes <= 0 || maxEntries <= 0 {
		cache.releaseAdmissionLocked()
		return
	}
	if cache.admission == nil {
		session, err := beginExtractionAdmissionSession(cache.ctx, batch[0].entry, maxBytes, maxEntries)
		if err != nil {
			return
		}
		cache.inventoryCalls.Add(1)
		cache.admission = session
	}
	// Preserve bounded batch admission: a configured quota may be smaller than
	// the normal pending batch, so split it before reserving capacity. The
	// session inventory remains shared across every resulting chunk.
	for start := 0; start < len(batch); {
		end := start
		var bytes int64
		for end < len(batch) && end-start < maxEntries {
			bound := batch[end].bound
			if bound <= 0 || bound > maxBytes {
				end++
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
			if item.bound > 0 && item.bound <= maxBytes {
				chunk = append(chunk, item)
			}
		}
		start = end
		if len(chunk) == 0 {
			continue
		}
		writtenBytes, _, err := cache.admission.publishBatch(cache.ctx, chunk)
		cache.cacheWriteBytes.Add(writtenBytes)
		if err != nil {
			cache.releaseAdmissionLocked()
			return
		}
	}
}

func (cache *extractionCache) releaseAdmission() {
	if cache == nil {
		return
	}
	cache.admissionMu.Lock()
	defer cache.admissionMu.Unlock()
	cache.releaseAdmissionLocked()
}

func (cache *extractionCache) releaseAdmissionLocked() {
	if cache.admission != nil {
		_ = cache.admission.Close()
		cache.admission = nil
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
