package osmmini

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
)

// The original ID-based implementation remains an independent reference for
// tie ordering, costs and predecessors, including when ALT is active.
func checkTurnSearch(t testing.TB, r *Router, from, to int64, opt RouteOptions) {
	t.Helper()
	opt = opt.withDefaults()
	goal, wantCost, came, wantErr := r.astar(t.Context(), from, to, opt, true)
	var wantPath []int64
	if wantErr == nil {
		wantPath = reconstructPathTurnState(goal, came)
	}
	got, err := r.RouteWithOptions(t.Context(), from, to, opt)
	if !errors.Is(err, wantErr) || err == nil && (!slices.Equal(got.Path, wantPath) || got.Cost != wantCost) {
		t.Fatalf("%d -> %d %+v: path %v cost %.17g err %v; want %v %.17g %v", from, to, opt, got.Path, got.Cost, err, wantPath, wantCost, wantErr)
	}
	cost, err := r.RouteCostWithOptions(t.Context(), from, to, opt)
	if !errors.Is(err, wantErr) || err == nil && cost != wantCost {
		t.Fatalf("%d -> %d %+v: cost %.17g err %v; want %.17g %v", from, to, opt, cost, err, wantCost, wantErr)
	}
}

func TestIndexedTurnSearchMatchesIDSearch(t *testing.T) {
	grid, _, _ := buildGridRouter(9, 9)
	for id, edges := range grid.g.adj {
		for i := range edges {
			edges[i].SpeedKph = 20 + float64((id*7+int64(i)*13)%80)
			if id%7 == 0 {
				edges[i].MaxHeightM, edges[i].MaxWeightT = 2.5, 5
			}
		}
	}
	landmarks, _ := landmarkTestGraph(t)
	signed := NewRouterFromGraph(map[int64]Coord{
		math.MinInt64 + 1: {Lat: 48, Lon: 12},
		0:                 {Lat: 48.001, Lon: 12},
		math.MaxInt64 - 1: {Lat: 48.001, Lon: 12.001},
		-3:                {Lat: 48.002, Lon: 12.001},
		7:                 {Lat: 49, Lon: 13},
	}, map[int64][]Edge{
		math.MinInt64 + 1: {{To: 0, DistM: 112, SpeedKph: 50, HwyType: "residential"}},
		0: {
			{To: math.MinInt64 + 1, DistM: 112, SpeedKph: 50, HwyType: "residential"},
			{To: math.MaxInt64 - 1, DistM: 75, SpeedKph: 40, HwyType: "service"},
			{To: math.MaxInt64 - 1, DistM: 90, SpeedKph: 60, HwyType: "service"},
			{To: 0, SpeedKph: 50},
		},
		math.MaxInt64 - 1: {{To: -3, DistM: 112, SpeedKph: 50, HwyType: "residential"}},
	})
	store := NewTerritoryStore()
	if err := store.LoadLayerTerritories("test", []*Territory{{ID: "home", Geometry: Geometry{Polygons: []Polygon{{Outer: square(47, 11, 50, 14)}}}}}); err != nil {
		t.Fatal(err)
	}
	policy := &TerritoryCostPolicy{Store: store, Layer: "test", HomeTerritoryID: "home", SameFactor: 1.5}
	fixtures := map[string]*Router{"grid": grid, "chain": chainTestRouter(40, false), "ring": chainTestRouter(40, true), "signed": signed}
	for name, memory := range fixtures {
		for format, router := range snapGraphVariants(t, memory) {
			t.Run(name+"/"+format, func(t *testing.T) { checkTurnSearchVariants(t, router, policy) })
		}
	}
	t.Run("landmarks", func(t *testing.T) { checkTurnSearchVariants(t, landmarks, policy) })
}

func checkTurnSearchVariants(t *testing.T, router *Router, policy *TerritoryCostPolicy) {
	t.Helper()
	var ids []int64
	router.g.eachNode(func(id int64, _ Coord) { ids = append(ids, id) })
	slices.Sort(ids)
	pairs := [][2]int64{
		{ids[0], ids[len(ids)-1]}, {ids[len(ids)-1], ids[0]},
		{ids[0], ids[len(ids)/2]}, {ids[len(ids)/2], ids[0]},
		{ids[0], ids[0]}, {-999, ids[0]}, {ids[0], -999}, {-999, -999},
	}
	if _, ok := router.Coord(0); ok {
		pairs = append(pairs, [2]int64{0, math.MaxInt64 - 1}, [2]int64{math.MinInt64 + 1, -3})
	}
	for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra} {
		for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
			for _, opt := range []RouteOptions{
				{Profile: ProfileDelivery},
				{Weights: ProWeights{Crossing: 5, TrafficLightPenalty: 2}},
				{Weights: ProWeights{LeftTurn: 2, RightTurn: 1, UTurn: 10, NoLeftTurn: true}},
				{Profile: ProfileDelivery, Weights: ProWeights{VehicleHeightM: 3, VehicleWeightT: 6}},
				{Profile: ProfileTruck, Weights: ProWeights{Crossing: 2}},
				{Profile: ProfileDelivery, EmergencyMode: true},
				{Profile: ProfileDelivery, TerritoryCost: policy},
			} {
				opt.Engine, opt.Objective = engine, objective
				for _, pair := range pairs {
					checkTurnSearch(t, router, pair[0], pair[1], opt)
				}
			}
		}
	}
}

type cancellingTurnContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancellingTurnContext) Err() error {
	c.checks++
	if c.checks == 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestIndexedTurnSearchCancellationAndConcurrentQueries(t *testing.T) {
	memory, from, to := buildGridRouter(40, 40)
	router, _ := writeChainTestGraph(t, memory)
	opt := RouteOptions{Profile: ProfileDelivery, Engine: EngineDijkstra}
	for _, wantPath := range []bool{false, true} {
		for _, during := range []bool{false, true} {
			ctx, cancel := context.WithCancel(t.Context())
			var query context.Context = ctx
			if during {
				query = &cancellingTurnContext{Context: ctx, cancel: cancel}
			} else {
				cancel()
			}
			if wantPath {
				got, err := router.RouteWithOptions(query, from, to, opt)
				if !errors.Is(err, context.Canceled) || got.Path != nil {
					t.Fatalf("cancelled path returned %+v, %v", got, err)
				}
			} else if got, err := router.RouteCostWithOptions(query, from, to, opt); !errors.Is(err, context.Canceled) || got != 0 {
				t.Fatalf("cancelled cost returned %g, %v", got, err)
			}
			cancel()
		}
	}
	for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
		t.Run(string(objective), func(t *testing.T) {
			t.Parallel()
			options := opt
			options.Objective = objective
			for range 3 {
				checkTurnSearch(t, router, from, to, options)
			}
		})
	}
}

func BenchmarkIndexedTurnSearch(b *testing.B) {
	memory, from, to := buildGridRouter(100, 100)
	router, _ := writeChainTestGraph(b, memory)
	opt := (RouteOptions{Profile: ProfileDelivery}).withDefaults()
	for _, query := range []struct {
		name string
		to   int64
	}{{"short", from + 1}, {"regional", to}} {
		b.Run(query.name, func(b *testing.B) {
			for _, wantPath := range []bool{false, true} {
				name := "cost"
				if wantPath {
					name = "path"
				}
				b.Run(name, func(b *testing.B) {
					for _, indexed := range []bool{false, true} {
						name := "id"
						if indexed {
							name = "indexed"
						}
						b.Run(name, func(b *testing.B) {
							b.ReportAllocs()
							for range b.N {
								var err error
								if indexed {
									_, _, err = router.indexTurnSearch(b.Context(), from, query.to, opt, wantPath)
								} else {
									var goal turnState
									var came map[turnState]turnState
									goal, _, came, err = router.astar(b.Context(), from, query.to, opt, wantPath)
									if err == nil && wantPath {
										_ = reconstructPathTurnState(goal, came)
									}
								}
								if err != nil {
									b.Fatal(err)
								}
							}
						})
					}
				})
			}
		})
	}
}
