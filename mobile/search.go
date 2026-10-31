package mobile

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	osm "simonwaldherr.de/go/osmmini/core"
)

// SearchResult uses OSM IDs as signed 64-bit integers throughout the bridge.
type SearchResult struct {
	ID         int64         `json:"id"`
	Kind       string        `json:"kind"`
	Label      string        `json:"label"`
	Coordinate osm.Coord     `json:"coordinate"`
	Category   string        `json:"category,omitempty"`
	Place      string        `json:"place,omitempty"`
	Text       string        `json:"text,omitempty"`
	Details    *PlaceDetails `json:"details,omitempty"`
}

const searchKeyBytes = 96
const searchIndexBytes = 112
const maxSearchRecordBytes = 1 << 20

type searchPosting struct {
	term   string
	offset uint64
	length uint32
}

func normalized(s string) string {
	// Most prefix queries are already lowercase ASCII. Return their existing
	// storage instead of allocating a replacer, token slice and joined string.
	clean := len(s) > 0 && s[0] != ' ' && s[len(s)-1] != ' '
	for i := 0; clean && i < len(s); i++ {
		c := s[i]
		clean = c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == ' ' && (i == 0 || s[i-1] != ' ')
	}
	if clean {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	separator := false
	for _, r := range s {
		r = unicode.ToLower(r)
		switch r {
		case 'ä':
			r = 'a'
		case 'ö':
			r = 'o'
		case 'ü':
			r = 'u'
		case 'é':
			r = 'e'
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			separator = out.Len() > 0
			continue
		}
		if separator {
			out.WriteByte(' ')
			separator = false
		}
		if r == 'ß' {
			out.WriteString("ss")
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func buildSearch(ctx context.Context, path string, r *osm.Router, source string) error {
	return buildSearchWithPreset(ctx, path, r, source, POIPresetAll)
}

func buildSearchWithPreset(ctx context.Context, path string, r *osm.Router, source string, preset POIPreset) error {
	return buildSearchWithPresetObserved(ctx, path, r, source, preset, nil)
}

func buildSearchWithPresetObserved(ctx context.Context, path string, r *osm.Router, source string, preset POIPreset, observe func(SearchResult, uint64, uint32) error) error {
	var err error
	preset, err = preset.normalized()
	if err != nil {
		return err
	}
	if err := cleanSearchScratch(ctx, filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := newSearchDataWriter(ctx, f)
	if err != nil {
		return err
	}
	defer w.close()
	index := searchIndexBuilder{ctx: ctx, path: path + ".idx"}
	defer index.close()
	var offset uint64
	emit := func(result SearchResult) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		result.Text = normalized(result.Text + " " + result.Label + " " + result.Category)
		b, err := json.Marshal(result)
		if err != nil {
			return err
		}
		b = append(b, '\n')
		if len(b) > maxSearchRecordBytes {
			return fmt.Errorf("search record exceeds %d bytes", maxSearchRecordBytes)
		}
		if _, err = w.Write(b); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, term := range strings.Fields(result.Text) {
			if len(term) > searchKeyBytes {
				term = term[:searchKeyBytes]
			}
			if !seen[term] {
				if err := index.add(searchPosting{term, offset, uint32(len(b))}); err != nil {
					return err
				}
				seen[term] = true
			}
		}
		if observe != nil {
			observed := result
			// JSON replaces malformed UTF-8. Derived indexes must classify the
			// exact strings later readers see, without reparsing normal records.
			if !utf8.ValidString(result.Label) || !utf8.ValidString(result.Category) || !utf8.ValidString(result.Place) || !utf8.ValidString(result.Kind) {
				if err := json.Unmarshal(b, &observed); err != nil {
					return err
				}
			}
			if err := observe(observed, offset, uint32(len(b))); err != nil {
				return err
			}
		}
		offset += uint64(len(b))
		return nil
	}
	for _, street := range r.AllStreets() {
		if err = emit(SearchResult{ID: street.NodeID, Kind: "street", Label: street.Name, Coordinate: street.Coord}); err != nil {
			return err
		}
	}
	// Spool labels and references independently; neither a region's way tags nor
	// its complete reference/coordinate table needs to remain on the Go heap.
	ways, err := os.CreateTemp(filepath.Dir(path), ".search-ways-*")
	if err != nil {
		return err
	}
	defer func() { ways.Close(); os.Remove(ways.Name()) }()
	wayWriter := bufio.NewWriter(ways)
	nodes := searchNodeBuilder{ctx: ctx, parent: filepath.Dir(path)}
	defer nodes.close()
	result := func(id int64, kind string, c osm.Coord, tags osm.Tags, category string) SearchResult {
		label := strings.TrimSpace(tags["name"])
		street := strings.TrimSpace(tags["addr:street"])
		if street == "" {
			street = strings.TrimSpace(tags["addr:place"])
		}
		address := strings.TrimSpace(street + " " + tags["addr:housenumber"])
		text := address + " " + tags["addr:postcode"]
		details := detailsFromTags(tags)
		filteredPOI := category == "" && presetCategory(tags, POIPresetAll) != ""
		if filteredPOI {
			// Keep address lookup without restoring an excluded POI's name or category.
			label = address
			if details != nil {
				details.Emergency = nil
			}
		} else {
			text += " " + tags["brand"] + " " + poiSearchAliases(category)
			if category == emergencyCategory(tags) && details != nil && details.Emergency != nil {
				text += " " + details.Emergency.Reference
			}
		}
		keepEmergency := preset == POIPresetAll || preset == POIPresetFirefighting ||
			(preset == POIPresetOutdoor && category == emergencyCategory(tags))
		if details != nil && !keepEmergency {
			details.Emergency = nil
		}
		if label == "" && category == emergencyCategory(tags) && category != "" {
			label = poiLabel(category)
			if reference := detailValue(tags["ref"], 256); reference != "" {
				label += " " + reference
			}
		}
		if label == "" {
			label = address
		}
		if label == "" {
			label = poiLabel(category)
		}
		if city := tags["addr:city"]; city != "" {
			label += " · " + city
		}
		return SearchResult{ID: id, Kind: kind, Label: label, Coordinate: c, Category: category, Place: tags["place"], Text: text, Details: cleanDetails(details)}
	}
	opts := osm.Options{Context: ctx, EmitWayNodeIDs: true, TaggedNodeKey: func(k string) bool {
		return searchPOIKey(k) || strings.HasPrefix(k, "addr:")
	}, TaggedWayKey: func(k string) bool {
		// highway=emergency_access_point is a node-only special case. Ordinary
		// road ways already have their searchable names in the routing graph.
		return k != "highway" && searchPOIKey(k) || strings.HasPrefix(k, "addr:")
	}, KeepTag: func(k string) bool {
		return k == "name" || k == "brand" || searchPOIKey(k) || strings.HasPrefix(k, "addr:") || detailTag(k)
	}}
	err = osm.ExtractFile(source, opts, osm.Callbacks{TaggedNode: func(n osm.Node) error {
		category := presetCategory(n.Tags, preset)
		if category == "" && strings.TrimSpace(n.Tags["addr:street"]) == "" && strings.TrimSpace(n.Tags["addr:place"]) == "" {
			return nil
		}
		return emit(result(n.ID, "node", osm.Coord{Lat: n.Lat, Lon: n.Lon}, n.Tags, category))
	}, TaggedWay: func(way osm.Way) error {
		category := presetCategory(way.Tags, preset)
		if category == "" && strings.TrimSpace(way.Tags["addr:street"]) == "" && strings.TrimSpace(way.Tags["addr:place"]) == "" {
			return nil
		}
		if err := nodes.add(way.NodeIDs); err != nil {
			return err
		}
		return writeSearchWay(wayWriter, result(way.ID, "way", osm.Coord{}, way.Tags, category), way.NodeIDs)
	}})
	if err != nil {
		return err
	}
	if err = wayWriter.Flush(); err != nil {
		return err
	}
	if _, err = ways.Seek(0, io.SeekStart); err != nil {
		return err
	}
	coordinates, err := nodes.finish()
	if err != nil {
		return err
	}
	if coordinates != nil {
		defer coordinates.close()
		err = osm.ExtractFile(source, osm.Options{Context: ctx}, osm.Callbacks{Node: coordinates.record})
		if err != nil {
			return err
		}
		if err = coordinates.finishCoordinates(); err != nil {
			return err
		}
		reader := bufio.NewReader(ways)
		for {
			way, err := readSearchWay(ctx, reader, coordinates.coordinate)
			if err == io.EOF {
				break
			} else if err != nil {
				return err
			}
			if way.Kind != "" {
				if err = emit(way); err != nil {
					return err
				}
			}
		}
	}
	if err = w.finish(); err != nil {
		return err
	}
	return index.finish()
}

// Import holds the destination lock while building this private staging
// directory. A process killed during indexing cannot run defers, so discard its
// scratch files on resume instead of carrying them into the published region.
func cleanSearchScratch(ctx context.Context, directory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".search-sort-") || strings.HasPrefix(entry.Name(), ".search-ways-") || strings.HasPrefix(entry.Name(), ".search-nodes-") {
			if err := os.RemoveAll(filepath.Join(directory, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Way records and sort runs are private import formats. Published search data
// and its final index each have their own versioned readers.
func writeSearchWay(w *bufio.Writer, result SearchResult, nodes []int64) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(data) > maxSearchRecordBytes || uint64(len(nodes)) > uint64(^uint32(0)) {
		return fmt.Errorf("temporary search way exceeds record limit")
	}
	var b [8]byte
	binary.LittleEndian.PutUint32(b[:4], uint32(len(data)))
	binary.LittleEndian.PutUint32(b[4:], uint32(len(nodes)))
	if _, err = w.Write(b[:]); err != nil {
		return err
	}
	if _, err = w.Write(data); err != nil {
		return err
	}
	// Geometry is streamed as IDs, avoiding another large JSON allocation for
	// ways with many vertices. The extraction callback already owns the slice.
	for _, id := range nodes {
		binary.LittleEndian.PutUint64(b[:], uint64(id))
		if _, err = w.Write(b[:]); err != nil {
			return err
		}
	}
	return nil
}

func readSearchWay(ctx context.Context, r *bufio.Reader, lookup func(int64) (osm.Coord, bool, error)) (SearchResult, error) {
	var result SearchResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return result, err
	}
	length := binary.LittleEndian.Uint32(b[:4])
	nodes := binary.LittleEndian.Uint32(b[4:])
	if length == 0 || length > maxSearchRecordBytes {
		return result, fmt.Errorf("invalid temporary search way length")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return result, fmt.Errorf("read temporary search way: %w", err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	var c osm.Coord
	count := 0
	for i := uint32(0); i < nodes; i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return result, err
			}
		}
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return result, fmt.Errorf("read temporary search way reference: %w", err)
		}
		coordinate, found, err := lookup(int64(binary.LittleEndian.Uint64(b[:])))
		if err != nil {
			return result, err
		}
		if found {
			c.Lat += coordinate.Lat
			c.Lon += coordinate.Lon
			count++
		}
	}
	if count == 0 {
		return SearchResult{}, nil
	}
	result.Coordinate = osm.Coord{Lat: c.Lat / float64(count), Lon: c.Lon / float64(count)}
	return result, nil
}

// Each sort chunk holds at most 16,384 postings and 96 bytes per term. Merge
// passes open at most 16 runs, so index preparation does not grow a RAM-resident
// posting list alongside the graph and all POI/address geometry.
const searchSortPostings = 16 << 10
const searchMergeRuns = 16

type searchIndexBuilder struct {
	ctx      context.Context
	path     string
	dir      string
	postings []searchPosting
	runs     []string
}

func (s *searchIndexBuilder) close() {
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

func (s *searchIndexBuilder) add(p searchPosting) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.postings == nil {
		s.postings = make([]searchPosting, 0, searchSortPostings)
	}
	// A term must not pin the entire normalized address/brand string.
	p.term = strings.Clone(p.term)
	s.postings = append(s.postings, p)
	if len(s.postings) < searchSortPostings {
		return nil
	}
	return s.flush()
}

func sortSearchPostings(postings []searchPosting) {
	slices.SortFunc(postings, func(a, b searchPosting) int {
		if c := strings.Compare(a.term, b.term); c != 0 {
			return c
		}
		if a.offset < b.offset {
			return -1
		}
		if a.offset > b.offset {
			return 1
		}
		return 0
	})
}

func (s *searchIndexBuilder) flush() error {
	if len(s.postings) == 0 {
		return s.ctx.Err()
	}
	if s.dir == "" {
		var err error
		s.dir, err = os.MkdirTemp(filepath.Dir(s.path), ".search-sort-*")
		if err != nil {
			return err
		}
	}
	sortSearchPostings(s.postings)
	path := filepath.Join(s.dir, fmt.Sprintf("run-%d", len(s.runs)))
	if err := writeSearchPostings(s.ctx, path, s.postings, true); err != nil {
		return err
	}
	s.runs = append(s.runs, path)
	clear(s.postings)
	s.postings = s.postings[:0]
	return nil
}

func (s *searchIndexBuilder) finish() error {
	if len(s.runs) == 0 {
		sortSearchPostings(s.postings)
		return writeSearchPostings(s.ctx, s.path, s.postings, false)
	}
	if err := s.flush(); err != nil {
		return err
	}
	runs := s.runs
	for pass := 0; len(runs) > searchMergeRuns; pass++ {
		var next []string
		for i := 0; i < len(runs); i += searchMergeRuns {
			group := runs[i:min(len(runs), i+searchMergeRuns)]
			path := filepath.Join(s.dir, fmt.Sprintf("merge-%d-%d", pass, i))
			if err := mergeSearchPostings(s.ctx, path, group, true); err != nil {
				return err
			}
			next = append(next, path)
			for _, old := range group {
				if err := os.Remove(old); err != nil {
					return err
				}
			}
		}
		runs = next
	}
	return mergeSearchPostings(s.ctx, s.path, runs, false)
}

func writeSearchRunPosting(w *bufio.Writer, p searchPosting) error {
	length := 1 + len(p.term) + 12
	if w.Available() < length {
		if err := w.Flush(); err != nil {
			return err
		}
	}
	b := w.AvailableBuffer()[:length]
	b[0] = byte(len(p.term))
	copy(b[1:], p.term)
	at := 1 + len(p.term)
	binary.LittleEndian.PutUint64(b[at:], p.offset)
	binary.LittleEndian.PutUint32(b[at+8:], p.length)
	_, err := w.Write(b)
	return err
}

func writeSearchPostings(ctx context.Context, path string, postings []searchPosting, compact bool) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	w := bufio.NewWriterSize(f, 32<<10)
	var final *compactSearchWriter
	if !compact {
		final, err = newCompactSearchWriter(ctx, f, w)
		if err != nil {
			return err
		}
		defer final.close()
	}
	for i, p := range postings {
		if i%4096 == 0 {
			if err = ctx.Err(); err != nil {
				return err
			}
		}
		if compact {
			err = writeSearchRunPosting(w, p)
		} else {
			err = final.add(p)
		}
		if err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = w.Flush(); err != nil {
		return err
	}
	if !compact {
		return final.finish()
	}
	return nil
}

type searchRun struct {
	reader  *bufio.Reader
	posting searchPosting
	buffer  [searchKeyBytes + 12]byte
}

func (r *searchRun) advance() error {
	n, err := r.reader.ReadByte()
	if err != nil {
		return err
	}
	if n == 0 || n > searchKeyBytes {
		return fmt.Errorf("invalid temporary search key length")
	}
	b := r.buffer[:int(n)+12]
	if _, err = io.ReadFull(r.reader, b); err != nil {
		return fmt.Errorf("read temporary search posting: %w", err)
	}
	term := r.posting.term
	if term != string(b[:n]) {
		term = string(b[:n])
	}
	r.posting = searchPosting{term, binary.LittleEndian.Uint64(b[n:]), binary.LittleEndian.Uint32(b[int(n)+8:])}
	return nil
}

type searchRunHeap []*searchRun

func (h searchRunHeap) Len() int { return len(h) }
func (h searchRunHeap) Less(i, j int) bool {
	a, b := h[i].posting, h[j].posting
	if a.term != b.term {
		return a.term < b.term
	}
	return a.offset < b.offset
}
func (h searchRunHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *searchRunHeap) Push(x any)   { *h = append(*h, x.(*searchRun)) }
func (h *searchRunHeap) Pop() any {
	i := len(*h) - 1
	x := (*h)[i]
	*h = (*h)[:i]
	return x
}

func mergeSearchPostings(ctx context.Context, path string, paths []string, compact bool) (err error) {
	var queue searchRunHeap
	for _, path := range paths {
		f, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer f.Close()
		run := &searchRun{reader: bufio.NewReaderSize(f, 32<<10)}
		if err = run.advance(); err == io.EOF {
			continue
		} else if err != nil {
			return err
		}
		queue = append(queue, run)
	}
	heap.Init(&queue)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	w := bufio.NewWriterSize(f, 32<<10)
	var final *compactSearchWriter
	if !compact {
		final, err = newCompactSearchWriter(ctx, f, w)
		if err != nil {
			return err
		}
		defer final.close()
	}
	for n := 0; len(queue) > 0; n++ {
		if n%4096 == 0 {
			if err = ctx.Err(); err != nil {
				return err
			}
		}
		run := queue[0]
		if compact {
			err = writeSearchRunPosting(w, run.posting)
		} else {
			err = final.add(run.posting)
		}
		if err != nil {
			return err
		}
		if err = run.advance(); err == io.EOF {
			heap.Pop(&queue)
		} else if err != nil {
			return err
		} else {
			heap.Fix(&queue, 0)
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = w.Flush(); err != nil {
		return err
	}
	if !compact {
		return final.finish()
	}
	return nil
}

type searchReader struct {
	data, index    *os.File
	count          int
	dataBytes      int64
	compact        bool
	blocks         int
	directory      int64
	compressedData bool
	dataBlocks     uint64
	dataDirectory  int64
}

// Scratch buffers are owned by one query and reused across calls. The reader
// itself has no cursor or mutable cache, so parallel Swift/Go searches cannot
// interfere with one another. Large JSON records are never retained by the pool.
type searchScratch struct {
	postings   [64 * searchIndexBytes]byte
	block      [searchBlockMaxBytes]byte
	data       [32 << 10]byte
	encoded    [searchDataMaxEncoded]byte
	directory  [64 * searchDataEntryBytes]byte
	compressed bytes.Reader
	inflater   io.ReadCloser
	extra      [1]byte
}

var searchScratchPool = sync.Pool{New: func() any { return new(searchScratch) }}

func openSearch(path string) (*searchReader, error) {
	data, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	idx, err := os.Open(path + ".idx")
	if err != nil {
		data.Close()
		return nil, err
	}
	st, err := idx.Stat()
	if err != nil {
		data.Close()
		idx.Close()
		return nil, fmt.Errorf("invalid search index")
	}
	dst, err := data.Stat()
	if err != nil {
		data.Close()
		idx.Close()
		return nil, err
	}
	reader := &searchReader{data: data, index: idx}
	if err = reader.readDataHeader(dst.Size()); err != nil {
		reader.close()
		return nil, err
	}
	if err = reader.readIndexHeader(st.Size()); err != nil {
		reader.close()
		return nil, err
	}
	return reader, nil
}
func (s *searchReader) close() { s.data.Close(); s.index.Close() }
func (s *searchReader) search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	aliases := map[string]string{"apotheke": "pharmacy", "tankstelle": "fuel", "krankenhaus": "hospital", "supermarkt": "supermarket", "feuerwehr": "fire_station"}
	query = normalized(query)
	if alias := aliases[query]; alias != "" {
		query = normalized(alias)
	}
	words := strings.Fields(query)
	if len(words) == 0 {
		return []SearchResult{}, nil
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	key := words[0]
	for _, word := range words {
		if len(word) > len(key) {
			key = word
		}
	}
	if len(key) > searchKeyBytes {
		return []SearchResult{}, nil
	}
	scratch := searchScratchPool.Get().(*searchScratch)
	defer searchScratchPool.Put(scratch)
	keyBytes := []byte(key)
	iterator, err := s.postingsFrom(keyBytes, scratch)
	if err != nil {
		return nil, err
	}
	seen := map[uint64]bool{}
	out := []SearchResult{}
	records := searchRecords{search: s, ctx: ctx, scratch: scratch}
	for len(out) < limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		term, offset, length, err := iterator.next()
		if err == io.EOF {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		if bytes.Compare(term, keyBytes) < 0 {
			continue
		}
		if !bytes.HasPrefix(term, keyBytes) {
			return out, nil
		}
		b, err := records.read(offset, length)
		if err != nil {
			return nil, err
		}
		// Normalized words contain only letters/digits and therefore have no
		// JSON escape sequences. A missing raw word cannot match Text. This
		// cheap filter avoids decoding every POI for a broad prefix; the full
		// Text check below retains the established substring semantics.
		matches := true
		for _, word := range words {
			if !bytes.Contains(b, []byte(word)) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if seen[offset] {
			continue
		}
		seen[offset] = true
		var result SearchResult
		if err = json.Unmarshal(b, &result); err != nil {
			return nil, err
		}
		for _, word := range words {
			if !strings.Contains(result.Text, word) {
				matches = false
				break
			}
		}
		if matches {
			result.Text = ""
			result.Details = cleanDetails(result.Details)
			if len(out) == 0 {
				out = make([]SearchResult, 0, limit)
			}
			out = append(out, result)
		}
	}
	return out, nil
}
