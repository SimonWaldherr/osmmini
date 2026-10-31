package osmmini

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func chainTestRouter(nodes int, ring bool) *Router {
	coords, adj := make(map[int64]Coord), make(map[int64][]Edge)
	for i := 1; i <= nodes; i++ {
		coords[int64(i)] = Coord{Lat: 48 + float64(i)*0.0001, Lon: 12 + math.Sin(float64(i))*0.0001}
	}
	for i := 1; i < nodes || (ring && i == nodes); i++ {
		from, to := int64(i), int64(i%nodes+1)
		e := Edge{To: to, DistM: math.Nextafter(float64(i)*1.2345, math.Inf(1)), SpeedKph: 50, HwyType: "residential", MaxWeightT: 7.5}
		adj[from] = append(adj[from], e)
		e.To = from
		adj[to] = append(adj[to], e)
	}
	return NewRouterFromGraph(coords, adj)
}

func assertChainGraphEqual(t testing.TB, original, packed *Router) {
	t.Helper()
	if original.NodeCount() != packed.NodeCount() || original.EdgeCount() != packed.EdgeCount() {
		t.Fatal("logical counts changed")
	}
	original.g.eachNode(func(id int64, c Coord) {
		got, ok := packed.Coord(id)
		if !ok || math.Float64bits(got.Lat) != math.Float64bits(c.Lat) || math.Float64bits(got.Lon) != math.Float64bits(c.Lon) {
			t.Fatalf("coordinate changed at %d", id)
		}
		wantEdges, gotEdges := original.g.edges(id), packed.g.edges(id)
		for wantEdges.next() {
			if !gotEdges.next() || auditGraphEdgeKey(wantEdges.edge()) != auditGraphEdgeKey(gotEdges.edge()) {
				t.Fatalf("ordered edge bits changed at %d", id)
			}
		}
		if gotEdges.next() || original.g.degree(id) != packed.g.degree(id) {
			t.Fatalf("degree changed at %d", id)
		}
	})
}

func writeChainTestGraph(t testing.TB, r *Router) (*Router, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "graph.bin")
	if err := r.WriteGraph(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	assertChainGraphEqual(t, r, disk)
	return disk, path
}

func TestChainGraphRoundTripPreservesAllOriginalNodesAndEdges(t *testing.T) {
	for _, ring := range []bool{false, true} {
		r := chainTestRouter(30, ring)
		r.streets = map[string]streetEntry{"kurvenstrasse": {Display: "Kurvenstraße", NodeIDs: []int64{4, 13, 26}}}
		disk, path := writeChainTestGraph(t, r)
		if string(disk.g.store.data[:8]) != graphChainMagic {
			t.Fatal("long chain was not packed")
		}
		if disk.g.store.storedEdges != 2 {
			t.Fatalf("explicit edges=%d, want 2", disk.g.store.storedEdges)
		}
		for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
			for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
				for _, pair := range [][2]int64{{1, 30}, {5, 24}, {25, 3}, {13, 13}} {
					opt := RouteOptions{Engine: engine, Objective: objective, Pro: true, Weights: ProWeights{LeftTurn: 2, RightTurn: 1, UTurn: 10, Crossing: 3}}
					want, err := r.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
					if err != nil {
						t.Fatal(err)
					}
					got, err := disk.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
					if err != nil || !slices.Equal(got.Path, want.Path) || got.Cost != want.Cost || got.DistanceM != want.DistanceM || got.DurationS != want.DurationS {
						t.Fatalf("route changed: %s %s %v: %v", engine, objective, pair, err)
					}
				}
			}
		}
		for _, id := range []int64{4, 13, 26} {
			c, _ := r.Coord(id)
			got, distance, ok := disk.NearestNode(c.Lat, c.Lon)
			if !ok || got != id || distance != 0 {
				t.Fatalf("shape-node snapping changed: %d %d %g", id, got, distance)
			}
		}
		wantStreet, _ := r.StreetNode("Kurvenstraße")
		if id, ok := disk.StreetNode("Kurvenstraße"); !ok || id != wantStreet {
			t.Fatalf("street lookup changed: %d %v", id, ok)
		}
		coords, adj := disk.ExportGraph()
		assertChainGraphEqual(t, r, NewRouterFromGraph(coords, adj))
		copyPath := path + ".copy"
		if err := disk.WriteGraph(t.Context(), copyPath); err != nil {
			t.Fatal(err)
		}
		copyBytes, err := os.ReadFile(copyPath)
		if err != nil || !bytes.Equal(copyBytes, disk.g.store.data) {
			t.Fatal("snapshot changed packed graph")
		}
		oldBytes := graphHeaderSize + r.NodeCount()*(graphNodeSize+graphCellSize) + r.EdgeCount()*graphEdgeSize + graphAttributeSize + int(binary.LittleEndian.Uint64(disk.g.store.data[24:]))
		if saved := oldBytes - len(disk.g.store.data); saved < 500 {
			t.Fatalf("only saved %d bytes", saved)
		}
	}
}

func TestChainGraphKeepsBranchRestrictionsAndIncomingOnlyNodes(t *testing.T) {
	r := chainTestRouter(80, false)
	// One-way entry, incoming-only destination, an attribute change, parallel
	// variants and a self loop must stay exactly observable.
	r.g.coords[-4] = Coord{Lat: 48.01, Lon: 12.01}
	r.g.coords[-3] = Coord{Lat: 48.02, Lon: 12.02}
	e := r.g.adj[1][0]
	e.To = 14
	r.g.adj[-4] = []Edge{e}
	e.To = -3
	r.g.adj[24] = append(r.g.adj[24], e)
	r.g.adj[34][0].MaxHeightM = 2.5
	e = r.g.adj[44][0]
	e.SpeedKph = 5
	r.g.adj[44] = append(r.g.adj[44], e)
	e.To = 54
	r.g.adj[54] = append(r.g.adj[54], e)
	// Change the sign of zero and preserve a noncanonical NaN payload.
	for _, id := range []int64{60, 61, 62, 63, 64, 65, 66, 67, 68, 69} {
		for i := range r.g.adj[id] {
			r.g.adj[id][i].MaxHeightM = math.Copysign(0, -1)
			r.g.adj[id][i].MaxWeightT = math.Float64frombits(0x7ff8000000000042)
		}
	}
	r = NewRouterFromGraph(r.g.coords, r.g.adj)
	disk, _ := writeChainTestGraph(t, r)
	if disk.g.store.chainsOffset == 0 {
		t.Fatal("unaffected long chains should still be packed")
	}
	for _, id := range []int64{14, 24, 34, 44, 54} {
		if chain, _, _ := disk.g.store.chainNode(disk.g.store.nodeIndex(id)); chain != nil {
			t.Fatalf("boundary %d was packed", id)
		}
	}
	if disk.g.degree(-3) != 0 {
		t.Fatal("incoming-only sink changed")
	}
}

func TestShortAndOneWayChainsDoNotIncreaseFileSize(t *testing.T) {
	for _, oneWay := range []bool{false, true} {
		n := 4
		if oneWay {
			n = 30
		}
		r := chainTestRouter(n, false)
		if oneWay {
			for id, edges := range r.g.adj {
				r.g.adj[id] = slices.DeleteFunc(edges, func(e Edge) bool { return e.To < id })
			}
		}
		disk, _ := writeChainTestGraph(t, r)
		if string(disk.g.store.data[:8]) != graphMagic {
			t.Fatal("nonbeneficial chain changed format")
		}
	}
}

func TestChainGraphRandomSubdividedRoads(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 19))
	for trial := 0; trial < 20; trial++ {
		r := chainTestRouter(100, false)
		for id := int64(2); id < 100; id++ {
			if rng.IntN(10) == 0 {
				for i := range r.g.adj[id] {
					r.g.adj[id][i].SpeedKph = float64(rng.IntN(80) + 10)
				}
			}
			if rng.IntN(10) == 0 {
				slices.Reverse(r.g.adj[id])
			}
		}
		writeChainTestGraph(t, r)
	}
}

func TestChainGraphSignedIDsAndLargeJunction(t *testing.T) {
	line := chainTestRouter(20, false)
	relabel := func(id int64) int64 {
		switch id {
		case 1:
			return math.MinInt64 + 1
		case 2:
			return -8
		case 3:
			return 0
		case 4:
			return math.MaxInt64 - 1
		}
		return id
	}
	coords, adj := map[int64]Coord{}, map[int64][]Edge{}
	for id, c := range line.g.coords {
		coords[relabel(id)] = c
	}
	for id, edges := range line.g.adj {
		for _, e := range edges {
			e.To = relabel(e.To)
			adj[relabel(id)] = append(adj[relabel(id)], e)
		}
	}
	writeChainTestGraph(t, NewRouterFromGraph(coords, adj))
	// Many long spokes remain compressible without repeatedly scanning the
	// high-degree hub during candidate discovery or file validation.
	coords, adj = map[int64]Coord{0: {Lat: 48, Lon: 12}}, map[int64][]Edge{}
	for spoke := int64(0); spoke < 100; spoke++ {
		previous := int64(0)
		for step := int64(1); step <= 10; step++ {
			id := spoke*10 + step
			coords[id] = Coord{Lat: 48 + float64(id)*0.00001, Lon: 12}
			e := Edge{To: id, DistM: 1, SpeedKph: 50, HwyType: "residential"}
			adj[previous] = append(adj[previous], e)
			e.To = previous
			adj[id] = append(adj[id], e)
			previous = id
		}
	}
	disk, _ := writeChainTestGraph(t, NewRouterFromGraph(coords, adj))
	if disk.g.store.chainsOffset == 0 {
		t.Fatal("long spokes were not packed")
	}
}

func TestChainGraphRejectsInvalidRecords(t *testing.T) {
	disk, _ := writeChainTestGraph(t, chainTestRouter(12, false))
	valid := bytes.Clone(disk.g.store.data)
	d := disk.g.store
	shape := graphHeaderSize + d.nodeIndex(5)*graphNodeSize
	mutations := map[string]func([]byte){
		"stored count":      func(b []byte) { binary.LittleEndian.PutUint64(b[72:], math.MaxUint64) },
		"chain count":       func(b []byte) { binary.LittleEndian.PutUint64(b[80:], math.MaxUint64) },
		"vertex count":      func(b []byte) { binary.LittleEndian.PutUint64(b[88:], math.MaxUint64) },
		"logical count":     func(b []byte) { binary.LittleEndian.PutUint64(b[16:], 1) },
		"shape chain":       func(b []byte) { binary.LittleEndian.PutUint32(b[shape+24:], 1) },
		"shape position":    func(b []byte) { binary.LittleEndian.PutUint32(b[shape+28:], graphChainFlag) },
		"chain start":       func(b []byte) { binary.LittleEndian.PutUint32(b[d.chainsOffset:], 1) },
		"chain length":      func(b []byte) { binary.LittleEndian.PutUint32(b[d.chainsOffset+4:], math.MaxUint32) },
		"chain attribute":   func(b []byte) { binary.LittleEndian.PutUint32(b[d.chainsOffset+8:], math.MaxUint32) },
		"reserved":          func(b []byte) { b[d.chainsOffset+12] = 1 },
		"vertex node":       func(b []byte) { binary.LittleEndian.PutUint32(b[d.verticesOffset:], math.MaxUint32) },
		"vertex ownership":  func(b []byte) { copy(b[d.verticesOffset+12:], b[d.verticesOffset:d.verticesOffset+4]) },
		"endpoint target":   func(b []byte) { binary.LittleEndian.PutUint32(b[d.edgesOffset:], 5) },
		"endpoint distance": func(b []byte) { b[d.edgesOffset+8] ^= 1 },
		"terminal distance": func(b []byte) { b[d.verticesOffset+11*12+4] = 1 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b := bytes.Clone(valid)
			mutate(b)
			path := filepath.Join(t.TempDir(), "bad.bin")
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
			if r, err := OpenGraph(path); err == nil {
				r.Close()
				t.Fatal("accepted invalid chain graph")
			}
		})
	}
	for _, end := range []int{d.chainsOffset - 1, d.verticesOffset - 1, d.verticesOffset + 12, len(valid) - 1} {
		path := filepath.Join(t.TempDir(), "truncated.bin")
		if err := os.WriteFile(path, valid[:end], 0o600); err != nil {
			t.Fatal(err)
		}
		if r, err := OpenGraph(path); err == nil {
			r.Close()
			t.Fatal("accepted truncated chain graph")
		}
	}
}

func FuzzChainGraphReader(f *testing.F) {
	for _, ring := range []bool{false, true} {
		path := filepath.Join(f.TempDir(), "seed.bin")
		if err := chainTestRouter(12, ring).WriteGraph(context.Background(), path); err != nil {
			f.Fatal(err)
		}
		seed, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		path := filepath.Join(t.TempDir(), "graph.bin")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := OpenGraph(path)
		if err != nil {
			return
		}
		defer r.Close()
		r.g.eachNode(func(id int64, _ Coord) {
			for it := r.g.edges(id); it.next(); {
				_ = it.edge()
			}
		})
		r.ExportGraph()
	})
}

func TestChainGraphReaderByteMutations(t *testing.T) {
	disk, _ := writeChainTestGraph(t, chainTestRouter(12, true))
	seed := bytes.Clone(disk.g.store.data)
	rng := rand.New(rand.NewPCG(11, 29))
	path := filepath.Join(t.TempDir(), "mutated.bin")
	for trial := 0; trial < 1000; trial++ {
		data := bytes.Clone(seed)
		for changes := rng.IntN(4) + 1; changes > 0; changes-- {
			data[rng.IntN(len(data))] ^= byte(1 << rng.IntN(8))
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := OpenGraph(path)
		if err != nil {
			continue
		}
		r.g.eachNode(func(id int64, _ Coord) {
			for it := r.g.edges(id); it.next(); {
				_ = it.edge()
			}
		})
		r.ExportGraph()
		r.Close()
	}
}
