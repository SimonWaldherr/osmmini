package osmmini

import (
	"context"
	"slices"
)

// Without transition costs the incoming edge cannot change the best onward
// route. One distance per node is sufficient instead of one per (prev, cur)
// pair. Keep the turn-aware search whenever any transition option is active.
func needsTurnState(opt RouteOptions) bool {
	w := opt.Weights
	return w.NoLeftTurn || w.LeftTurn != 0 || w.RightTurn != 0 || w.UTurn != 0 || w.Crossing != 0 || w.TrafficLightPenalty != 0
}

func (r *Router) nodeSearch(ctx context.Context, from, to int64, opt RouteOptions, wantPath bool) ([]int64, float64, error) {
	if r.g.store != nil && r.g.store.compact && opt.TerritoryCost == nil {
		return r.indexNodeSearch(ctx, from, to, opt, wantPath)
	}
	if from == to {
		if wantPath {
			return []int64{from}, 0, nil
		}
		return nil, 0, nil
	}
	if _, ok := r.g.coord(from); !ok {
		return nil, 0, ErrRouteStartUnreachable
	}
	target, ok := r.g.coord(to)
	if !ok {
		return nil, 0, ErrRouteTargetUnreachable
	}
	if opt.Engine != EngineDijkstraNode && r.g.degree(from) == 0 {
		return nil, 0, ErrRouteStartUnreachable
	}
	eval := newRouteEvaluator(opt)
	heuristic := r.queryHeuristic(to, target, opt)
	labels := newNodeLabels[int64](min(64, r.NodeCount()), wantPath)
	labels.set(from, 0, 0)
	pq := nodeValueHeap[int64]{}
	pq.push(nodeCandidate[int64]{id: from, dist: 0, priority: heuristic.estimate(r, from)})
	for pops := 0; len(pq) > 0; pops++ {
		if ctx != nil && pops%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
		current := pq.pop()
		if distance, _ := labels.distance(current.id); current.dist != distance {
			continue
		}
		if current.id == to {
			if !wantPath {
				return nil, current.dist, nil
			}
			path := make([]int64, 0, 64)
			for id := to; ; id = labels.paths[id].previous {
				path = append(path, id)
				if id == from {
					break
				}
			}
			slices.Reverse(path)
			return path, current.dist, nil
		}
		for it := r.g.edges(current.id); it.next(); {
			edge := it.edge()
			if !eval.edgeAllowed(edge) {
				continue
			}
			cost := current.dist + eval.edgeCost(r, edge)
			if old, exists := labels.distance(edge.To); exists && cost >= old {
				continue
			}
			labels.set(edge.To, cost, current.id)
			pq.push(nodeCandidate[int64]{id: edge.To, dist: cost, priority: cost + heuristic.estimate(r, edge.To)})
		}
	}
	return nil, 0, ErrRouteNoPath
}

// Path queries update distance and predecessor together. Keeping both values in
// one map saves a second lookup and map growth. Cost-only queries retain just
// distances, and neither representation allocates region-sized arrays.
type nodeLabel[ID ~int64 | ~uint32] struct {
	distance float64
	previous ID
}

type nodeLabels[ID ~int64 | ~uint32] struct {
	distances map[ID]float64
	paths     map[ID]nodeLabel[ID]
}

func newNodeLabels[ID ~int64 | ~uint32](capacity int, wantPath bool) nodeLabels[ID] {
	if wantPath {
		return nodeLabels[ID]{paths: make(map[ID]nodeLabel[ID], capacity)}
	}
	return nodeLabels[ID]{distances: make(map[ID]float64, capacity)}
}

func (labels nodeLabels[ID]) distance(node ID) (float64, bool) {
	if labels.paths != nil {
		label, exists := labels.paths[node]
		return label.distance, exists
	}
	distance, exists := labels.distances[node]
	return distance, exists
}

func (labels nodeLabels[ID]) set(node ID, distance float64, previous ID) {
	if labels.paths != nil {
		labels.paths[node] = nodeLabel[ID]{distance: distance, previous: previous}
		return
	}
	labels.distances[node] = distance
}

type nodeCandidate[ID ~int64 | ~uint32 | ~uint64] struct {
	id             ID
	dist, priority float64
}

type nodeValueHeap[ID ~int64 | ~uint32 | ~uint64] []nodeCandidate[ID]

func (h *nodeValueHeap[ID]) push(v nodeCandidate[ID]) {
	*h = append(*h, v)
	i := len(*h) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if (*h)[parent].priority <= v.priority {
			break
		}
		(*h)[i] = (*h)[parent]
		i = parent
	}
	(*h)[i] = v
}

func (h *nodeValueHeap[ID]) pop() nodeCandidate[ID] {
	a := *h
	out, last := a[0], a[len(a)-1]
	a = a[:len(a)-1]
	if len(a) > 0 {
		i := 0
		for {
			child := 2*i + 1
			if child >= len(a) {
				break
			}
			if child+1 < len(a) && a[child+1].priority < a[child].priority {
				child++
			}
			if last.priority <= a[child].priority {
				break
			}
			a[i] = a[child]
			i = child
		}
		a[i] = last
	}
	*h = a
	return out
}
