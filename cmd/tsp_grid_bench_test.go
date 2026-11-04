package main

import (
	"fmt"
	"math/rand"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

// tspGridFixture places stops on a w x h street grid, so each leg needs a real
// graph search (hundreds to thousands of node expansions) instead of the
// single-edge hops of tspFixture.
func tspGridFixture(w, h, stops int, seed int64) (*server, []stopR) {
	random := rand.New(rand.NewSource(seed))
	const step = 0.001
	coords := make(map[int64]osmmini.Coord, w*h)
	adj := make(map[int64][]osmmini.Edge, w*h)
	id := func(x, y int) int64 { return int64(y*w+x) + 1 }
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			coords[id(x, y)] = osmmini.Coord{Lat: 48 + float64(y)*step, Lon: 12 + float64(x)*step}
		}
	}
	link := func(a, b int64, oneWay bool) {
		ca, cb := coords[a], coords[b]
		dist := 111_000 * step * (1 + 0.2*random.Float64()) // varied lengths avoid ties
		_ = ca
		_ = cb
		adj[a] = append(adj[a], osmmini.Edge{To: b, DistM: dist, SpeedKph: 50, HwyType: "residential"})
		if !oneWay {
			adj[b] = append(adj[b], osmmini.Edge{To: a, DistM: dist, SpeedKph: 50, HwyType: "residential"})
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x+1 < w {
				link(id(x, y), id(x+1, y), random.Intn(10) == 0)
			}
			if y+1 < h {
				link(id(x, y), id(x, y+1), random.Intn(10) == 0)
			}
		}
	}
	list := make([]stopR, stops)
	for i := range list {
		list[i] = stopR{id: fmt.Sprint(i), node: id(random.Intn(w), random.Intn(h))}
	}
	return &server{router: osmmini.NewRouterFromGraph(coords, adj)}, list
}

func BenchmarkTSPGridGreedy(b *testing.B) {
	for _, tc := range []struct {
		name  string
		stops int
		chain bool
	}{{"stops24", 24, false}, {"stops40", 40, false}, {"stops60", 60, false}, {"dependencies60", 60, true}} {
		b.Run(tc.name, func(b *testing.B) {
			s, stops := tspGridFixture(80, 80, tc.stops, 5)
			pre := make([]uint64, tc.stops)
			if tc.chain {
				for i := 1; i < tc.stops; i++ {
					pre[i] = 1 << uint(i-1)
				}
			}
			opt := osmmini.RouteOptions{Objective: osmmini.ObjectiveDuration}
			start, end := stops[0].node, stops[1].node
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				order, err := s.tspGreedy(b.Context(), start, end, stops, pre, opt)
				if err != nil || len(order) != tc.stops {
					b.Fatalf("order=%v err=%v", order, err)
				}
			}
		})
	}
}
