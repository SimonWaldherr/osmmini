package osmmini

import (
	"context"
	"errors"
	"testing"
)

func TestRoutingToDirectedSinkAndCancelledShortRoute(t *testing.T) {
	r := NewRouterFromGraph(map[int64]Coord{1: {Lat: 48, Lon: 12}, 2: {Lat: 48.001, Lon: 12}}, map[int64][]Edge{1: {{To: 2, DistM: 112, SpeedKph: 50, HwyType: "residential"}}})
	for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
		opt := RouteOptions{Engine: engine, Objective: ObjectiveDistance}
		route, err := r.RouteWithOptions(context.Background(), 1, 2, opt)
		if err != nil || len(route.Path) != 2 {
			t.Fatalf("%s: directed sink: %v, %v", engine, route, err)
		}
		cost, err := r.RouteCostWithOptions(context.Background(), 1, 2, opt)
		if err != nil || cost != route.Cost {
			t.Fatalf("%s cost = %v, %v", engine, cost, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := r.RouteWithOptions(ctx, 1, 2, opt); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: %v", engine, err)
		}
		if _, err := r.RouteCostWithOptions(ctx, 1, 1, opt); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancelled same-node route: %v", engine, err)
		}
	}
}

func TestIncompleteCHIsRejectedInsteadOfFallingBackToAStar(t *testing.T) {
	r := NewRouterFromGraph(
		map[int64]Coord{1: {Lat: 48, Lon: 12}, 2: {Lat: 48.001, Lon: 12}},
		map[int64][]Edge{1: {{To: 2, DistM: 112, SpeedKph: 50, HwyType: "residential"}}},
	)
	r.BuildCH()
	options := RouteOptions{Engine: EngineCH, Objective: ObjectiveDistance}
	if _, err := r.RouteWithOptions(context.Background(), 1, 2, options); !errors.Is(err, ErrRouteEngineUnsupported) {
		t.Fatalf("CH route error = %v", err)
	}
	if _, err := r.RouteCostWithOptions(context.Background(), 1, 2, options); !errors.Is(err, ErrRouteEngineUnsupported) {
		t.Fatalf("CH cost error = %v", err)
	}
}

func BenchmarkShortRouteCost(b *testing.B) {
	r, from, _ := buildGridRouter(40, 40)
	for _, engine := range []RouteEngine{EngineAStar, EngineDijkstraNode} {
		b.Run(string(engine), func(b *testing.B) {
			opt := RouteOptions{Engine: engine, Objective: ObjectiveDistance}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := r.RouteCostWithOptions(context.Background(), from, from+1, opt); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestRouteCostOnlyMatchesReconstructedPaths(t *testing.T) {
	r, _, _ := buildGridRouter(8, 8)
	for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
		for _, pair := range [][2]int64{{1, 64}, {10, 3}, {25, 26}, {8, 57}} {
			opt := RouteOptions{Engine: engine, Objective: ObjectiveDistance}
			route, err := r.RouteWithOptions(context.Background(), pair[0], pair[1], opt)
			if err != nil {
				t.Fatal(err)
			}
			cost, err := r.RouteCostWithOptions(context.Background(), pair[0], pair[1], opt)
			if err != nil || cost != route.Cost {
				t.Fatalf("%s %v: cost %v, path cost %v, error %v", engine, pair, cost, route.Cost, err)
			}
		}
	}
}
