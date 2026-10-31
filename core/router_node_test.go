package osmmini

import (
	"math"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func TestNodeSearchRetainsImprovedAndEqualCostPredecessors(t *testing.T) {
	const start int64 = math.MinInt64 + 1
	const target int64 = math.MaxInt64 - 1
	for _, tc := range []struct {
		name string
		adj  map[int64][]Edge
		path []int64
		cost float64
	}{
		{
			name: "improved predecessor through zero ID",
			adj: map[int64][]Edge{
				start: {{To: 0, DistM: 10}, {To: -3, DistM: 1}},
				-3:    {{To: 0, DistM: 1}, {To: target, DistM: 10}},
				0:     {{To: target, DistM: 1}, {To: start, DistM: 0}},
			},
			path: []int64{start, -3, 0, target}, cost: 3,
		},
		{
			name: "equal cost keeps first predecessor",
			adj: map[int64][]Edge{
				start: {{To: 0, DistM: 1}, {To: -3, DistM: 1}},
				0:     {{To: target, DistM: 1}},
				-3:    {{To: target, DistM: 1}},
			},
			path: []int64{start, 0, target}, cost: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Coincident coordinates make every heuristic zero, so the fixture's
			// explicit edge weights exercise the same relaxation order in A*.
			router := NewRouterFromGraph(map[int64]Coord{start: {}, 0: {}, -3: {}, target: {}}, tc.adj)
			for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
				opt := (RouteOptions{Engine: engine, Objective: ObjectiveDistance}).withDefaults()
				path, cost, err := router.nodeSearch(t.Context(), start, target, opt, true)
				if err != nil || cost != tc.cost || !slices.Equal(path, tc.path) {
					t.Fatalf("%s: path=%v, cost=%g, err=%v; want %v, %g", engine, path, cost, err, tc.path, tc.cost)
				}
				if _, cost, err := router.nodeSearch(t.Context(), start, target, opt, false); err != nil || cost != tc.cost {
					t.Fatalf("%s cost-only: %g, %v; want %g", engine, cost, err, tc.cost)
				}
			}
		})
	}
}

func TestNodeSearchMatchesTurnSearchWithoutTransitionCosts(t *testing.T) {
	r, _, _ := buildGridRouter(12, 12)
	for id, edges := range r.g.adj {
		for i := range edges {
			edges[i].SpeedKph = 10 + float64((id+edges[i].To)%9)*10
		}
	}
	file := filepath.Join(t.TempDir(), "graph")
	if err := r.WriteGraph(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(file)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	for _, router := range []*Router{r, disk} {
		for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra} {
			for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
				for _, profile := range []VehicleProfile{"", ProfileCar, ProfileCycling, ProfileWalking} {
					opt := (RouteOptions{Engine: engine, Objective: objective, Profile: profile}).withDefaults()
					for _, pair := range [][2]int64{{1, 144}, {54, 12}, {88, 89}} {
						_, want, _, err := router.astar(t.Context(), pair[0], pair[1], opt, false)
						if err != nil {
							t.Fatal(err)
						}
						got, err := router.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
						if err != nil || math.Abs(got.Cost-want) > 1e-8 {
							t.Fatalf("%s/%s/%s %v: got %g, want %g, error %v", engine, objective, profile, pair, got.Cost, want, err)
						}
						if got.Path[0] != pair[0] || got.Path[len(got.Path)-1] != pair[1] || got.Engine != engine {
							t.Fatalf("invalid result: %+v", got)
						}
						cost, err := router.RouteCostWithOptions(t.Context(), pair[0], pair[1], opt)
						if err != nil || cost != got.Cost {
							t.Fatalf("cost-only differs: %g, %g, %v", cost, got.Cost, err)
						}
					}
				}
			}
		}
	}
}

func TestRoutingRetainsTransitionPenalties(t *testing.T) {
	r := NewRouterFromGraph(map[int64]Coord{
		1: {Lat: 48, Lon: 12}, 2: {Lat: 48, Lon: 12.001},
		3: {Lat: 48.001, Lon: 12.001}, 4: {Lat: 48.001, Lon: 12},
		5: {Lat: 48, Lon: 12.002},
	}, map[int64][]Edge{
		1: {{To: 2, DistM: 100, SpeedKph: 50}, {To: 4, DistM: 110, SpeedKph: 50}},
		2: {{To: 3, DistM: 100, SpeedKph: 50}, {To: 1, DistM: 100, SpeedKph: 50}, {To: 5, DistM: 100, SpeedKph: 50}},
		4: {{To: 3, DistM: 110, SpeedKph: 50}},
	})
	for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra} {
		for _, tc := range []struct {
			weights ProWeights
			path    []int64
		}{
			{ProWeights{}, []int64{1, 2, 3}},
			{ProWeights{LeftTurn: 60}, []int64{1, 4, 3}},
			{ProWeights{NoLeftTurn: true}, []int64{1, 4, 3}},
			{ProWeights{Crossing: 60}, []int64{1, 4, 3}},
			{ProWeights{TrafficLightPenalty: 60}, []int64{1, 4, 3}},
			{ProWeights{RightTurn: 60}, []int64{1, 2, 3}},
		} {
			result, err := r.RouteWithOptions(t.Context(), 1, 3, RouteOptions{Engine: engine, Weights: tc.weights})
			if err != nil || !slices.Equal(result.Path, tc.path) {
				t.Fatalf("%s weights %+v: got %v, want %v, error %v", engine, tc.weights, result.Path, tc.path, err)
			}
		}
	}
}

func TestConcurrentRoutingUsesIndependentSearchState(t *testing.T) {
	r, from, to := buildGridRouter(12, 12)
	want, err := r.RouteCostWithOptions(t.Context(), from, to, RouteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			for range 3 {
				got, err := r.RouteCostWithOptions(t.Context(), from, to, RouteOptions{})
				if err != nil || got != want {
					t.Errorf("concurrent query: %g, %v; want %g", got, err, want)
				}
			}
		})
	}
	wg.Wait()
}

func TestNearestNodeAcrossCellBoundary(t *testing.T) {
	r := NewRouterFromGraph(map[int64]Coord{
		1: {Lat: 48.004, Lon: 12},
		2: {Lat: 48.01001, Lon: 12},
		3: {Lat: 48, Lon: 12},
	}, map[int64][]Edge{
		1: {{To: 2, DistM: 700}}, 2: {{To: 1, DistM: 700}}, 3: {{To: 1, DistM: 450}},
	})
	file := filepath.Join(t.TempDir(), "narrow-region")
	if err := r.WriteGraph(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(file)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	for _, router := range []*Router{r, disk} {
		id, distance, ok := router.NearestNode(48.00999, 12)
		if !ok || id != 2 || distance > 3 {
			t.Fatalf("nearest road across cell boundary: %d, %.2f m, %v", id, distance, ok)
		}
	}
}
