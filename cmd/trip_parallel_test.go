package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

func TestParallelMapPreservesOrderAndBoundsConcurrentSearches(t *testing.T) {
	for _, n := range []int{0, 1, 32} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var active, peak atomic.Int32
			calls := make([]atomic.Int32, n)
			results, err := parallelMap(t.Context(), n, func(i int) int {
				current := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); current > old; old = peak.Load() {
					if peak.CompareAndSwap(old, current) {
						break
					}
				}
				calls[i].Add(1)
				runtime.Gosched()
				return i * 17
			})
			if err != nil || len(results) != n {
				t.Fatalf("results=%v, err=%v", results, err)
			}
			for i, result := range results {
				if result != i*17 || calls[i].Load() != 1 {
					t.Fatalf("job %d: result=%d, calls=%d", i, result, calls[i].Load())
				}
			}
			if peak.Load() > maxTripRouteWorkers {
				t.Fatalf("concurrent searches=%d, limit=%d", peak.Load(), maxTripRouteWorkers)
			}
		})
	}
}

func TestParallelMapStopsQueuedJobsAndWaitsForRunningJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	var once sync.Once
	var calls, finished atomic.Int32
	done := make(chan error, 1)
	go func() {
		_, err := parallelMap(ctx, 64, func(i int) int {
			calls.Add(1)
			once.Do(func() { close(started) })
			<-ctx.Done()
			finished.Add(1)
			return i
		})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("pool error=%v, want cancellation", err)
	}
	if calls.Load() > maxTripRouteWorkers || finished.Load() != calls.Load() {
		t.Fatalf("started=%d, finished=%d", calls.Load(), finished.Load())
	}
}

func TestSolveTripAssemblesParallelLegsInStopOrder(t *testing.T) {
	s := routeExportFixture(t)
	location := func(node int64) Location {
		c, _ := s.router.Coord(node)
		return Location{Lat: &c.Lat, Lon: &c.Lon}
	}
	opt := osmmini.RouteOptions{Engine: osmmini.EngineDijkstra, Objective: osmmini.ObjectiveDistance}
	for _, n := range []int{0, 1, 20, 60} {
		for _, loop := range []bool{false, true} {
			t.Run(fmt.Sprintf("stops=%d/loop=%v", n, loop), func(t *testing.T) {
				end := location(3)
				plan := TripPlan{Start: location(1), End: &end, Loop: loop}
				nodes := []int64{1}
				var ids []string
				for i := 0; i < n; i++ {
					node := int64(1 + (i/2)%3) // Include consecutive stops at the same node.
					id := fmt.Sprintf("stop-%d", i)
					plan.Stops = append(plan.Stops, TripStop{ID: id, Location: location(node)})
					ids = append(ids, id)
					nodes = append(nodes, node)
				}
				endNode := int64(3)
				if loop {
					endNode = 1
				}
				nodes = append(nodes, endNode)
				inputs := append([]string{"START"}, ids...)
				inputs = append(inputs, "END")
				response, err := s.solveTrip(t.Context(), plan, opt)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(response.Order, ids) || len(response.Legs) != len(nodes)-1 {
					t.Fatalf("order=%v, legs=%d", response.Order, len(response.Legs))
				}
				var path []osmmini.Coord
				var distance, duration, cost float64
				for i, leg := range response.Legs {
					want, err := s.router.RouteWithOptions(t.Context(), nodes[i], nodes[i+1], opt)
					if err != nil {
						t.Fatal(err)
					}
					if leg.From.Node != nodes[i] || leg.To.Node != nodes[i+1] || !slices.Equal(leg.Path, want.PathCoords) {
						t.Fatalf("leg %d: nodes %d->%d, path=%v", i, leg.From.Node, leg.To.Node, leg.Path)
					}
					if leg.From.Input != inputs[i] || leg.To.Input != inputs[i+1] {
						t.Fatalf("leg %d lost stop labels: %s->%s", i, leg.From.Input, leg.To.Input)
					}
					if i == 0 {
						path = append(path, want.PathCoords...)
					} else {
						path = append(path, want.PathCoords[1:]...)
					}
					distance += want.DistanceM
					duration += want.DurationS
					cost += want.Cost
				}
				if !slices.Equal(response.Path, path) || response.DistanceM != distance || response.DurationS != duration || response.Cost != cost {
					t.Fatal("assembled trip differs from sequential route results")
				}
			})
		}
	}
}

func TestSolveTripLegAppendCannotOverwriteSharedGeometry(t *testing.T) {
	s := routeExportFixture(t)
	lat, lon := 48.002, 12.0
	start := Location{Lat: &lat, Lon: &lon}
	stopLat := 48.0
	plan := TripPlan{Start: start, Loop: true, Stops: []TripStop{{ID: "stop", Location: Location{Lat: &stopLat, Lon: &lon}}}}
	response, err := s.solveTrip(t.Context(), plan, osmmini.RouteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(response.Path)
	for _, leg := range response.Legs {
		_ = append(leg.Path, osmmini.Coord{Lat: 90, Lon: 180})
	}
	if !slices.Equal(response.Path, want) {
		t.Fatal("appending to a leg overwrote the combined path")
	}
}

func TestSolveTripCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (&server{}).solveTrip(ctx, TripPlan{}, osmmini.RouteOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled trip: %v", err)
	}
	s := routeExportFixture(t)
	lat, lon := 48.001, 12.0
	plan := TripPlan{Start: Location{Lat: &lat, Lon: &lon}, Loop: true}
	for range 20 {
		plan.Stops = append(plan.Stops, TripStop{Location: plan.Start})
	}
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	during := &cancelOnCheck{Context: base, cancel: cancel}
	during.remaining.Store(8)
	response, err := s.solveTrip(during, plan, osmmini.RouteOptions{})
	if !errors.Is(err, context.Canceled) || len(response.Legs) != 0 {
		t.Fatalf("cancelled trip: err=%v, legs=%d", err, len(response.Legs))
	}
}
