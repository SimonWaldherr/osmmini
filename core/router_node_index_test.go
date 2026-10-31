package osmmini

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
)

func TestIndexedNodeSearchMatchesOriginalIDSearch(t *testing.T) {
	grid, _, _ := buildGridRouter(12, 12)
	line := chainTestRouter(40, false)
	ring := chainTestRouter(40, true)
	signed := NewRouterFromGraph(map[int64]Coord{
		math.MinInt64 + 1: {Lat: 48, Lon: 12}, 0: {Lat: 48.001, Lon: 12}, math.MaxInt64 - 1: {Lat: 48.002, Lon: 12}, -3: {Lat: 49, Lon: 13},
	}, map[int64][]Edge{
		math.MinInt64 + 1: {{To: 0, DistM: 112, SpeedKph: 50, HwyType: "residential"}},
		0:                 {{To: math.MaxInt64 - 1, DistM: 113, SpeedKph: 40, HwyType: "service"}},
	})
	store := NewTerritoryStore()
	if err := store.LoadLayerTerritories("routing", []*Territory{
		{ID: "home", Geometry: Geometry{Polygons: []Polygon{{Outer: square(47, 11, 50, 14)}}}},
	}); err != nil {
		t.Fatal(err)
	}
	policy := &TerritoryCostPolicy{Store: store, Layer: "routing", HomeTerritoryID: "home", SameFactor: 1.5}
	for _, memory := range []*Router{grid, line, ring, signed} {
		disk, _ := writeChainTestGraph(t, memory)
		var ids []int64
		memory.g.eachNode(func(id int64, _ Coord) { ids = append(ids, id) })
		slices.Sort(ids)
		pairs := [][2]int64{{ids[0], ids[len(ids)-1]}, {ids[len(ids)-1], ids[0]}, {ids[len(ids)/2], ids[0]}, {ids[0], ids[0]}, {-9999, ids[0]}, {ids[0], -9999}}
		for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
			for _, opt := range []RouteOptions{
				{Profile: ProfileCar, Objective: ObjectiveDistance},
				{Profile: ProfileCar, Objective: ObjectiveDuration},
				{Profile: ProfileCar, Objective: ObjectiveEconomy},
				{Profile: ProfileWalking}, {Profile: ProfileCycling}, {Profile: ProfileTravel},
				{Profile: ProfileDelivery},
				{Pro: true, Weights: ProWeights{LeftTurn: 2, Crossing: 5, NoLeftTurn: true}},
				{Profile: ProfileTruck, Weights: ProWeights{VehicleHeightM: 3, VehicleWeightT: 20}},
				{EmergencyMode: true}, {TerritoryCost: policy},
			} {
				opt.Engine = engine
				for _, pair := range pairs {
					want, wantErr := memory.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
					got, gotErr := disk.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
					if !errors.Is(gotErr, wantErr) {
						t.Fatalf("%s %s %v: errors %v vs %v", engine, opt.Profile, pair, gotErr, wantErr)
					}
					if gotErr != nil {
						continue
					}
					if !slices.Equal(got.Path, want.Path) || got.Cost != want.Cost || got.DistanceM != want.DistanceM || got.DurationS != want.DurationS {
						t.Fatalf("indexed route changed: %s %s %v", engine, opt.Profile, pair)
					}
					cost, err := disk.RouteCostWithOptions(t.Context(), pair[0], pair[1], opt)
					if err != nil || cost != want.Cost {
						t.Fatalf("indexed cost changed: %g, %v", cost, err)
					}
				}
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := disk.RouteWithOptions(ctx, ids[0], ids[len(ids)-1], RouteOptions{}); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestIndexedShortQueriesInLongChain(t *testing.T) {
	// Exercise both query modes at the ends and inside a packed road chain.
	memory := chainTestRouter(20_000, false)
	disk, _ := writeChainTestGraph(t, memory)
	for _, engine := range []RouteEngine{EngineAStar, EngineDijkstraNode} {
		for _, pair := range [][2]int64{{1, 2}, {10_000, 10_001}, {19_999, 20_000}, {20_000, 19_999}} {
			opt := RouteOptions{Engine: engine, Profile: ProfileCar, Objective: ObjectiveDistance}
			want, err := memory.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
			if err != nil {
				t.Fatal(err)
			}
			got, err := disk.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
			if err != nil || !slices.Equal(got.Path, want.Path) || got.Cost != want.Cost || got.DistanceM != want.DistanceM || got.DurationS != want.DurationS {
				t.Fatalf("%s %v: short route changed: %v", engine, pair, err)
			}
			cost, err := disk.RouteCostWithOptions(t.Context(), pair[0], pair[1], opt)
			if err != nil || cost != want.Cost {
				t.Fatalf("%s %v: cost-only route changed: %v", engine, pair, err)
			}
		}
	}
}

func BenchmarkIndexedShortQueries(b *testing.B) {
	memory := chainTestRouter(20_000, false)
	disk, _ := writeChainTestGraph(b, memory)
	for _, wantPath := range []bool{false, true} {
		name := "cost"
		if wantPath {
			name = "path"
		}
		b.Run(name, func(b *testing.B) {
			opt := (RouteOptions{Profile: ProfileCar, Objective: ObjectiveDistance}).withDefaults()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, _, err := disk.indexNodeSearch(b.Context(), 10_000, 10_001, opt, wantPath); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
