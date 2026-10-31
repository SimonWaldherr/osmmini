package mobile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func TestSearchIndexSortRunsPreservePostingOrder(t *testing.T) {
	for _, count := range []int{0, 1, searchSortPostings - 1, searchSortPostings, searchSortPostings + 1, searchSortPostings*searchMergeRuns + 17} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "search.idx")
			builder := searchIndexBuilder{ctx: t.Context(), path: path}
			defer builder.close()
			want := make([]searchPosting, count)
			for i := range want {
				term := fmt.Sprintf("café%05d", (count-i)%1297)
				if i%7 == 0 {
					term = strings.Repeat("x", searchKeyBytes)
				}
				want[i] = searchPosting{term: term, offset: uint64(count-i) * 17, length: uint32(i%500 + 1)}
				if err := builder.add(want[i]); err != nil {
					t.Fatal(err)
				}
				if len(builder.postings) >= searchSortPostings || cap(builder.postings) > searchSortPostings {
					t.Fatalf("unbounded sort chunk: length %d, capacity %d", len(builder.postings), cap(builder.postings))
				}
			}
			if err := builder.finish(); err != nil {
				t.Fatal(err)
			}
			// Compare independently sorted postings after decoding the final
			// blocks, including the multi-pass merge and term/block boundaries.
			sort.Slice(want, func(i, j int) bool {
				if want[i].term != want[j].term {
					return want[i].term < want[j].term
				}
				return want[i].offset < want[j].offset
			})
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			reader := &searchReader{index: file}
			if err := reader.readIndexHeader(info.Size()); err != nil {
				t.Fatal(err)
			}
			if !reader.compact || reader.count != count {
				t.Fatalf("index format/count = %v/%d", reader.compact, reader.count)
			}
			iterator, err := reader.postingsFrom(nil, new(searchScratch))
			if err != nil {
				t.Fatal(err)
			}
			for i, p := range want {
				term, offset, length, err := iterator.next()
				if err != nil || string(term) != p.term || offset != p.offset || length != p.length {
					t.Fatalf("posting %d differs: %q/%d/%d, want %+v; %v", i, term, offset, length, p, err)
				}
			}
			if _, _, _, err := iterator.next(); err != io.EOF {
				t.Fatalf("index has extra data: %v", err)
			}
			builder.close()
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 || entries[0].Name() != "search.idx" {
				t.Fatalf("scratch files left after completion: %+v; %v", entries, err)
			}
		})
	}
}

func TestSearchIndexCancellationAndRunErrorsCleanScratch(t *testing.T) {
	for _, phase := range []string{"add", "flush", "merge", "broken run", "write error"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			builder := searchIndexBuilder{ctx: t.Context(), path: filepath.Join(root, "index")}
			defer builder.close()
			if phase != "add" {
				for i := 0; i < searchSortPostings+20; i++ {
					if err := builder.add(searchPosting{term: "test", offset: uint64(i), length: 50}); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var err error
			switch phase {
			case "add":
				cancel()
				builder.ctx = ctx
				err = builder.add(searchPosting{term: "test"})
			case "flush":
				cancel()
				builder.ctx = ctx
				err = builder.flush()
			case "merge":
				if err = builder.flush(); err != nil {
					t.Fatal(err)
				}
				builder.ctx = &cancelSearchBuildContext{Context: ctx, cancel: cancel, after: 3}
				err = builder.finish()
			case "broken run":
				if err = os.WriteFile(builder.runs[0], []byte{4}, 0o600); err != nil {
					t.Fatal(err)
				}
				err = builder.finish()
			case "write error":
				if err = os.Mkdir(builder.path, 0o700); err != nil {
					t.Fatal(err)
				}
				err = builder.finish()
			}
			if err == nil {
				t.Fatal("operation succeeded despite cancellation/error")
			}
			if phase == "add" || phase == "flush" || phase == "merge" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("wrong cancellation error: %v", err)
				}
			}
			builder.close()
			matches, err := filepath.Glob(filepath.Join(root, ".search-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("scratch files left after error: %v, %v", matches, err)
			}
		})
	}
}

type cancelSearchBuildContext struct {
	context.Context
	cancel context.CancelFunc
	after  int
	checks int
}

func (ctx *cancelSearchBuildContext) Err() error {
	ctx.checks++
	if ctx.checks == ctx.after {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestSearchWaySpoolPreservesCentroidAndMissingReferences(t *testing.T) {
	var spool bytes.Buffer
	writer := bufio.NewWriter(&spool)
	original := SearchResult{ID: 42, Kind: "way", Label: "Campus · Munich", Category: "school", Place: "quarter", Text: "Main 7 81600 Karte"}
	if err := writeSearchWay(writer, original, []int64{1, 404, 3, 2, 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeSearchWay(writer, original, []int64{404}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(&spool)
	coordinates := map[int64]osm.Coord{1: {Lat: 1, Lon: 10}, 2: {Lat: 2, Lon: 20}, 3: {Lat: 3, Lon: 30}}
	lookup := func(id int64) (osm.Coord, bool, error) {
		coordinate, found := coordinates[id]
		return coordinate, found, nil
	}
	got, err := readSearchWay(t.Context(), reader, lookup)
	want := original
	want.Coordinate = osm.Coord{Lat: 1.75, Lon: 17.5}
	if err != nil || got != want {
		t.Fatalf("way centroid/metadata = %+v, %v; want %+v", got, err, want)
	}
	if got, err = readSearchWay(t.Context(), reader, lookup); err != nil || got != (SearchResult{}) {
		t.Fatalf("way without known nodes = %+v, %v", got, err)
	}
	if _, err = readSearchWay(t.Context(), reader, lookup); err != io.EOF {
		t.Fatalf("final way did not end stream: %v", err)
	}
}

func TestSearchWaySpoolRejectsTruncationAndOversizedRecords(t *testing.T) {
	for _, data := range [][]byte{[]byte("short"), binary.LittleEndian.AppendUint32(make([]byte, 4), 1), append(binary.LittleEndian.AppendUint32(nil, maxSearchRecordBytes+1), make([]byte, 4)...)} {
		if _, err := readSearchWay(t.Context(), bufio.NewReader(bytes.NewReader(data)), nil); err == nil || err == io.EOF {
			t.Fatalf("accepted truncated/oversized way %x: %v", data, err)
		}
	}
	var data bytes.Buffer
	writer := bufio.NewWriter(&data)
	if err := writeSearchWay(writer, SearchResult{Kind: "way"}, []int64{1}); err != nil {
		t.Fatal(err)
	}
	writer.Flush()
	truncated := data.Bytes()[:data.Len()-8]
	if _, err := readSearchWay(t.Context(), bufio.NewReader(bytes.NewReader(truncated)), nil); err == nil || err == io.EOF {
		t.Fatalf("accepted missing node reference: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readSearchWay(ctx, bufio.NewReader(&data), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled way read: %v", err)
	}
}

func TestBuildSearchSpoolsWaysWithUnsortedNodes(t *testing.T) {
	uv := func(v uint64) []byte { return binary.AppendUvarint(nil, v) }
	field := func(n int, v uint64) []byte { return append(uv(uint64(n<<3)), uv(v)...) }
	message := func(n int, b []byte) []byte {
		out := uv(uint64(n<<3 | 2))
		out = append(out, uv(uint64(len(b)))...)
		return append(out, b...)
	}
	zig := func(v int64) uint64 { return uint64(v<<1) ^ uint64(v>>63) }
	var table, group []byte
	for _, text := range []string{"", "name", "Campus", "amenity", "school", "addr:street", "Main", "addr:housenumber", "7", "addr:city", "Munich", "place", "quarter", "brand", "Karte"} {
		table = append(table, message(1, []byte(text))...)
	}
	for _, id := range []int64{3, 1, 2} {
		node := field(1, zig(id))
		node = append(node, field(8, zig(id*10000000))...)
		node = append(node, field(9, zig(id*100000000))...)
		group = append(group, message(1, node)...)
	}
	for _, id := range []uint64{100, 101} {
		way := field(1, id)
		way = append(way, message(2, []byte{1, 3, 5, 7, 9, 11, 13})...)
		way = append(way, message(3, []byte{2, 4, 6, 8, 10, 12, 14})...)
		nodes := []int64{1, 404, 3, 2, 1}
		if id == 101 {
			nodes = []int64{404}
		}
		var refs []byte
		var previous int64
		for _, node := range nodes {
			refs = append(refs, uv(zig(node-previous))...)
			previous = node
		}
		way = append(way, message(8, refs)...)
		group = append(group, message(3, way)...)
	}
	block := append(message(1, table), message(2, group)...)
	blob := message(1, block)
	header := append(message(1, []byte("OSMData")), field(3, uint64(len(blob)))...)
	out := append(binary.BigEndian.AppendUint32(nil, uint32(len(header))), header...)
	out = append(out, blob...)
	root := t.TempDir()
	pbf := filepath.Join(root, "source.osm.pbf")
	if err := os.WriteFile(pbf, out, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "search.jsonl")
	if err := buildSearch(t.Context(), path, &osm.Router{}, pbf); err != nil {
		t.Fatal(err)
	}
	reader, err := openSearch(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()
	got, err := reader.search(t.Context(), "main 7", 20)
	if err != nil || len(got) != 1 || got[0].ID != 100 || got[0].Label != "Campus · Munich" || got[0].Category != "school" || got[0].Place != "quarter" || math.Abs(got[0].Coordinate.Lat-1.75) > 1e-9 || math.Abs(got[0].Coordinate.Lon-17.5) > 1e-9 {
		t.Fatalf("spooled search = %+v, %v", got, err)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".search-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("way spool/sort runs left behind: %v, %v", matches, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := buildSearch(ctx, path, &osm.Router{}, pbf); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search build: %v", err)
	}
	matches, err = filepath.Glob(filepath.Join(root, ".search-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("cancelled import left scratch: %v, %v", matches, err)
	}
}

func TestBuildSearchRemovesInterruptedScratchOnly(t *testing.T) {
	root := t.TempDir()
	staleRun := filepath.Join(root, ".search-sort-interrupted")
	if err := os.Mkdir(staleRun, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleRun, "run-0"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	staleWay := filepath.Join(root, ".search-ways-interrupted")
	if err := os.WriteFile(staleWay, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, ".search-unrelated")
	if err := os.WriteFile(unrelated, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := buildSearch(t.Context(), filepath.Join(root, "search.jsonl"), &osm.Router{}, fixture(t)); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{staleRun, staleWay} {
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("interrupted scratch remains at %s: %v", stale, err)
		}
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "preserve" {
		t.Fatalf("unrelated staging file changed: %q, %v", data, err)
	}
}
