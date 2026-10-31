package osmmini

import (
	"context"
	"testing"
)

// buildGridRouter builds a synthetic w x h grid graph (4-connected, ~100m
// edges) for routing benchmarks -- large enough to make per-query overhead
// that scales with graph size (rather than with the route itself) show up,
// which is exactly the kind of regression these benchmarks guard against
// (see router.go: dijkstraNode used to pre-fill a distance map for every
// node in the graph before starting the search).
func buildGridRouter(w, h int) (r *Router, from, to int64) {
	const step = 0.001 // ~111m per grid step at this latitude
	coords := make(map[int64]Coord, w*h)
	adj := make(map[int64][]Edge, w*h)

	id := func(x, y int) int64 { return int64(y*w+x) + 1 } // +1: id 0 is the turnState "no previous edge" sentinel

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			nid := id(x, y)
			coords[nid] = Coord{Lat: 48.0 + float64(y)*step, Lon: 12.0 + float64(x)*step}
		}
	}
	addEdge := func(a, b int64) {
		ca, cb := coords[a], coords[b]
		dist := haversineMeters(ca.Lat, ca.Lon, cb.Lat, cb.Lon)
		adj[a] = append(adj[a], Edge{To: b, DistM: dist, SpeedKph: 50, HwyType: "residential"})
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			nid := id(x, y)
			if x+1 < w {
				addEdge(nid, id(x+1, y))
				addEdge(id(x+1, y), nid)
			}
			if y+1 < h {
				addEdge(nid, id(x, y+1))
				addEdge(id(x, y+1), nid)
			}
		}
	}
	return NewRouterFromGraph(coords, adj), id(0, 0), id(w-1, h-1)
}

func benchmarkRoute(b *testing.B, engine RouteEngine, w, h int) {
	r, from, to := buildGridRouter(w, h)
	opt := RouteOptions{Engine: engine, Objective: ObjectiveDuration}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.RouteCostWithOptions(ctx, from, to, opt); err != nil {
			b.Fatalf("route failed: %v", err)
		}
	}
	// ns/op is the standard `go test -bench` metric already, but report the
	// same number as ms/op too since "how long did one route computation
	// take" is more directly readable that way.
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ms/op")
}

// Corner-to-corner route across a 200x200 (40,000 node) grid -- roughly
// comparable in per-node density to a mid-sized loaded region, though real
// OSM graphs are far larger; the point of the benchmark is relative
// engine/change comparison, not an absolute production number.
func BenchmarkRouteAStar(b *testing.B)        { benchmarkRoute(b, EngineAStar, 200, 200) }
func BenchmarkRouteDijkstra(b *testing.B)     { benchmarkRoute(b, EngineDijkstra, 200, 200) }
func BenchmarkRouteDijkstraNode(b *testing.B) { benchmarkRoute(b, EngineDijkstraNode, 200, 200) }

// A second, larger graph specifically stresses dijkstraNode's per-query
// fixed cost (it used to scale with total graph size, not route length --
// see the "lazy dist/prev" comment in dijkstraNode). If that regresses,
// this benchmark's ns/op will grow much faster than BenchmarkRouteAStar's
// as w/h increase.
func BenchmarkRouteDijkstraNodeLargeGraph(b *testing.B) {
	benchmarkRoute(b, EngineDijkstraNode, 600, 600)
}
