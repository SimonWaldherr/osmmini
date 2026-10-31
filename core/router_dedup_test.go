package osmmini

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestImportedEdgeDedupPreservesOrderAndAllRoutingAttributeBits(t *testing.T) {
	base := Edge{To: 2, DistM: 10, SpeedKph: 50, HwyType: "residential"}
	var expected []Edge
	for _, field := range []string{"base", "target", "distance", "speed", "height", "weight", "class", "negative-zero", "nan-one", "nan-two"} {
		edge := base
		switch field {
		case "target":
			edge.To = 3
		case "distance":
			edge.DistM = math.Nextafter(edge.DistM, math.Inf(1))
		case "speed":
			edge.SpeedKph = 80
		case "height":
			edge.MaxHeightM = 3
		case "weight":
			edge.MaxWeightT = 7.5
		case "class":
			edge.HwyType = "service"
		case "negative-zero":
			edge.MaxHeightM = math.Copysign(0, -1)
		case "nan-one":
			edge.SpeedKph = math.Float64frombits(0x7ff8000000000001)
		case "nan-two":
			edge.SpeedKph = math.Float64frombits(0x7ff8000000000002)
		}
		expected = append(expected, edge)
	}
	for _, count := range []int{3, len(expected)} {
		var input []Edge
		for _, edge := range expected[:count] {
			input = append(input, edge, edge)
		}
		// These node slices deliberately share one backing array, as imported
		// adjacency does. Clearing duplicates must never touch another node.
		storage := append(input, Edge{To: 1, DistM: 99, HwyType: "track"})
		adj := map[int64][]Edge{1: storage[:len(input):len(input)], 2: storage[len(input):]}
		removed, err := deduplicateImportedEdges(t.Context(), adj)
		if err != nil || removed != count || len(adj[1]) != count || cap(adj[1]) != count {
			t.Fatalf("count=%d: removed=%d, edges=%+v, error=%v", count, removed, adj[1], err)
		}
		for index, want := range expected[:count] {
			got := adj[1][index]
			if got.To != want.To || got.HwyType != want.HwyType ||
				math.Float64bits(got.DistM) != math.Float64bits(want.DistM) ||
				math.Float64bits(got.SpeedKph) != math.Float64bits(want.SpeedKph) ||
				math.Float64bits(got.MaxHeightM) != math.Float64bits(want.MaxHeightM) ||
				math.Float64bits(got.MaxWeightT) != math.Float64bits(want.MaxWeightT) {
				t.Fatalf("edge %d lost order or attribute bits: got %+v, want %+v", index, got, want)
			}
		}
		if len(adj[2]) != 1 || adj[2][0].To != 1 || adj[2][0].DistM != 99 {
			t.Fatalf("compaction changed neighboring adjacency: %+v", adj[2])
		}
		if removed, err := deduplicateImportedEdges(t.Context(), adj); err != nil || removed != 0 {
			t.Fatalf("second pass was not idempotent: %d, %v", removed, err)
		}
	}
}

func TestRoadImportDeduplicatesOverlappingWaysAndRetainsStreetNamesAndOneWaySinks(t *testing.T) {
	table := []string{"", "highway", "residential", "name", "Main", "Alternate", "maxspeed", "80", "oneway", "yes", "East", "Other East"}
	var group []byte
	for _, id := range []int64{10, 20, 30} {
		group = append(group, testPBFPrimitiveGroup(1, testPBFNode(id, nil, nil, 480000000+id*1000, 120000000+id*500))...)
	}
	group = append(group, testPBFPrimitiveGroup(3, testPBFWay(1, []uint64{1, 3}, []uint64{2, 4}, []int64{10, 10}))...)
	group = append(group, testPBFPrimitiveGroup(3, testPBFWay(2, []uint64{1, 3}, []uint64{2, 5}, []int64{10, 10}))...)
	group = append(group, testPBFPrimitiveGroup(3, testPBFWay(3, []uint64{1, 6}, []uint64{2, 7}, []int64{10, 10}))...)
	group = append(group, testPBFPrimitiveGroup(3, testPBFWay(4, []uint64{1, 3, 8}, []uint64{2, 10, 9}, []int64{20, 10}))...)
	group = append(group, testPBFPrimitiveGroup(3, testPBFWay(5, []uint64{1, 3, 8}, []uint64{2, 11, 9}, []int64{20, 10}))...)
	path := filepath.Join(t.TempDir(), "overlap.osm.pbf")
	if err := os.WriteFile(path, testPBFData(table, group), 0o600); err != nil {
		t.Fatal(err)
	}
	router, _, err := BuildRouterWithAddressesOptions(path, BuildOptions{Context: t.Context(), SkipAddresses: true})
	if err != nil {
		t.Fatal(err)
	}
	if router.NodeCount() != 3 || router.EdgeCount() != 5 {
		t.Fatalf("graph=%d nodes/%d edges, want3/5", router.NodeCount(), router.EdgeCount())
	}
	coords, adj := router.ExportGraph()
	if _, ok := coords[30]; !ok || len(adj[30]) != 0 {
		t.Fatal("one-way sink lost")
	}
	if len(adj[10]) != 2 || adj[10][0].To != 20 || adj[10][1].To != 20 || adj[10][0].SpeedKph == 80 || adj[10][1].SpeedKph != 80 {
		t.Fatalf("distinct speed alternatives or order changed: %+v", adj[10])
	}
	for _, name := range []string{"Main", "Alternate", "East", "Other East"} {
		got := router.SearchStreets(name, 10)
		found := false
		for _, result := range got {
			found = found || result.Name == name
		}
		if !found {
			t.Fatalf("street metadata %q lost: %+v", name, got)
		}
	}
	graphPath := filepath.Join(t.TempDir(), "graph.bin")
	if err := router.WriteGraph(t.Context(), graphPath); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(graphPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	diskCoords, diskAdj := disk.ExportGraph()
	if !reflect.DeepEqual(coords, diskCoords) || !reflect.DeepEqual(adj, diskAdj) {
		t.Fatal("compact serialization changed retained graph")
	}
	for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration} {
		options := RouteOptions{Profile: ProfileCar, Objective: objective}
		want, err := router.RouteWithOptions(t.Context(), 10, 30, options)
		if err != nil {
			t.Fatal(err)
		}
		got, err := disk.RouteWithOptions(t.Context(), 10, 30, options)
		if err != nil || !reflect.DeepEqual(got, want) || len(got.Path) != 3 {
			t.Fatalf("route %s changed: %+v %v; want %+v", objective, got, err, want)
		}
		if _, err := disk.RouteWithOptions(t.Context(), 30, 10, options); !errors.Is(err, ErrRouteStartUnreachable) {
			t.Fatalf("one-way sink acquired outgoing route: %v", err)
		}
	}
}

func TestExplicitGraphConstructorAndSnapshotsKeepCallerParallelEdges(t *testing.T) {
	edge := Edge{To: 2, DistM: 10, SpeedKph: 50, HwyType: "residential"}
	adj := map[int64][]Edge{1: {edge, edge}}
	router := NewRouterFromGraph(map[int64]Coord{1: {Lat: 48, Lon: 12}, 2: {Lat: 48.001, Lon: 12.001}}, adj)
	if router.EdgeCount() != 2 || len(adj[1]) != 2 {
		t.Fatal("explicit caller graph was deduplicated")
	}
	path := filepath.Join(t.TempDir(), "graph.bin")
	if err := router.WriteGraph(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if disk.EdgeCount() != 2 || len(adj[1]) != 2 {
		t.Fatal("snapshot mutated caller edge alternatives")
	}
}

func TestImportedEdgeDedupRemovesArtificialJunctionPenalty(t *testing.T) {
	left := Edge{To: 1, DistM: 10, HwyType: "residential"}
	right := Edge{To: 3, DistM: 10, HwyType: "residential"}
	adj := map[int64][]Edge{2: {left, right, left, right}}
	router := NewRouterFromGraph(map[int64]Coord{
		1: {Lat: 48, Lon: 12}, 2: {Lat: 48.001, Lon: 12}, 3: {Lat: 48.002, Lon: 12},
	}, adj)
	eval := newRouteEvaluator(RouteOptions{Weights: ProWeights{Crossing: 7, TrafficLightPenalty: 5}}.withDefaults())
	if got := eval.prepareTransition(router, 1, 2).crossing; got != 12 {
		t.Fatalf("fixture does not reproduce duplicate-inflated junction degree: %g", got)
	}
	if removed, err := deduplicateImportedEdges(t.Context(), adj); err != nil || removed != 2 {
		t.Fatalf("deduplication failed: %d, %v", removed, err)
	}
	if got := eval.prepareTransition(router, 1, 2).crossing; got != 0 {
		t.Fatalf("two-neighbor road still receives a junction penalty: %g", got)
	}
}

type cancellingDedupContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (ctx *cancellingDedupContext) Err() error {
	ctx.checks++
	if ctx.checks == 3 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestImportedEdgeDedupHonorsCancellation(t *testing.T) {
	edges := make([]Edge, 1000)
	for index := range edges {
		edges[index] = Edge{To: int64(index), DistM: 1}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := deduplicateImportedEdges(ctx, map[int64][]Edge{1: edges}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled import: %v", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	during := &cancellingDedupContext{Context: ctx, cancel: cancel}
	if _, err := deduplicateImportedEdges(during, map[int64][]Edge{1: edges}); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-import cancellation: %v", err)
	}
}
