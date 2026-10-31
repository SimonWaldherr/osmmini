package main

import (
	"context"
	"errors"
	"math"
	"math/bits"
	"runtime"
	"sync"
	"sync/atomic"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

// Costs are scoped to one solve and its options; one-way roads stay directed.
// Matrix workers share the cache; the routing graph is read-only.
func (s *server) tripCosts(ctx context.Context, opt osmmini.RouteOptions) func(int64, int64) (float64, error) {
	type pair struct{ from, to int64 }
	type result struct {
		cost float64
		err  error
	}
	var mu sync.Mutex
	cache := make(map[pair]result)
	return func(from, to int64) (float64, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		key := pair{from, to}
		mu.Lock()
		value, ok := cache[key]
		mu.Unlock()
		if ok {
			return value.cost, value.err
		}
		cost, err := s.router.RouteCostWithOptions(ctx, from, to, opt)
		mu.Lock()
		cache[key] = result{cost, err}
		mu.Unlock()
		return cost, err
	}
}

// Each route search has its own working set, so limit concurrency even on
// servers with many CPUs.
const maxTripRouteWorkers = 4

// parallelMap preserves job order and waits for running jobs on cancellation.
func parallelMap[T any](ctx context.Context, n int, fn func(i int) T) ([]T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, nil
	}
	results := make([]T, n)
	workers := min(runtime.GOMAXPROCS(0), n, maxTripRouteWorkers)
	if workers <= 1 {
		for i := 0; i < n; i++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			results[i] = fn(i)
		}
		return results, ctx.Err()
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				results[i] = fn(i)
			}
		}()
	}
	wg.Wait()
	return results, ctx.Err()
}

func validateTSP(ctx context.Context, n int, pre []uint64, maxStops int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if n > maxStops || len(pre) != n {
		return errors.New("tsp: invalid stop or dependency count")
	}
	visited := uint64(0)
	for count := 0; count < n; count++ {
		found := false
		for i := 0; i < n; i++ {
			bit := uint64(1) << uint(i)
			if visited&bit == 0 && visited&pre[i] == pre[i] {
				visited |= bit
				found = true
				break
			}
		}
		if !found {
			return errors.New("tsp: dependency cycle or invalid prerequisite")
		}
	}
	return nil
}

func (s *server) tspExact(ctx context.Context, startNode, endNode int64, stops []stopR, pre []uint64, opt osmmini.RouteOptions) ([]int, error) {
	n := len(stops)
	if err := validateTSP(ctx, n, pre, 16); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	cost := s.tripCosts(ctx, opt)

	inf := math.MaxFloat64 / 4

	costStart := make([]float64, n)
	costEnd := make([]float64, n)
	costBetween := make([][]float64, n)
	for i := 0; i < n; i++ {
		costBetween[i] = make([]float64, n)
	}

	// Each directed leg can be searched independently.
	type leg struct{ from, to int64 }
	legs := make([]leg, 0, n*(n+1))
	legIndex := make([]int, 0, n*(n+1))
	byNodes := make(map[leg]int, n*(n+1))
	addLeg := func(from, to int64) {
		key := leg{from, to}
		index, exists := byNodes[key]
		if !exists {
			index = len(legs)
			byNodes[key] = index
			legs = append(legs, key)
		}
		legIndex = append(legIndex, index)
	}
	for i := 0; i < n; i++ {
		addLeg(startNode, stops[i].node)
		addLeg(stops[i].node, endNode)
		for j := 0; j < n; j++ {
			if i != j {
				addLeg(stops[i].node, stops[j].node)
			}
		}
	}
	legCosts, err := parallelMap(ctx, len(legs), func(k int) float64 {
		c, err := cost(legs[k].from, legs[k].to)
		if err != nil {
			return inf
		}
		return c
	})
	if err != nil {
		return nil, err
	}
	k := 0
	for i := 0; i < n; i++ {
		costStart[i] = legCosts[legIndex[k]]
		k++
		costEnd[i] = legCosts[legIndex[k]]
		k++
		for j := 0; j < n; j++ {
			if i == j {
				costBetween[i][j] = inf
				continue
			}
			costBetween[i][j] = legCosts[legIndex[k]]
			k++
		}
	}

	size := 1 << uint(n)
	all := size - 1
	dp := make([]float64, size*n)
	for i := range dp {
		dp[i] = inf
	}

	for i := 0; i < n; i++ {
		if pre[i] == 0 && costStart[i] < inf {
			m := 1 << uint(i)
			dp[(m)*n+(i)] = costStart[i]
		}
	}

	for m := 0; m < size; m++ {
		if m%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for remaining := uint(m); remaining != 0; remaining &= remaining - 1 {
			last := bits.TrailingZeros(remaining)
			if dp[(m)*n+(last)] >= inf {
				continue
			}
			for next := uint(all &^ m); next != 0; next &= next - 1 {
				nxt := bits.TrailingZeros(next)
				if (uint64(m) & pre[nxt]) != pre[nxt] {
					continue
				}
				nm := m | (1 << uint(nxt))
				c := dp[(m)*n+(last)] + costBetween[last][nxt]
				if c < dp[(nm)*n+(nxt)] {
					dp[(nm)*n+(nxt)] = c
				}
			}
		}
	}

	best := inf
	bestLast := -1
	for last := 0; last < n; last++ {
		if dp[(all)*n+(last)] >= inf || costEnd[last] >= inf {
			continue
		}
		c := dp[(all)*n+(last)] + costEnd[last]
		if c < best {
			best = c
			bestLast = last
		}
	}
	if bestLast == -1 {
		return nil, errors.New("tsp: no feasible order (unreachable legs or dependency cycle)")
	}

	order := make([]int, 0, n)
	m := all
	cur := bestLast
	for cur >= 0 {
		order = append(order, cur)
		previousMask := m &^ (1 << uint(cur))
		if previousMask == 0 {
			break
		}
		// Recover predecessors from costs to avoid a second exponential table.
		previous := -1
		for candidates := uint(previousMask); candidates != 0; candidates &= candidates - 1 {
			p := bits.TrailingZeros(candidates)
			if dp[previousMask*n+p]+costBetween[p][cur] == dp[m*n+cur] {
				previous = p
				break
			}
		}
		if previous < 0 {
			return nil, errors.New("tsp: missing predecessor for feasible order")
		}
		m, cur = previousMask, previous
	}
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	return order, nil
}

func (s *server) tspGreedy(ctx context.Context, startNode, endNode int64, stops []stopR, pre []uint64, opt osmmini.RouteOptions) ([]int, error) {
	n := len(stops)
	if err := validateTSP(ctx, n, pre, 60); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	cost := s.tripCosts(ctx, opt)
	visited := uint64(0)
	order := make([]int, 0, n)
	curNode := startNode
	eligible := make([]int, 0, n)
	eligibleNodes := make(map[int64]bool, n)

	for len(order) < n {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		eligible = eligible[:0]
		clear(eligibleNodes)
		for i := 0; i < n; i++ {
			bit := uint64(1) << uint64(i)
			if (visited&bit) == 0 && (visited&pre[i]) == pre[i] {
				// Equal-cost stops at the same node keep their original tie order.
				if !eligibleNodes[stops[i].node] {
					eligibleNodes[stops[i].node] = true
					eligible = append(eligible, i)
				}
			}
		}
		costs, err := parallelMap(ctx, len(eligible), func(k int) float64 {
			c, err := cost(curNode, stops[eligible[k]].node)
			if err != nil {
				return math.MaxFloat64
			}
			return c
		})
		if err != nil {
			return nil, err
		}
		best := math.MaxFloat64
		bestIdx := -1
		for k, i := range eligible {
			if costs[k] < best {
				best = costs[k]
				bestIdx = i
			}
		}
		if bestIdx == -1 {
			return nil, errors.New("tsp: no eligible next stop (dependency cycle or unreachable stop)")
		}
		order = append(order, bestIdx)
		visited |= uint64(1) << uint64(bestIdx)
		curNode = stops[bestIdx].node
	}

	// 2-opt improvement: try swapping segments to find shorter tours
	if n >= 4 {
		order = tsp2optCosts(ctx, startNode, endNode, stops, pre, order, cost)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return order, nil
}

// tsp2opt applies the 2-opt local search improvement to the given tour order.
// It repeatedly reverses sub-segments of the tour if doing so reduces total cost,
// while respecting dependency constraints. Route costs are cached to avoid
// redundant pathfinding computations.
func (s *server) tsp2opt(ctx context.Context, startNode, endNode int64, stops []stopR, pre []uint64, opt osmmini.RouteOptions, order []int) []int {
	return tsp2optCosts(ctx, startNode, endNode, stops, pre, order, s.tripCosts(ctx, opt))
}

func tsp2optCosts(ctx context.Context, startNode, endNode int64, stops []stopR, pre []uint64, order []int, cost func(int64, int64) (float64, error)) []int {
	n := len(order)
	if n < 4 {
		return order
	}

	cachedCost := func(from, to int64) float64 {
		c, err := cost(from, to)
		if err != nil || math.IsNaN(c) || math.IsInf(c, 0) {
			return math.Inf(1)
		}
		return c
	}

	nodes := make([]int64, n+2)
	nodes[0] = startNode
	for i, idx := range order {
		nodes[i+1] = stops[idx].node
	}
	nodes[n+1] = endNode

	tourCost := func() float64 {
		total := 0.0
		for k := 0; k < n+1; k++ {
			total += cachedCost(nodes[k], nodes[k+1])
			if math.IsInf(total, 1) {
				break
			}
		}
		return total
	}
	reverse := func(l, r int) {
		for l < r {
			order[l], order[r] = order[r], order[l]
			nodes[l+1], nodes[r+1] = nodes[r+1], nodes[l+1]
			l++
			r--
		}
	}
	bestCost := tourCost()

	const maxPasses = 64
	for pass := 0; pass < maxPasses; pass++ {
		improved := false
		for l := 0; l < n-1; l++ {
			segment := uint64(1) << uint(order[l])
			for r := l + 1; r < n; r++ {
				if ctx.Err() != nil {
					return order
				}
				// The input order is feasible. A segment containing both ends
				// of a prerequisite cannot be reversed, nor can any larger one.
				if pre[order[r]]&segment != 0 {
					break
				}
				segment |= 1 << uint(order[r])
				// Compare affected edges without subtracting unreachable costs.
				oldSegment := 0.0
				for k := l; k <= r+1; k++ {
					oldSegment += cachedCost(nodes[k], nodes[k+1])
				}
				newSegment := cachedCost(nodes[l], nodes[r+1]) + cachedCost(nodes[l+1], nodes[r+2])
				for k := l + 1; k <= r; k++ {
					newSegment += cachedCost(nodes[k+1], nodes[k])
				}
				if newSegment >= oldSegment {
					continue
				}
				reverse(l, r)
				// Summing the whole candidate only for promising moves prevents
				// rounding drift from accepting a worse tour or cycling on equal costs.
				candidateCost := tourCost()
				if candidateCost < bestCost && ctx.Err() == nil {
					bestCost = candidateCost
					improved = true
				} else {
					reverse(l, r)
				}
			}
		}
		if !improved {
			break
		}
	}
	return order
}
