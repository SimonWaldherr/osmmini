package main

import (
	"math/rand"
	"slices"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

// Prefetching only changes when legs are priced, never which moves are taken:
// the result must equal the strictly sequential search, including instances
// with one-way streets, prerequisites and an unreachable stop.
func TestTSP2OptPrefetchMatchesSequentialSearch(t *testing.T) {
	improvedInstances := 0
	for seed := int64(1); seed <= 12; seed++ {
		count := 6 + int(seed)*2 // 8 .. 30 stops
		s, stops := tspGridFixture(24, 24, count, seed)
		random := rand.New(rand.NewSource(seed))
		pre := make([]uint64, count)
		switch seed % 3 {
		case 1: // sparse prerequisites between earlier and later stops of a permutation
			perm := random.Perm(count)
			for i, stop := range perm {
				for _, earlier := range perm[:i] {
					if random.Intn(6) == 0 {
						pre[stop] |= 1 << uint(earlier)
					}
				}
			}
		case 2: // a strict chain: 2-opt can reverse nothing
			for i := 1; i < count; i++ {
				pre[i] = 1 << uint(i-1)
			}
		}
		if seed%4 == 0 { // one stop on an isolated node
			stops[count/2].node = 1 << 40
		}
		opt := osmmini.RouteOptions{Objective: osmmini.ObjectiveDuration}
		// Start from a deliberately poor but feasible order so that moves are taken.
		initial := make([]int, 0, count)
		visited := uint64(0)
		for len(initial) < count {
			for i := 0; i < count; i++ {
				bit := uint64(1) << uint(i)
				if visited&bit == 0 && visited&pre[i] == pre[i] {
					initial = append(initial, i)
					visited |= bit
					break
				}
			}
		}
		start, end := stops[0].node, stops[1].node
		sequential := tsp2optSearch(t.Context(), start, end, stops, pre, append([]int(nil), initial...), s.tripCosts(t.Context(), opt), false)
		prefetched := tsp2optSearch(t.Context(), start, end, stops, pre, append([]int(nil), initial...), s.tripCosts(t.Context(), opt), true)
		if !slices.Equal(sequential, prefetched) {
			t.Fatalf("seed %d (%d stops): prefetched order %v, sequential %v", seed, count, prefetched, sequential)
		}
		if !slices.Equal(sequential, initial) {
			improvedInstances++
		}
	}
	if improvedInstances < 4 {
		t.Fatalf("only %d instances took 2-opt moves; the comparison is not exercising them", improvedInstances)
	}
}
