package osmmini

import (
	"cmp"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func snapReference(r *Router, lat, lon float64, limit int, maxMeters float64) []NodeSnap {
	var nodes []NodeSnap
	r.g.eachNode(func(id int64, coordinate Coord) {
		distance := haversineMeters(lat, lon, coordinate.Lat, coordinate.Lon)
		if distance <= maxMeters {
			nodes = append(nodes, NodeSnap{ID: id, DistanceM: distance})
		}
	})
	slices.SortFunc(nodes, func(a, b NodeSnap) int {
		if order := cmp.Compare(a.DistanceM, b.DistanceM); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return nodes[:min(len(nodes), min(limit, 8))]
}

func snapGraphVariants(t *testing.T, memory *Router) map[string]*Router {
	t.Helper()
	root := t.TempDir()
	compact := filepath.Join(root, "compact.bin")
	if err := memory.WriteGraph(t.Context(), compact); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "legacy.bin")
	if err := os.WriteFile(legacy, legacyGraphFixture(t, memory), 0o600); err != nil {
		t.Fatal(err)
	}
	variants := map[string]*Router{"memory": memory}
	for name, path := range map[string]string{"compact": compact, "legacy": legacy} {
		disk, err := OpenGraph(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = disk.Close() })
		variants[name] = disk
	}
	return variants
}

func TestNearestNodesWithSparseSignedIDs(t *testing.T) {
	// Spatial order differs from signed ID order, and OSM IDs cannot be used
	// as compact table indices (including values beyond exact float64 range).
	coords := map[int64]Coord{
		-9223372036854775807: {Lat: 48.0101, Lon: 12.0101},
		-17:                  {Lat: 48.0001, Lon: 12.0002},
		42:                   {Lat: 48.0102, Lon: 12.0001},
		9223372036854775806:  {Lat: 48.0002, Lon: 12.0102},
	}
	memory := NewRouterFromGraph(coords, nil)
	for name, router := range snapGraphVariants(t, memory) {
		t.Run(name, func(t *testing.T) {
			for id, query := range coords {
				nearest, distance, ok := router.NearestNode(query.Lat, query.Lon)
				if !ok || nearest != id || distance != 0 {
					t.Fatalf("nearest at %d: %d, %g, %v", id, nearest, distance, ok)
				}
				want := snapReference(memory, query.Lat, query.Lon, 8, 1000)
				got, err := router.NearestNodes(t.Context(), query.Lat, query.Lon, 8, 1000)
				if err != nil || !slices.Equal(got, want) {
					t.Fatalf("nearby at %d: %+v, %v; want %+v", id, got, err, want)
				}
			}
		})
	}
}

func TestNearestNodesMatchesBruteRadiusForMemoryAndFileGraphs(t *testing.T) {
	coords := map[int64]Coord{}
	var id int64
	for x := 0; x < 21; x++ {
		for y := 0; y < 21; y++ {
			id++
			coords[id] = Coord{Lat: 48 + float64(x)*.001, Lon: 12 + float64(y)*.001}
		}
	}
	// The center crosses an index-cell boundary; nodes on the other side can
	// be closer than ones in the query's own cell.
	memory := NewRouterFromGraph(coords, nil)
	for name, router := range snapGraphVariants(t, memory) {
		t.Run(name, func(t *testing.T) {
			for _, query := range []Coord{
				{Lat: 48.009999, Lon: 12.010001}, {Lat: 48, Lon: 12},
				{Lat: 47.9999, Lon: 12.0001}, {Lat: 49, Lon: 13},
			} {
				for _, radius := range []float64{1, 150, 250, 1000} {
					for _, limit := range []int{1, 3, 8, 100} {
						want := snapReference(memory, query.Lat, query.Lon, limit, radius)
						got, err := router.NearestNodes(t.Context(), query.Lat, query.Lon, limit, radius)
						if err != nil || !slices.Equal(got, want) {
							t.Fatalf("%+v limit=%d radius=%g: got %+v, %v; want %+v", query, limit, radius, got, err, want)
						}
					}
				}
			}
		})
	}
}

func TestNearestNodesIncludesSinksAndOrdersDistanceTiesBySignedID(t *testing.T) {
	memory := NewRouterFromGraph(map[int64]Coord{
		9: {Lat: 48, Lon: 12}, -4: {Lat: 48, Lon: 12},
		1: {Lat: 48.0001, Lon: 12.0001}, 2: {Lat: 48.001, Lon: 12.001},
	}, map[int64][]Edge{1: {{To: 9, DistM: 10, HwyType: "residential"}}})
	for name, router := range snapGraphVariants(t, memory) {
		t.Run(name, func(t *testing.T) {
			got, err := router.NearestNodes(t.Context(), 48, 12, 3, 200)
			if err != nil || len(got) != 3 || got[0].ID != -4 || got[1].ID != 9 || got[2].ID != 1 {
				t.Fatalf("one-way sink and signed ties: %+v, %v", got, err)
			}
			if got[0].DistanceM != 0 || got[1].DistanceM != 0 {
				t.Fatalf("exact coordinate distances: %+v", got)
			}
		})
	}
}

func TestNearestNodesWrapsLongitudeAndIncludesPolarCircles(t *testing.T) {
	for name, fixture := range map[string]struct {
		coords  map[int64]Coord
		queries []Coord
	}{
		"dateline": {
			coords: map[int64]Coord{
				1: {Lat: 48, Lon: 179.999}, 2: {Lat: 48, Lon: -179.999},
				3: {Lat: 48.001, Lon: -180}, 4: {Lat: 48.001, Lon: 180},
			},
			queries: []Coord{{Lat: 48, Lon: 180}, {Lat: 48, Lon: -180}, {Lat: 48, Lon: 179.9995}},
		},
		"north-pole": {
			coords: map[int64]Coord{
				1: {Lat: 89.999, Lon: 0}, 2: {Lat: 89.999, Lon: 180},
				3: {Lat: 89.999, Lon: -180}, 4: {Lat: 89.998, Lon: 90},
			},
			queries: []Coord{{Lat: 90, Lon: 0}, {Lat: 89.9995, Lon: -90}},
		},
		"south-pole": {
			coords: map[int64]Coord{
				1: {Lat: -89.999, Lon: 0}, 2: {Lat: -89.999, Lon: 180},
				3: {Lat: -89.999, Lon: -180}, 4: {Lat: -89.998, Lon: 90},
			},
			queries: []Coord{{Lat: -90, Lon: 0}, {Lat: -89.9995, Lon: 90}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			memory := NewRouterFromGraph(fixture.coords, nil)
			for variant, router := range snapGraphVariants(t, memory) {
				t.Run(variant, func(t *testing.T) {
					for _, query := range fixture.queries {
						for _, radius := range []float64{100, 250, 1000} {
							want := snapReference(memory, query.Lat, query.Lon, 8, radius)
							got, err := router.NearestNodes(t.Context(), query.Lat, query.Lon, 8, radius)
							if err != nil || !slices.Equal(got, want) {
								t.Fatalf("%+v radius=%g: got %+v, %v; want %+v", query, radius, got, err, want)
							}
						}
					}
				})
			}
		})
	}
}

type cancellingSnapContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (ctx *cancellingSnapContext) Err() error {
	ctx.checks++
	if ctx.checks == 3 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestNearestNodesValidationCancellationAndUnindexedMemory(t *testing.T) {
	router := NewRouterFromGraph(map[int64]Coord{1: {Lat: 48, Lon: 12}}, nil)
	for _, invalid := range []struct {
		lat, lon, radius float64
		limit            int
	}{
		{math.NaN(), 12, 250, 3}, {math.Inf(1), 12, 250, 3}, {91, 12, 250, 3},
		{48, math.NaN(), 250, 3}, {48, math.Inf(-1), 250, 3}, {48, 181, 250, 3},
		{48, 12, math.NaN(), 3}, {48, 12, math.Inf(1), 3},
		{48, 12, -1, 3}, {48, 12, 0, 3}, {48, 12, 1001, 3},
		{48, 12, 250, 0}, {48, 12, 250, -1},
	} {
		if got, err := router.NearestNodes(t.Context(), invalid.lat, invalid.lon, invalid.limit, invalid.radius); err == nil || got != nil {
			t.Fatalf("accepted invalid input %+v: %+v, %v", invalid, got, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := router.NearestNodes(ctx, 48, 12, 3, 250); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("pre-cancelled search: %+v, %v", got, err)
	}
	for _, indexed := range []bool{true, false} {
		coords := make(map[int64]Coord)
		for id := int64(1); id <= 200; id++ {
			coords[id] = Coord{Lat: 48 + float64(id)*.000001, Lon: 12}
		}
		memory := NewRouterFromGraph(coords, nil)
		if !indexed {
			memory.idx = nil
		}
		want := snapReference(memory, 48, 12, 3, 250)
		got, err := memory.NearestNodes(nil, 48, 12, 3, 250)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("indexed=%t: got %+v, %v; want %+v", indexed, got, err, want)
		}
		ctx, cancel := context.WithCancel(t.Context())
		during := &cancellingSnapContext{Context: ctx, cancel: cancel}
		got, err = memory.NearestNodes(during, 48, 12, 3, 250)
		cancel()
		if !errors.Is(err, context.Canceled) || got != nil {
			t.Fatalf("indexed=%t mid-query cancellation returned partial nodes: %+v, %v", indexed, got, err)
		}
	}
	if got, err := (&Router{}).NearestNodes(t.Context(), 48, 12, 3, 250); err != nil || len(got) != 0 {
		t.Fatalf("empty graph: %+v, %v", got, err)
	}
}
