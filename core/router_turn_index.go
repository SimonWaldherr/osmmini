package osmmini

import (
	"context"
	"encoding/binary"
	"slices"
)

func (r *Router) turnSearch(ctx context.Context, from, to int64, opt RouteOptions, wantPath bool) ([]int64, float64, error) {
	if r.g.store != nil && r.g.store.compact && opt.TerritoryCost == nil {
		return r.indexTurnSearch(ctx, from, to, opt, wantPath)
	}
	goal, cost, came, err := r.astar(ctx, from, to, opt, wantPath)
	if err != nil || !wantPath {
		return nil, cost, err
	}
	return reconstructPathTurnState(goal, came), cost, nil
}

// Compact graphs already address neighbors by uint32 index. Pack the incoming
// and current indices into one uint64 label key, avoiding OSM-ID lookups for
// coordinates, outgoing edges and heuristics throughout turn-aware routing.
// Labels grow with the visited states; short queries never allocate graph-sized
// arrays, and cost-only queries do not retain predecessors.
func (r *Router) indexTurnSearch(ctx context.Context, from, to int64, opt RouteOptions, wantPath bool) ([]int64, float64, error) {
	if from == to {
		if wantPath {
			return []int64{from}, 0, nil
		}
		return nil, 0, nil
	}
	d := r.g.store
	fromNode, toNode := d.nodeIndex(from), d.nodeIndex(to)
	if fromNode < 0 {
		return nil, 0, ErrRouteStartUnreachable
	}
	if toNode < 0 {
		return nil, 0, ErrRouteTargetUnreachable
	}
	if _, count := d.edgeRange(fromNode); count == 0 {
		return nil, 0, ErrRouteStartUnreachable
	}
	// Preserve astar's OSM ID zero sentinel even when zero is a real node.
	// When absent, -1 becomes MaxUint32, outside the valid compact node range.
	zeroNode := uint32(d.nodeIndex(0))
	start := uint64(zeroNode)<<32 | uint64(fromNode)
	var bearings routeBearingCache[uint32]
	eval := newRouteEvaluator(opt)
	costs := newCompactRouteCosts(eval)
	heuristic := r.queryHeuristic(to, d.coordAt(toNode), opt)
	labels := newIndexedRouteLabels[uint64](wantPath, min(64, d.nodes))
	labels.relax(start, 0, 0)
	pq := nodeValueHeap[uint64]{}
	pq.push(nodeCandidate[uint64]{id: start, priority: heuristic.estimateIndex(d, uint32(fromNode))})
	for pops := 1; len(pq) > 0; pops++ {
		if ctx != nil && pops%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
		current := pq.pop()
		if current.dist > labels.distance(current.id) {
			continue
		}
		previous, node := uint32(current.id>>32), uint32(current.id)
		if node == uint32(toNode) {
			if !wantPath {
				return nil, current.dist, nil
			}
			path := make([]int64, 0, 256)
			for state := current.id; ; state = labels.slot(state).previous {
				path = append(path, int64(binary.LittleEndian.Uint64(d.nodeRecord(int(uint32(state))))))
				if state == start {
					break
				}
			}
			slices.Reverse(path)
			return path, current.dist, nil
		}
		transition := routeTransition{weights: &eval.options.Weights}
		if previous != zeroNode {
			if _, count := d.edgeRange(int(node)); eval.crossingPenalty > 0 && count > 2 {
				transition.crossing = eval.crossingPenalty
			}
			if eval.turnDirections {
				p, c := d.coordAt(int(previous)), d.coordAt(int(node))
				transition.current = c
				transition.incomingBearing = bearings.bearing(previous, node, p, c)
				transition.turnDirections = true
			}
		}
		for it := d.edgesAt(int(node)); it.next(); {
			next, attribute, distance := it.compactFields()
			base, allowed := costs.edgeCost(d, attribute, distance)
			if !allowed {
				continue
			}
			penalty := transition.crossing
			if transition.turnDirections {
				outgoing := bearings.bearing(node, next, transition.current, d.coordAt(int(next)))
				penalty = transition.penaltyBearing(outgoing)
			}
			cost := current.dist + base + penalty
			state := uint64(node)<<32 | uint64(next)
			if !labels.relax(state, cost, current.id) {
				continue
			}
			pq.push(nodeCandidate[uint64]{id: state, dist: cost, priority: cost + heuristic.estimateIndex(d, next)})
		}
	}
	return nil, 0, ErrRouteNoPath
}
