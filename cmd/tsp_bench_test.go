package main

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini"
)

func BenchmarkTSPManyStops(b *testing.B) {
	for _, scenario := range []struct {
		name  string
		n     int
		chain bool
	}{{"exact16", 16, false}, {"greedy60", 60, false}, {"dependencies60", 60, true}} {
		b.Run(scenario.name, func(b *testing.B) {
			s, stops, opt := tspFixture(scenario.n, 42)
			pre := make([]uint64, scenario.n)
			if scenario.chain {
				for i := 1; i < scenario.n; i++ {
					pre[i] = 1 << uint(i-1)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				var order []int
				var err error
				if scenario.n <= 16 {
					order, err = s.tspExact(b.Context(), 1, int64(scenario.n+2), stops, pre, opt)
				} else {
					order, err = s.tspGreedy(b.Context(), 1, int64(scenario.n+2), stops, pre, opt)
				}
				if err != nil || len(order) != scenario.n {
					b.Fatalf("order=%v, err=%v", order, err)
				}
			}
		})
	}
}

var benchmarkTripResponse TripSolveResponse

func BenchmarkTripLongLegs60(b *testing.B) {
	const nodes = 10000
	coords := make(map[int64]osmmini.Coord, nodes)
	adj := make(map[int64][]osmmini.Edge, nodes)
	for id := int64(1); id <= nodes; id++ {
		coords[id] = osmmini.Coord{Lat: 48 + float64(id)*0.00001, Lon: 12}
		for _, to := range []int64{id - 1, id + 1} {
			if to > 0 && to <= nodes {
				adj[id] = append(adj[id], osmmini.Edge{To: to, DistM: 2, SpeedKph: 50, HwyType: "residential"})
			}
		}
	}
	s := &server{router: osmmini.NewRouterFromGraph(coords, adj)}
	location := func(node int64) Location {
		c := coords[node]
		return Location{Lat: &c.Lat, Lon: &c.Lon}
	}
	plan := TripPlan{Start: location(1), Loop: true}
	for i := 0; i < 60; i++ {
		node := int64(1)
		if i%2 == 0 {
			node = nodes
		}
		plan.Stops = append(plan.Stops, TripStop{ID: fmt.Sprint(i), Location: location(node)})
	}
	opt := osmmini.RouteOptions{Engine: osmmini.EngineDijkstra, Objective: osmmini.ObjectiveDistance}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		response, err := s.solveTrip(b.Context(), plan, opt)
		if err != nil || len(response.Legs) != 61 {
			b.Fatalf("legs=%d, err=%v", len(response.Legs), err)
		}
		benchmarkTripResponse = response
	}
}

// Opt in with a Niederbayern extract; PBF import is outside the measurement.
func BenchmarkTripNiederbayern60(b *testing.B) {
	path := os.Getenv("OSMMINI_BENCH_NIEDERBAYERN_PBF")
	if path == "" {
		b.Skip("set OSMMINI_BENCH_NIEDERBAYERN_PBF")
	}
	router, _, err := osmmini.BuildRouterWithAddressesOptions(path, osmmini.BuildOptions{Context: b.Context(), SkipAddresses: true, MaxNodes: 8_000_000})
	if err != nil {
		b.Fatal(err)
	}
	s := &server{router: router}
	passauLat, passauLon := 48.574, 13.450
	landshutLat, landshutLon := 48.549, 12.137
	passau := Location{Lat: &passauLat, Lon: &passauLon}
	landshut := Location{Lat: &landshutLat, Lon: &landshutLon}
	for _, location := range []Location{passau, landshut} {
		_, snap, ok := router.NearestNode(*location.Lat, *location.Lon)
		if !ok || snap > 1000 {
			b.Fatal("extract must cover Passau and Landshut")
		}
	}
	plan := TripPlan{Start: passau, Loop: true}
	for i := 0; i < 60; i++ {
		location := landshut
		if i%2 != 0 {
			location = passau
		}
		plan.Stops = append(plan.Stops, TripStop{ID: fmt.Sprint(i), Location: location})
	}
	runtime.GC()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		response, err := s.solveTrip(b.Context(), plan, osmmini.RouteOptions{Engine: osmmini.EngineAStar, Objective: osmmini.ObjectiveDuration})
		if err != nil || len(response.Legs) != 61 {
			b.Fatalf("legs=%d, err=%v", len(response.Legs), err)
		}
		benchmarkTripResponse = response
	}
	b.ReportMetric(float64(router.NodeCount()), "graph-nodes")
}
