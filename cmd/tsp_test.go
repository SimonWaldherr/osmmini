package main

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"slices"
	"sync/atomic"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

func tspFixture(n int, seed int64) (*server, []stopR, osmmini.RouteOptions) {
	random := rand.New(rand.NewSource(seed))
	coords := make(map[int64]osmmini.Coord)
	adj := make(map[int64][]osmmini.Edge)
	stops := make([]stopR, n)
	for i := 0; i < n+2; i++ {
		id := int64(i + 1)
		coords[id] = osmmini.Coord{Lat: 48 + float64(i)*0.001, Lon: 12}
		for j := 0; j < n+2; j++ {
			if i != j {
				adj[id] = append(adj[id], osmmini.Edge{To: int64(j + 1), DistM: float64(1 + random.Intn(100)), SpeedKph: 50, HwyType: "residential"})
			}
		}
		if i < n {
			stops[i] = stopR{node: int64(i + 2)}
		}
	}
	return &server{router: osmmini.NewRouterFromGraph(coords, adj)}, stops, osmmini.RouteOptions{Engine: osmmini.EngineDijkstra, Objective: osmmini.ObjectiveDistance}
}

func TestTSPExactMatchesBruteForceDirectedCosts(t *testing.T) {
	for seed := int64(0); seed < 10; seed++ {
		s, stops, opt := tspFixture(5, seed)
		if seed%2 != 0 {
			opt.Objective = osmmini.ObjectiveDuration
		}
		pre := []uint64{0, 0, 1, 0, 2} // 0 before 2, 1 before 4
		cost := make([][]float64, 7)
		for i := range cost {
			cost[i] = make([]float64, 7)
			for j := range cost[i] {
				c, err := s.router.RouteCostWithOptions(context.Background(), int64(i+1), int64(j+1), opt)
				if err != nil {
					t.Fatal(err)
				}
				cost[i][j] = c
			}
		}
		evaluate := func(order []int) float64 {
			total := 0.0
			prev := 0
			mask := uint64(0)
			for _, idx := range order {
				if mask&pre[idx] != pre[idx] {
					return math.Inf(1)
				}
				mask |= 1 << uint(idx)
				total += cost[prev][idx+1]
				prev = idx + 1
			}
			return total + cost[prev][6]
		}
		best := math.Inf(1)
		var visit func([]int, uint64)
		visit = func(order []int, mask uint64) {
			if len(order) == 5 {
				best = math.Min(best, evaluate(order))
				return
			}
			for i := 0; i < 5; i++ {
				if mask&(1<<uint(i)) == 0 {
					visit(append(order, i), mask|(1<<uint(i)))
				}
			}
		}
		visit(nil, 0)
		order, err := s.tspExact(context.Background(), 1, 7, stops, pre, opt)
		if err != nil {
			t.Fatal(err)
		}
		if len(order) != 5 || evaluate(order) != best {
			t.Fatalf("seed %d: order %v cost %v, optimum %v", seed, order, evaluate(order), best)
		}
	}
}

func TestTSPRejectsCyclesAndCancellationBeforeRouting(t *testing.T) {
	s := &server{} // A valid early rejection must not access a router.
	stops := make([]stopR, 2)
	for _, solve := range []func(context.Context, int64, int64, []stopR, []uint64, osmmini.RouteOptions) ([]int, error){s.tspExact, s.tspGreedy} {
		if _, err := solve(context.Background(), 1, 2, stops, []uint64{2, 1}, osmmini.RouteOptions{}); err == nil {
			t.Fatal("cycle accepted")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := solve(ctx, 1, 2, stops, []uint64{0, 0}, osmmini.RouteOptions{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	}
}

func TestTSP2OptCanImproveFirstStop(t *testing.T) {
	coords := make(map[int64]osmmini.Coord)
	adj := make(map[int64][]osmmini.Edge)
	// Directed chain: start -> stop 1 -> stop 0 -> stop 2 -> stop 3 -> end.
	chain := []int64{1, 3, 2, 4, 5, 6}
	for _, id := range chain {
		coords[id] = osmmini.Coord{Lat: 48, Lon: 12}
		for _, to := range chain {
			if id != to {
				adj[id] = append(adj[id], osmmini.Edge{To: to, DistM: 100, SpeedKph: 50, HwyType: "residential"})
			}
		}
	}
	for i := 0; i < len(chain)-1; i++ {
		adj[chain[i]] = append(adj[chain[i]], osmmini.Edge{To: chain[i+1], DistM: 1, SpeedKph: 50, HwyType: "residential"})
	}
	s := &server{router: osmmini.NewRouterFromGraph(coords, adj)}
	stops := []stopR{{node: 2}, {node: 3}, {node: 4}, {node: 5}}
	opt := osmmini.RouteOptions{Engine: osmmini.EngineDijkstra, Objective: osmmini.ObjectiveDistance}
	order := s.tsp2opt(context.Background(), 1, 6, stops, make([]uint64, 4), opt, []int{0, 1, 2, 3})
	if order[0] != 1 {
		t.Fatalf("first stop never improved: %v", order)
	}
	order = s.tsp2opt(context.Background(), 1, 6, stops, []uint64{0, 1, 0, 0}, opt, []int{0, 1, 2, 3})
	seen := uint64(0)
	for _, i := range order {
		if i == 1 && seen&1 == 0 {
			t.Fatal("2-opt violated dependency")
		}
		seen |= 1 << uint(i)
	}
}

func BenchmarkTSPExact12(b *testing.B) {
	s, stops, opt := tspFixture(12, 42)
	pre := make([]uint64, 12)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.tspExact(context.Background(), 1, 14, stops, pre, opt); err != nil {
			b.Fatal(err)
		}
	}
}

func TestTSPExactKeepsStopsAtSameNodeAndRejectsDisconnectedTour(t *testing.T) {
	s, _, opt := tspFixture(2, 1)
	stops := []stopR{{node: 2}, {node: 2}}
	order, err := s.tspExact(context.Background(), 1, 4, stops, []uint64{2, 0}, opt)
	if err != nil || len(order) != 2 || order[0] != 1 || order[1] != 0 {
		t.Fatalf("coincident stops: %v, %v", order, err)
	}
	s.router = osmmini.NewRouterFromGraph(map[int64]osmmini.Coord{1: {}, 2: {}, 3: {}, 4: {}}, nil)
	if _, err := s.tspExact(context.Background(), 1, 4, stops, []uint64{0, 0}, opt); err == nil {
		t.Fatal("disconnected tour accepted")
	}
}

func TestTSPGreedyPreservesAllStopsAndDependencies(t *testing.T) {
	s, stops, opt := tspFixture(20, 41)
	pre := make([]uint64, 20)
	for i := 1; i < 20; i += 2 {
		pre[i] = 1 << uint(i-1)
	}
	order, err := s.tspGreedy(context.Background(), 1, 22, stops, pre, opt)
	if err != nil {
		t.Fatal(err)
	}
	mask := uint64(0)
	for _, i := range order {
		if i < 0 || i >= 20 || mask&(1<<uint(i)) != 0 || mask&pre[i] != pre[i] {
			t.Fatalf("invalid order: %v", order)
		}
		mask |= 1 << uint(i)
	}
	if mask != (1<<20)-1 {
		t.Fatalf("missing stops: %v", order)
	}
}

func TestTSPMaximumStopsAtSameNodeKeepDependenciesAndTieOrder(t *testing.T) {
	s := routeExportFixture(t)
	opt := osmmini.RouteOptions{Engine: osmmini.EngineDijkstra, Objective: osmmini.ObjectiveDuration}
	t.Run("exact16", func(t *testing.T) {
		stops := make([]stopR, 16)
		pre := make([]uint64, len(stops))
		for i := range stops {
			stops[i].node = 2
			if i+1 < len(stops) {
				pre[i] = 1 << uint(i+1)
			}
		}
		order, err := s.tspExact(t.Context(), 1, 3, stops, pre, opt)
		if err != nil || len(order) != len(stops) {
			t.Fatalf("order=%v, err=%v", order, err)
		}
		for i, stop := range order {
			if stop != len(stops)-1-i {
				t.Fatalf("reverse dependency chain: %v", order)
			}
		}
	})
	t.Run("greedy60", func(t *testing.T) {
		stops := make([]stopR, 60)
		pre := make([]uint64, len(stops))
		for i := range stops {
			stops[i].node = 2
			if i%2 == 0 {
				pre[i] = 1 << uint(i+1)
			}
		}
		order, err := s.tspGreedy(t.Context(), 1, 3, stops, pre, opt)
		if err != nil || len(order) != len(stops) {
			t.Fatalf("order=%v, err=%v", order, err)
		}
		for i, stop := range order {
			if stop != i^1 {
				t.Fatalf("coincident stop tie order: %v", order)
			}
		}
	})
}

// Cancel during a cost lookup deterministically, without timing-sensitive sleeps.
// Cost lookups now run on parallel workers, so the counter must be atomic.
type cancelOnCheck struct {
	context.Context
	cancel    context.CancelFunc
	remaining atomic.Int32
}

func (c *cancelOnCheck) Err() error {
	if c.remaining.Add(-1) == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestTSPCancellationDuringCostLookup(t *testing.T) {
	s, stops, opt := tspFixture(5, 7)
	for _, solve := range []func(context.Context, int64, int64, []stopR, []uint64, osmmini.RouteOptions) ([]int, error){s.tspExact, s.tspGreedy} {
		base, cancel := context.WithCancel(context.Background())
		ctx := &cancelOnCheck{Context: base, cancel: cancel}
		ctx.remaining.Store(4)
		_, err := solve(ctx, 1, 7, stops, make([]uint64, 5), opt)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation became a routing error: %v", err)
		}
	}
}

func TestTSP2OptRepairsUnreachableEnd(t *testing.T) {
	stops := []stopR{{node: 1}, {node: 2}, {node: 3}, {node: 4}}
	cost := func(from, to int64) (float64, error) {
		if from == 4 && to == 5 {
			return 0, errors.New("unreachable end")
		}
		return 1, nil
	}
	order := tsp2optCosts(t.Context(), 0, 5, stops, make([]uint64, 4), []int{0, 1, 2, 3}, cost)
	if stops[order[len(order)-1]].node == 4 {
		t.Fatalf("end is still unreachable after 2-opt: %v", order)
	}
}

func TestTSP2OptDoesNotIncreaseRoundedTourCost(t *testing.T) {
	const n = 8
	stops := make([]stopR, n)
	for i := range stops {
		stops[i].node = int64(i + 1)
	}
	for seed := int64(0); seed < 50; seed++ {
		random := rand.New(rand.NewSource(seed))
		from, to := make([]float64, n+2), make([]float64, n+2)
		for i := range from {
			from[i] = random.Float64() * 10
			to[i] = random.Float64() * 10
		}
		cost := func(a, b int64) (float64, error) { return from[a] + to[b], nil }
		total := func(order []int) float64 {
			var sum float64
			previous := int64(0)
			for _, i := range order {
				c, _ := cost(previous, stops[i].node)
				sum += c
				previous = stops[i].node
			}
			c, _ := cost(previous, n+1)
			return sum + c
		}
		order := random.Perm(n)
		before := total(order)
		order = tsp2optCosts(t.Context(), 0, n+1, stops, make([]uint64, n), order, cost)
		if after := total(order); after > before {
			t.Fatalf("seed %d: cost increased from %.17g to %.17g", seed, before, after)
		}
	}
}

func TestParallelMapHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var calls atomic.Int32
	_, err := parallelMap(ctx, 32, func(i int) int { calls.Add(1); return i })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pool error = %v, want cancellation", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("cancelled pool started %d jobs", got)
	}
}

func TestTSP2OptMatchesFullDirectedTourEvaluation(t *testing.T) {
	const n = 7
	stops := make([]stopR, n)
	for i := range stops {
		stops[i].node = int64(i + 1)
	}
	for seed := int64(0); seed < 30; seed++ {
		random := rand.New(rand.NewSource(seed))
		var weights [n + 2][n + 2]float64
		for i := range weights {
			for j := range weights[i] {
				weights[i][j] = float64(random.Intn(30) + 1)
				if random.Intn(10) == 0 {
					weights[i][j] = math.Inf(1)
				}
			}
		}
		pre := make([]uint64, n)
		for i := 1; i < n; i++ {
			if random.Intn(3) == 0 {
				pre[i] = 1 << uint(random.Intn(i))
			}
		}
		evaluate := func(order []int) float64 {
			var total float64
			var mask uint64
			previous := 0
			for _, i := range order {
				if mask&pre[i] != pre[i] {
					return math.Inf(1)
				}
				mask |= 1 << uint(i)
				total += weights[previous][i+1]
				previous = i + 1
			}
			return total + weights[previous][n+1]
		}
		initial := []int{0, 1, 2, 3, 4, 5, 6}
		want := slices.Clone(initial)
		best := evaluate(want)
		for improved := true; improved; {
			improved = false
			for l := 0; l < n-1; l++ {
				for r := l + 1; r < n; r++ {
					candidate := slices.Clone(want)
					slices.Reverse(candidate[l : r+1])
					if c := evaluate(candidate); c < best {
						want, best, improved = candidate, c, true
					}
				}
			}
		}
		got := tsp2optCosts(t.Context(), 0, n+1, stops, pre, initial, func(a, b int64) (float64, error) {
			if math.IsInf(weights[a][b], 1) {
				return 0, errors.New("unreachable leg")
			}
			return weights[a][b], nil
		})
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: order=%v (cost=%v), want=%v (cost=%v)", seed, got, evaluate(got), want, best)
		}
	}
}
