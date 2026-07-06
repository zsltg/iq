package file

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/fxamacker/cbor/v2"

	"github.com/zsltg/iq/internal/query"
)

// cacheVersion is bumped whenever the on-disk cache layout OR the normalized
// record shape changes — including any change to drivers/mongo Normalize or the
// drivers/file record builders. It is folded into the cache key, so a bump makes
// every prior cache a miss (a different filename) rather than serving records in
// a stale shape. The differential test (cold vs warm output) is the backstop for
// a forgotten bump.
const cacheVersion = 2

// cacheMagic prefixes every cache file so a truncated or foreign file is
// rejected cheaply before any CBOR decode.
var cacheMagic = [8]byte{'I', 'Q', 'C', 'A', 'C', 'H', 'E', '\n'}

// defaultCacheMinSize is the size floor below which a dump is not cached: its
// decode is already sub-perceptible, so a cache write would be pure overhead.
const defaultCacheMinSize = 4 << 20 // 4 MiB.

// CacheConfig is the decode-cache policy the composition root passes into a file
// Store. A zero value (or Enabled false, or empty Dir) disables caching, so a
// caller that wires nothing keeps today's always-decode behavior.
type CacheConfig struct {
	// Dir is the directory cache files live in; empty disables caching.
	Dir string
	// Enabled turns caching on; false disables it regardless of Dir.
	Enabled bool
	// Index builds a per-page Bloom index so a bounded Get decodes only the pages
	// that may hold a wanted key. When false the flat cache is still written, but a
	// Get streams the whole cache — the escape hatch for a very large keyspace
	// where the index build memory is unwelcome.
	Index bool
	// MinSize is the size floor in bytes; a dump smaller than it is not cached.
	// Zero selects defaultCacheMinSize.
	MinSize int64
}

// minSize returns the effective size floor.
func (c CacheConfig) minSize() int64 {
	if c.MinSize > 0 {
		return c.MinSize
	}
	return defaultCacheMinSize
}

// A cache file is laid out as:
//
//	magic[8] version[4] header(CBOR)   record… record   index(CBOR) trailer[8]
//
// The records are the flat, streamable body a scan reads. The index that follows
// them holds, per page, the byte offset where the page starts and a Bloom filter
// of its keys, so a bounded Get tests the filters and decodes only candidate
// pages. The 8-byte trailer is the little-endian offset of the index block, read
// from the end so the writer can stream the body without knowing final offsets up
// front; it also bounds the record region for a scan (records end where the index
// begins). A flat-mode cache writes an empty index (and still a trailer), so every
// cache file has the same shape and a Get simply falls back to streaming.

// cacheHeader is the metadata prefix of a cache file: enough to re-verify the
// cache still matches its dump (size, mtime, format, decimal mode) and to report
// the entry in `iq cache stat`. Encoded as a fixed-order CBOR array.
type cacheHeader struct {
	_         struct{} `cbor:",toarray"`
	Path      string
	Size      int64
	MTimeNano int64
	Format    int
	Decimal   int
}

// trailerLen is the fixed width of the index-offset trailer at a cache file's end.
const trailerLen = 8

// pageIndex locates one record page in a cache file: the byte offset where the
// page's records start, and a Bloom filter of the keys it holds.
type pageIndex struct {
	_      struct{} `cbor:",toarray"`
	Offset int64
	Filter []byte
}

// indexBlock is the whole per-page index, written after the records. An empty
// Pages (flat mode, or a dump with no records) means a Get streams instead.
type indexBlock struct {
	_     struct{} `cbor:",toarray"`
	Pages []pageIndex
}

// cacheMeta is the identity of a dump for cache-keying: its absolute path, size,
// and mtime. Two runs over an unmodified dump produce the same meta, hence the
// same cache file; any edit changes size or mtime and invalidates it.
type cacheMeta struct {
	path  string
	size  int64
	mtime int64
}

// cacheable reports the dump's cache identity and whether it may be cached at
// all: never for stdin (no stable path), a disabled policy, an unstattable path,
// or a dump below the size floor.
func (s *Store) cacheable() (cacheMeta, bool) {
	if s.data != nil || !s.cache.Enabled || s.cache.Dir == "" {
		return cacheMeta{}, false
	}
	abs, err := filepath.Abs(s.path)
	if err != nil {
		return cacheMeta{}, false
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() || fi.Size() < s.cache.minSize() {
		return cacheMeta{}, false
	}
	return cacheMeta{path: abs, size: fi.Size(), mtime: fi.ModTime().UnixNano()}, true
}

// cacheFile is the path of the cache file for a dump identity, keyed by a hash of
// the identity plus format, decimal mode, and cacheVersion, so any of them
// changing selects a different file (a miss) rather than reusing a stale one.
func (s *Store) cacheFile(m cacheMeta) string {
	h := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%d\x00%d\x00%d\x00%d",
		m.path, m.size, m.mtime, int(s.format), int(s.dec), cacheVersion))
	return filepath.Join(s.cache.Dir, hex.EncodeToString(h[:])+".cbor")
}

// cacheSource returns a RecordSource streaming the fresh cache for this dump, and
// true, when one exists and validates. Any miss — not cacheable, no file, wrong
// magic/version, or a header that no longer matches the dump — returns false so
// the caller decodes the original. It never returns an error: a bad cache is an
// optimization that did not apply, never a query failure.
func (s *Store) cacheSource() (query.RecordSource, bool) {
	m, ok := s.cacheable()
	if !ok {
		return nil, false
	}
	path := s.cacheFile(m)
	if !s.cacheFresh(path, m) {
		return nil, false
	}
	return func(ctx context.Context, fn func(batch []query.Record) error) error {
		return s.readCache(ctx, path, m, fn)
	}, true
}

// cacheFresh reports whether path is a cache file whose header still matches the
// dump identity, format, and decimal mode. It opens and reads only the header.
func (s *Store) cacheFresh(path string, m cacheMeta) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	h, _, err := readHeader(f)
	if err != nil {
		return false
	}
	return h.Size == m.size && h.MTimeNano == m.mtime &&
		h.Format == int(s.format) && h.Decimal == int(s.dec)
}

// readCache streams a validated cache file's record region as pages, stopping at
// the index block (whose offset the trailer names) rather than at end-of-file. It
// re-checks the header (guarding a file swapped since cacheFresh) and, on any read
// error, returns it wrapped — by the time records stream, the caller is committed
// to the cache, so a mid-stream corruption is a real error, not a silent miss.
func (s *Store) readCache(ctx context.Context, path string, m cacheMeta, fn func(batch []query.Record) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open cache %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	size, err := fileSize(f)
	if err != nil {
		return err
	}
	indexOffset, err := readTrailer(f, size)
	if err != nil {
		return fmt.Errorf("read cache trailer %q: %w", path, err)
	}
	// Bound the reader to the record region so the record decoder never runs into
	// the index block that follows it.
	body := io.NewSectionReader(f, 0, indexOffset)
	h, dec, err := readHeader(body)
	if err != nil {
		return fmt.Errorf("read cache header %q: %w", path, err)
	}
	if h.Size != m.size || h.MTimeNano != m.mtime || h.Format != int(s.format) || h.Decimal != int(s.dec) {
		return fmt.Errorf("cache %q no longer matches its dump", path)
	}
	page := make([]query.Record, 0, pageSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rec, err := decodeRecord(dec)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("decode cache record %q: %w", path, err)
		}
		page = append(page, rec)
		if len(page) >= pageSize {
			if err := fn(page); err != nil {
				return err
			}
			page = page[:0]
		}
	}
	if len(page) > 0 {
		return fn(page)
	}
	return nil
}

// populate decodes the original dump once, teeing each record into a temp cache
// file, and installs it atomically only if the decode ran to completion. A
// short-circuited Get, a cancelled scan, or a decode error leaves the temp
// discarded, so a partial dump is never cached. A cache write failure is
// swallowed — the query still streams from the live decode — so caching never
// makes a query fail.
func (s *Store) populate(ctx context.Context, m cacheMeta, fn func(batch []query.Record) error) error {
	w, err := s.newCacheWriter(m)
	if err != nil {
		// Cannot open a temp cache; fall back to a plain decode, no failure.
		return s.recordsDirect(ctx, fn)
	}
	teed := func(batch []query.Record) error {
		w.write(batch)
		return fn(batch)
	}
	if err := s.recordsDirect(ctx, teed); err != nil {
		w.discard()
		return err
	}
	if !w.commit() {
		w.discard()
	}
	return nil
}

// cacheWriter buffers a dump's records into a temp file, then renames it over the
// final cache path on commit, appending the page index and trailer. After a write
// error it goes inert: every later write is a no-op and commit reports failure, so
// a broken cache is discarded rather than installed. It tracks the byte position
// as it writes so each page's start offset is recorded for the index.
type cacheWriter struct {
	f      *os.File
	tmp    string
	final  string
	failed bool
	index  bool        // build the per-page Bloom index?
	pos    int64       // bytes written so far, i.e. the current file offset.
	pages  []pageIndex // one entry per page written, when index is on.
}

// Write appends b to the temp file and advances the tracked position, so the
// header/record/index writers share one offset counter.
func (w *cacheWriter) Write(b []byte) (int, error) {
	n, err := w.f.Write(b)
	w.pos += int64(n)
	return n, err
}

// newCacheWriter creates the temp cache file and writes its header, honoring the
// store's Index policy.
func (s *Store) newCacheWriter(m cacheMeta) (*cacheWriter, error) {
	if err := os.MkdirAll(s.cache.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	f, err := os.CreateTemp(s.cache.Dir, "iq-cache-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create temp cache: %w", err)
	}
	w := &cacheWriter{f: f, tmp: f.Name(), final: s.cacheFile(m), index: s.cache.Index}
	h := cacheHeader{Path: m.path, Size: m.size, MTimeNano: m.mtime, Format: int(s.format), Decimal: int(s.dec)}
	if err := writeHeader(w, h); err != nil {
		w.discard()
		return nil, err
	}
	return w, nil
}

// write appends a batch (one page) to the temp cache, recording its start offset
// and a Bloom filter of its keys when indexing. It goes inert on the first error.
func (w *cacheWriter) write(batch []query.Record) {
	if w.failed {
		return
	}
	offset := w.pos
	for _, rec := range batch {
		b, err := encodeRecord(rec)
		if err != nil {
			w.failed = true
			return
		}
		if _, err := w.Write(b); err != nil {
			w.failed = true
			return
		}
	}
	if w.index {
		filter := newBloom(len(batch))
		for _, rec := range batch {
			bloomAdd(filter, rec.Key)
		}
		w.pages = append(w.pages, pageIndex{Offset: offset, Filter: filter})
	}
}

// commit appends the index block and the trailer, then closes and atomically
// installs the temp cache, reporting whether it did. In flat mode w.pages is nil,
// so the index block is empty and a Get falls back to streaming.
func (w *cacheWriter) commit() bool {
	if w.failed {
		return false
	}
	indexOffset := w.pos
	blk, err := cborEnc.Marshal(indexBlock{Pages: w.pages})
	if err != nil {
		return false
	}
	if _, err := w.Write(blk); err != nil {
		return false
	}
	var trailer [trailerLen]byte
	binary.LittleEndian.PutUint64(trailer[:], uint64(indexOffset))
	if _, err := w.Write(trailer[:]); err != nil {
		return false
	}
	if err := w.f.Close(); err != nil {
		return false
	}
	return os.Rename(w.tmp, w.final) == nil
}

// discard closes and removes the temp cache, best-effort.
func (w *cacheWriter) discard() {
	_ = w.f.Close()
	_ = os.Remove(w.tmp)
}

// writeHeader writes the magic, version, and CBOR-encoded header to w.
func writeHeader(w io.Writer, h cacheHeader) error {
	if _, err := w.Write(cacheMagic[:]); err != nil {
		return fmt.Errorf("write cache magic: %w", err)
	}
	var ver [4]byte
	binary.LittleEndian.PutUint32(ver[:], cacheVersion)
	if _, err := w.Write(ver[:]); err != nil {
		return fmt.Errorf("write cache version: %w", err)
	}
	b, err := cborEnc.Marshal(h)
	if err != nil {
		return fmt.Errorf("encode cache header: %w", err)
	}
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("write cache header: %w", err)
	}
	return nil
}

// readHeader reads and validates the magic and version (via ReadFull, which reads
// exactly the fixed-width prefix), then decodes the CBOR header. It returns the
// decoder it used, positioned at the first record, so a record reader continues
// with the same decoder — a fresh one would drop the bytes this decoder buffered
// past the header. A wrong magic or version is an error, so an old-layout or
// foreign file is treated as a miss.
func readHeader(r io.Reader) (cacheHeader, *cbor.Decoder, error) {
	var magic [8]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return cacheHeader{}, nil, fmt.Errorf("read cache magic: %w", err)
	}
	if magic != cacheMagic {
		return cacheHeader{}, nil, errors.New("not an iq cache file")
	}
	var ver [4]byte
	if _, err := io.ReadFull(r, ver[:]); err != nil {
		return cacheHeader{}, nil, fmt.Errorf("read cache version: %w", err)
	}
	if binary.LittleEndian.Uint32(ver[:]) != cacheVersion {
		return cacheHeader{}, nil, errors.New("cache version mismatch")
	}
	dec := cborDec.NewDecoder(r)
	var h cacheHeader
	if err := dec.Decode(&h); err != nil {
		return cacheHeader{}, nil, fmt.Errorf("decode cache header: %w", err)
	}
	return h, dec, nil
}

// fileSize returns an open file's size.
func fileSize(f *os.File) (int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat cache: %w", err)
	}
	return fi.Size(), nil
}

// readTrailer reads the index-block offset from the fixed-width trailer at the end
// of a cache file, rejecting an offset that does not lie within the file.
func readTrailer(r io.ReaderAt, size int64) (int64, error) {
	if size < trailerLen {
		return 0, errors.New("cache file too small for trailer")
	}
	var trailer [trailerLen]byte
	if _, err := r.ReadAt(trailer[:], size-trailerLen); err != nil {
		return 0, fmt.Errorf("read trailer: %w", err)
	}
	off := int64(binary.LittleEndian.Uint64(trailer[:]))
	if off < 0 || off > size-trailerLen {
		return 0, fmt.Errorf("index offset %d out of range for size %d", off, size)
	}
	return off, nil
}

// readIndex decodes the index block spanning [indexOffset, size-trailerLen).
func readIndex(r io.ReaderAt, indexOffset, size int64) (indexBlock, error) {
	sec := io.NewSectionReader(r, indexOffset, size-trailerLen-indexOffset)
	var blk indexBlock
	if err := cborDec.NewDecoder(sec).Decode(&blk); err != nil {
		return indexBlock{}, fmt.Errorf("decode index: %w", err)
	}
	return blk, nil
}

// cacheGet resolves keys from a fresh indexed cache, decoding only the pages whose
// Bloom filter may hold a requested key, and returns true when it handled the
// request. It returns false — so the caller falls back to streaming — when the
// dump is not cacheable, the cache is missing or stale, carries no index (flat
// mode), or any read fails partway. It never returns an error: the cache is an
// optimization, and the streaming path re-derives the answer (and any real error).
func (s *Store) cacheGet(ctx context.Context, keys []string) (map[string]any, bool) {
	if !s.cache.Index {
		return nil, false // index disabled: a bounded read streams the flat cache.
	}
	m, ok := s.cacheable()
	if !ok {
		return nil, false
	}
	f, err := os.Open(s.cacheFile(m))
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	size, err := fileSize(f)
	if err != nil {
		return nil, false
	}
	h, _, err := readHeader(f)
	if err != nil || h.Size != m.size || h.MTimeNano != m.mtime || h.Format != int(s.format) || h.Decimal != int(s.dec) {
		return nil, false
	}
	indexOffset, err := readTrailer(f, size)
	if err != nil {
		return nil, false
	}
	idx, err := readIndex(f, indexOffset, size)
	if err != nil || len(idx.Pages) == 0 {
		return nil, false // flat cache (or empty): let the caller stream it.
	}
	want := make(map[string]struct{}, len(keys))
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
		out[k] = nil
	}
	found := make(map[string]bool, len(keys))
	for i := range idx.Pages {
		if len(found) == len(want) {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, false
		}
		if !pageMayHold(idx.Pages[i].Filter, want, found) {
			continue
		}
		end := indexOffset
		if i+1 < len(idx.Pages) {
			end = idx.Pages[i+1].Offset
		}
		if err := decodePage(f, idx.Pages[i].Offset, end, want, out, found); err != nil {
			return nil, false
		}
	}
	return out, true
}

// pageMayHold reports whether a page's filter may contain any still-unfound key,
// so a page relevant to no outstanding key is skipped without a decode.
func pageMayHold(filter []byte, want map[string]struct{}, found map[string]bool) bool {
	for k := range want {
		if !found[k] && bloomHas(filter, k) {
			return true
		}
	}
	return false
}

// decodePage decodes the records in [offset, end) and copies any whose key is
// wanted into out, marking it found.
func decodePage(r io.ReaderAt, offset, end int64, want map[string]struct{}, out map[string]any, found map[string]bool) error {
	sec := io.NewSectionReader(r, offset, end-offset)
	dec := cborDec.NewDecoder(sec)
	for {
		rec, err := decodeRecord(dec)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, ok := want[rec.Key]; ok {
			out[rec.Key] = rec.Value
			found[rec.Key] = true
		}
	}
}

// CacheEntry describes one cache file for `iq cache stat`: the dump it caches,
// that dump's recorded size, and the cache file's own size on disk.
type CacheEntry struct {
	File      string
	DumpPath  string
	DumpSize  int64
	CacheSize int64
}

// ListCache returns the valid cache entries in dir, sorted by dump path. A dir
// that does not exist yields no entries and no error (nothing cached yet); an
// unreadable or foreign file in it is skipped, not fatal.
func ListCache(dir string) ([]CacheEntry, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cache dir %q: %w", dir, err)
	}
	var out []CacheEntry
	for _, de := range des {
		if de.IsDir() || filepath.Ext(de.Name()) != ".cbor" {
			continue
		}
		path := filepath.Join(dir, de.Name())
		h, ok := headerOf(path)
		if !ok {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, CacheEntry{File: de.Name(), DumpPath: h.Path, DumpSize: h.Size, CacheSize: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DumpPath != out[j].DumpPath {
			return out[i].DumpPath < out[j].DumpPath
		}
		return out[i].File < out[j].File
	})
	return out, nil
}

// RemoveCache deletes cache files in dir. When dumpPath is empty it removes every
// cache file; otherwise only the entry whose header names that dump (matched by
// absolute path). It returns how many files it removed. A missing dir is not an
// error (nothing to remove).
func RemoveCache(dir, dumpPath string) (int, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read cache dir %q: %w", dir, err)
	}
	var want string
	if dumpPath != "" {
		if abs, err := filepath.Abs(dumpPath); err == nil {
			want = abs
		} else {
			want = dumpPath
		}
	}
	removed := 0
	for _, de := range des {
		if de.IsDir() || filepath.Ext(de.Name()) != ".cbor" {
			continue
		}
		path := filepath.Join(dir, de.Name())
		if want != "" {
			h, ok := headerOf(path)
			if !ok || h.Path != want {
				continue
			}
		}
		if err := os.Remove(path); err != nil {
			return removed, fmt.Errorf("remove cache %q: %w", path, err)
		}
		removed++
	}
	return removed, nil
}

// headerOf reads a cache file's header, reporting false for any file that is not
// a readable, current-version iq cache.
func headerOf(path string) (cacheHeader, bool) {
	f, err := os.Open(path)
	if err != nil {
		return cacheHeader{}, false
	}
	defer func() { _ = f.Close() }()
	h, _, err := readHeader(f)
	if err != nil {
		return cacheHeader{}, false
	}
	return h, true
}
