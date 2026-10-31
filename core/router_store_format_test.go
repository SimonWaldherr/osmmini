package osmmini

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

// legacyGraphFixture encodes the independently specified V1 records, ensuring
// compatibility coverage does not silently switch format with WriteGraph.
func legacyGraphFixture(t testing.TB, r *Router) []byte {
	t.Helper()
	ids := make([]int64, 0, len(r.g.coords))
	for id := range r.g.coords {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	classes := []string{}
	classIDs := map[string]uint32{}
	for _, id := range ids {
		for _, e := range r.g.adj[id] {
			if _, ok := classIDs[e.HwyType]; !ok {
				classIDs[e.HwyType] = uint32(len(classes))
				classes = append(classes, e.HwyType)
			}
		}
	}
	meta, err := json.Marshal(graphMetadata{classes, r.streets})
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 96+len(ids)*40+r.EdgeCount()*48+len(ids)*16+len(meta))
	put := binary.LittleEndian.PutUint64
	copy(data, "OSMMGR01")
	put(data[8:], uint64(len(ids)))
	put(data[16:], uint64(r.EdgeCount()))
	put(data[24:], uint64(len(meta)))
	bounds, _ := r.Bounds()
	for i, v := range []float64{bounds.MinLat, bounds.MaxLat, bounds.MinLon, bounds.MaxLon} {
		put(data[32+i*8:], math.Float64bits(v))
	}
	edge := 0
	for i, id := range ids {
		b := data[96+i*40:]
		c := r.g.coords[id]
		put(b, uint64(id))
		put(b[8:], math.Float64bits(c.Lat))
		put(b[16:], math.Float64bits(c.Lon))
		put(b[24:], uint64(edge))
		binary.LittleEndian.PutUint32(b[32:], uint32(len(r.g.adj[id])))
		for _, e := range r.g.adj[id] {
			b := data[96+len(ids)*40+edge*48:]
			put(b, uint64(e.To))
			for i, v := range []float64{e.DistM, e.SpeedKph, e.MaxHeightM, e.MaxWeightT} {
				put(b[8+i*8:], math.Float64bits(v))
			}
			binary.LittleEndian.PutUint32(b[40:], classIDs[e.HwyType])
			edge++
		}
	}
	type cell struct{ key, id int64 }
	cells := make([]cell, len(ids))
	idx := spatialIndex{cellSize: 0.01, minLat: bounds.MinLat, minLon: bounds.MinLon}
	for i, id := range ids {
		c := r.g.coords[id]
		x, y := idx.cell(c.Lat, c.Lon)
		cells[i] = cell{cellKey(x, y), id}
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].key != cells[j].key {
			return cells[i].key < cells[j].key
		}
		return cells[i].id < cells[j].id
	})
	offset := 96 + len(ids)*40 + r.EdgeCount()*48
	for _, c := range cells {
		put(data[offset:], uint64(c.key))
		put(data[offset+8:], uint64(c.id))
		offset += 16
	}
	copy(data[offset:], meta)
	return data
}

func TestFileGraphLegacyCompatibility(t *testing.T) {
	memory, from, to := buildGridRouter(8, 8)
	memory.streets = map[string]streetEntry{"hauptstrasse": {Display: "Hauptstraße", NodeIDs: []int64{from, to}}}
	data := legacyGraphFixture(t, memory)
	path := filepath.Join(t.TempDir(), "legacy.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	for id, c := range memory.g.coords {
		if got, ok := disk.Coord(id); !ok || got != c {
			t.Fatalf("legacy coordinate %d: %v, %v", id, got, ok)
		}
		var edges []Edge
		for it := disk.g.edges(id); it.next(); {
			edges = append(edges, it.edge())
		}
		if !slices.Equal(edges, memory.g.adj[id]) {
			t.Fatalf("legacy edges at %d differ", id)
		}
		nearest, _, ok := disk.NearestNode(c.Lat, c.Lon)
		if !ok || nearest != id {
			t.Fatalf("legacy snap at %d: %d, %v", id, nearest, ok)
		}
	}
	for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
		opts := RouteOptions{Engine: engine, Objective: ObjectiveDuration}
		want, err := memory.RouteWithOptions(t.Context(), from, to, opts)
		if err != nil {
			t.Fatal(err)
		}
		got, err := disk.RouteWithOptions(t.Context(), from, to, opts)
		if err != nil || got.Cost != want.Cost || got.DistanceM != want.DistanceM {
			t.Fatalf("legacy route %s differs: %v", engine, err)
		}
	}
	if got := disk.SearchStreets("Hauptstraße", 1); len(got) != 1 {
		t.Fatalf("legacy names: %v", got)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.bin")
	if err := disk.WriteGraph(t.Context(), copyPath); err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(copyPath)
	if err != nil || !bytes.Equal(copied, data) {
		t.Fatalf("legacy snapshot changed: %v", err)
	}
}

func TestCompactGraphPreservesIDsAndFloatBits(t *testing.T) {
	const from int64 = -9223372036854775807
	const to int64 = 9223372036854775806
	zero := math.Copysign(0, -1)
	payloadNaN := math.Float64frombits(0x7ff8000000000011)
	r := NewRouterFromGraph(map[int64]Coord{from: {Lat: zero, Lon: zero}, to: {Lat: 48.00000000000001, Lon: 12.00000000000001}}, map[int64][]Edge{
		from: {
			{To: to, DistM: math.Nextafter(123, 124), SpeedKph: 50, MaxHeightM: zero, MaxWeightT: 7.500000000000001, HwyType: "residential"},
			{To: to, DistM: 124, SpeedKph: 50, MaxHeightM: 0, MaxWeightT: 7.500000000000001, HwyType: "residential"},
			{To: to, DistM: 125, SpeedKph: payloadNaN, MaxHeightM: math.Inf(1), MaxWeightT: 0, HwyType: "track"},
		},
		to: {{To: from, DistM: 126, SpeedKph: 50, MaxHeightM: zero, MaxWeightT: 7.500000000000001, HwyType: "residential"}},
	})
	path := filepath.Join(t.TempDir(), "compact.bin")
	if err := r.WriteGraph(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if !disk.g.store.compact {
		t.Fatal("writer did not use compact graph")
	}
	if count := binary.LittleEndian.Uint64(disk.g.store.data[64:]); count != 3 {
		t.Fatalf("shared attributes: %d, want 3", count)
	}
	for id, c := range r.g.coords {
		got, ok := disk.Coord(id)
		if !ok || math.Float64bits(got.Lat) != math.Float64bits(c.Lat) || math.Float64bits(got.Lon) != math.Float64bits(c.Lon) {
			t.Fatalf("coordinate bits changed for %d", id)
		}
		it := disk.g.edges(id)
		for _, want := range r.g.adj[id] {
			if !it.next() {
				t.Fatal("missing edge")
			}
			got := it.edge()
			if got.To != want.To || got.HwyType != want.HwyType || math.Float64bits(got.DistM) != math.Float64bits(want.DistM) || math.Float64bits(got.SpeedKph) != math.Float64bits(want.SpeedKph) || math.Float64bits(got.MaxHeightM) != math.Float64bits(want.MaxHeightM) || math.Float64bits(got.MaxWeightT) != math.Float64bits(want.MaxWeightT) {
				t.Fatalf("edge bits changed: got %+v, want %+v", got, want)
			}
		}
		if it.next() {
			t.Fatal("extra edge")
		}
	}
}

func TestCompactGraphRecordSize(t *testing.T) {
	r, _, _ := buildGridRouter(25, 25)
	path := filepath.Join(t.TempDir(), "compact.bin")
	if err := r.WriteGraph(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	metadataSize := int(binary.LittleEndian.Uint64(data[24:]))
	want := 96 + r.NodeCount()*44 + r.EdgeCount()*16 + 32 + metadataSize
	if len(data) != want {
		t.Fatalf("file size=%d, want %d", len(data), want)
	}
	old := len(legacyGraphFixture(t, r))
	if len(data)*2 >= old {
		t.Fatalf("compact graph %d is not less than half of legacy %d", len(data), old)
	}
}

func TestCompactGraphRejectsInvalidRecords(t *testing.T) {
	r, _, _ := buildGridRouter(3, 3)
	path := filepath.Join(t.TempDir(), "valid.bin")
	if err := r.WriteGraph(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	nodes, edges := r.NodeCount(), r.EdgeCount()
	edgesOffset := 96 + nodes*32
	cellsOffset := edgesOffset + edges*16
	attrsOffset := cellsOffset + nodes*12
	mutations := map[string]func([]byte){
		"node count":      func(b []byte) { binary.LittleEndian.PutUint64(b[8:], math.MaxUint64) },
		"edge count":      func(b []byte) { binary.LittleEndian.PutUint64(b[16:], math.MaxUint64) },
		"attribute count": func(b []byte) { binary.LittleEndian.PutUint64(b[64:], math.MaxUint64) },
		"metadata count":  func(b []byte) { binary.LittleEndian.PutUint64(b[24:], 0) },
		"node order":      func(b []byte) { copy(b[96+32:96+40], b[96:104]) },
		"node start":      func(b []byte) { binary.LittleEndian.PutUint32(b[96+24:], 1) },
		"node degree":     func(b []byte) { binary.LittleEndian.PutUint32(b[96+28:], math.MaxUint32) },
		"target":          func(b []byte) { binary.LittleEndian.PutUint32(b[edgesOffset:], uint32(nodes)) },
		"attribute":       func(b []byte) { binary.LittleEndian.PutUint32(b[edgesOffset+4:], 1) },
		"class":           func(b []byte) { binary.LittleEndian.PutUint32(b[attrsOffset+24:], 1) },
		"cell node":       func(b []byte) { binary.LittleEndian.PutUint32(b[cellsOffset+8:], uint32(nodes)) },
		"cell order":      func(b []byte) { copy(b[cellsOffset+12:cellsOffset+24], b[cellsOffset:cellsOffset+12]) },
		"cell coordinate": func(b []byte) { binary.LittleEndian.PutUint64(b[cellsOffset:], uint64(cellKey(-1, -1))) },
		"coordinate":      func(b []byte) { binary.LittleEndian.PutUint64(b[96+8:], math.Float64bits(math.NaN())) },
		"bounds":          func(b []byte) { binary.LittleEndian.PutUint64(b[32:], math.Float64bits(math.NaN())) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := bytes.Clone(valid)
			mutate(data)
			path := filepath.Join(t.TempDir(), "invalid.bin")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if graph, err := OpenGraph(path); err == nil {
				graph.Close()
				t.Fatal("accepted invalid compact graph")
			}
		})
	}
	for _, length := range []int{0, 8, 95, 96, edgesOffset - 1, cellsOffset - 1, attrsOffset - 1, len(valid) - 1} {
		t.Run("truncated", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "truncated.bin")
			if err := os.WriteFile(path, valid[:length], 0o600); err != nil {
				t.Fatal(err)
			}
			if graph, err := OpenGraph(path); err == nil {
				graph.Close()
				t.Fatalf("accepted truncation to %d", length)
			}
		})
	}
}

func TestCompactGraphDegenerateBounds(t *testing.T) {
	const from, via, to int64 = -101, 7, 34359738369
	for _, tc := range []struct {
		name   string
		coords map[int64]Coord
		bounds CoordWindow
	}{
		{"single node", map[int64]Coord{from: {Lat: 48.5, Lon: 12.3}}, CoordWindow{MinLat: 48.5, MaxLat: 48.5, MinLon: 12.3, MaxLon: 12.3}},
		{"flat latitude", map[int64]Coord{from: {Lat: 48, Lon: 12}, via: {Lat: 48, Lon: 12.02}, to: {Lat: 48, Lon: 12.04}}, CoordWindow{MinLat: 48, MaxLat: 48, MinLon: 12, MaxLon: 12.04}},
		{"flat longitude", map[int64]Coord{from: {Lat: 48, Lon: 12}, via: {Lat: 48.02, Lon: 12}, to: {Lat: 48.04, Lon: 12}}, CoordWindow{MinLat: 48, MaxLat: 48.04, MinLon: 12, MaxLon: 12}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adj := map[int64][]Edge{}
			if len(tc.coords) > 1 {
				adj[from] = []Edge{{To: via, DistM: 200, SpeedKph: 40, HwyType: "residential"}}
				adj[via] = []Edge{{To: to, DistM: 200, SpeedKph: 40, HwyType: "residential"}}
			}
			memory := NewRouterFromGraph(tc.coords, adj)
			if _, ok := memory.Bounds(); ok {
				t.Fatal("fixture does not exercise bounds fallback")
			}
			path := filepath.Join(t.TempDir(), "degenerate.bin")
			if err := memory.WriteGraph(t.Context(), path); err != nil {
				t.Fatal(err)
			}
			disk, err := OpenGraph(path)
			if err != nil {
				t.Fatal(err)
			}
			defer disk.Close()
			if !disk.g.store.compact || disk.NodeCount() != len(tc.coords) || disk.EdgeCount() != memory.EdgeCount() {
				t.Fatal("compact graph counts differ")
			}
			if got, ok := disk.Bounds(); !ok || got != tc.bounds {
				t.Fatalf("bounds=%+v, %v; want %+v", got, ok, tc.bounds)
			}
			coords, edges := disk.ExportGraph()
			for id, c := range tc.coords {
				if coords[id] != c || !slices.Equal(edges[id], adj[id]) {
					t.Fatalf("export differs at node %d", id)
				}
				got, distance, ok := disk.NearestNode(c.Lat, c.Lon)
				if !ok || got != id || distance != 0 {
					t.Fatalf("snap at %d: %d, %g, %v", id, got, distance, ok)
				}
			}
			if len(tc.coords) == 1 {
				if n := binary.LittleEndian.Uint64(disk.g.store.data[64:]); n != 0 {
					t.Fatalf("zero-edge graph has %d attributes", n)
				}
				it := disk.g.edges(from)
				if it.next() {
					t.Fatal("zero-edge graph has outgoing edge")
				}
				return
			}
			for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
				opts := RouteOptions{Engine: engine, Objective: ObjectiveDuration}
				want, err := memory.RouteWithOptions(t.Context(), from, to, opts)
				if err != nil {
					t.Fatal(err)
				}
				got, err := disk.RouteWithOptions(t.Context(), from, to, opts)
				if err != nil || got.Cost != want.Cost || got.DistanceM != want.DistanceM {
					t.Fatalf("route into one-way sink with %s: %+v, %v; want %+v", engine, got, err, want)
				}
				if _, err := disk.RouteWithOptions(t.Context(), to, from, opts); err == nil {
					t.Fatalf("reverse route left one-way sink with %s", engine)
				}
			}
		})
	}
}
