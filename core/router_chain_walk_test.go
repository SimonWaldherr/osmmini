package osmmini

import (
	"context"
	"errors"
	"math"
	"os"
	"slices"
	"testing"
)

func TestBatchedChainSearchMatchesUnbatchedDijkstra(t *testing.T) {
	for _, ring := range []bool{false, true} {
		memory := chainTestRouter(400, ring)
		disk, _ := writeChainTestGraph(t, memory)
		for _, pair := range [][2]int64{{1, 400}, {40, 390}, {390, 40}, {20, 210}, {210, 20}} {
			for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
				for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
					opt := RouteOptions{Engine: engine, Objective: objective, Profile: ProfileCar}
					wantOpt := opt
					wantOpt.Engine = EngineDijkstra
					want, err := memory.RouteWithOptions(t.Context(), pair[0], pair[1], wantOpt)
					if err != nil {
						t.Fatal(err)
					}
					got, err := disk.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
					if err != nil || got.Cost != want.Cost || !slices.Equal(got.Path, want.Path) {
						t.Fatalf("ring=%t %v %s %s: different route, %v", ring, pair, engine, objective, err)
					}
					cost, err := disk.RouteCostWithOptions(t.Context(), pair[0], pair[1], opt)
					if err != nil || cost != want.Cost {
						t.Fatalf("cost-only differs: %g/%g, %v", cost, want.Cost, err)
					}
				}
			}
		}
		ctx := &landmarkCancelContext{Context: t.Context(), after: 3}
		if _, err := disk.RouteWithOptions(ctx, 1, 400, RouteOptions{Objective: ObjectiveDistance}); !errors.Is(err, context.Canceled) {
			t.Fatalf("chain cancellation: %v", err)
		}
	}
}

func TestRealChainBatchesMatchIndependentIDSearch(t *testing.T) {
	path := os.Getenv("OSMMINI_REAL_GRAPH")
	if path == "" {
		t.Skip("set OSMMINI_REAL_GRAPH to a prepared Niederbayern graph")
	}
	r, err := OpenGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if index := os.Getenv("OSMMINI_LANDMARKS"); index != "" {
		if err := r.LoadLandmarks(t.Context(), index); err != nil {
			t.Fatal(err)
		}
	}
	var nodes []int64
	for _, city := range []Coord{{Lat: 48.574, Lon: 13.450}, {Lat: 48.549, Lon: 12.137}, {Lat: 48.832, Lon: 12.960}, {Lat: 48.881, Lon: 12.573}} {
		node, _, ok := r.NearestNode(city.Lat, city.Lon)
		if !ok {
			t.Fatal("missing city endpoint")
		}
		nodes = append(nodes, node)
	}
	checked := 0
	for _, pair := range [][2]int{{0, 1}, {1, 0}, {2, 3}, {3, 2}} {
		for _, objective := range []Objective{ObjectiveDistance, ObjectiveEconomy} {
			opt := RouteOptions{Profile: ProfileCar, Objective: objective}
			got, err := r.RouteCostWithOptions(t.Context(), nodes[pair[0]], nodes[pair[1]], opt)
			// A no-op policy selects the original ID-based search and per-edge scorer,
			// bypassing compact labels, batching and ALT for an independent reference.
			opt.Engine = EngineDijkstra
			opt.TerritoryCost = &TerritoryCostPolicy{}
			want, wantErr := r.RouteCostWithOptions(t.Context(), nodes[pair[0]], nodes[pair[1]], opt)
			if !errors.Is(err, wantErr) || (err == nil && math.Abs(got-want) > 1e-10*max(1, want)) {
				t.Fatalf("%v/%s: %.17g vs %.17g, %v/%v", pair, objective, got, want, err, wantErr)
			}
			checked++
		}
	}
	t.Logf("%d real queries match the independent ID-based Dijkstra", checked)
}

func TestChainInteriorGoalWaitsForCheaperQueuedBranch(t *testing.T) {
	coords := map[int64]Coord{}
	edges := map[int64][]Edge{}
	for node := int64(1); node <= 40; node++ {
		coords[node] = Coord{Lat: 48, Lon: 12}
	}
	coords[11] = Coord{Lat: 48.02, Lon: 12}
	for node := int64(1); node <= 40; node++ {
		next := node%40 + 1
		distance := 3000.0
		if node <= 10 {
			distance = 10000
		}
		edges[node] = append(edges[node], Edge{To: next, DistM: distance, SpeedKph: 50, HwyType: "residential"})
		edges[next] = append(edges[next], Edge{To: node, DistM: distance, SpeedKph: 50, HwyType: "residential"})
	}
	disk, _ := writeChainTestGraph(t, NewRouterFromGraph(coords, edges))
	if disk.g.store.chainsOffset == 0 {
		t.Fatal("fixture was not packed")
	}
	for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
		opt := RouteOptions{Engine: engine, Objective: ObjectiveDistance}
		got, err := disk.RouteWithOptions(t.Context(), 1, 11, opt)
		if err != nil || got.Cost != 90000 {
			t.Fatalf("%s accepted the first 100km arrival instead of the 90km optimum: %g, %v", engine, got.Cost, err)
		}
		cost, err := disk.RouteCostWithOptions(t.Context(), 1, 11, opt)
		if err != nil || cost != got.Cost {
			t.Fatalf("%s cost-only differs: %g, %v", engine, cost, err)
		}
	}
}
