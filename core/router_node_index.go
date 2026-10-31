package osmmini

import (
	"context"
	"encoding/binary"
	"math"
	"slices"
)

// Use the compact file's uint32 node indices throughout a turn-free search.
// Resolve the API endpoints once and translate the final path back to OSM IDs.
// Search labels remain lazy: short routes must not allocate region-sized arrays.
// Territory-cost callbacks retain the ID-based path, as do legacy V1 graphs.
func (r *Router) indexNodeSearch(ctx context.Context, from, to int64, opt RouteOptions, wantPath bool) ([]int64, float64, error) {
	if from == to {
		if wantPath {
			return []int64{from}, 0, nil
		}
		return nil, 0, nil
	}
	d := r.g.store
	start, target := d.nodeIndex(from), d.nodeIndex(to)
	if start < 0 {
		return nil, 0, ErrRouteStartUnreachable
	}
	if target < 0 {
		return nil, 0, ErrRouteTargetUnreachable
	}
	if _, count := d.edgeRange(start); count == 0 && opt.Engine != EngineDijkstraNode {
		return nil, 0, ErrRouteStartUnreachable
	}
	fromIndex, toIndex := uint32(start), uint32(target)
	eval := newRouteEvaluator(opt)
	costs := newCompactRouteCosts(eval)
	heuristic := r.queryHeuristic(to, d.coordAt(target), opt)
	startCoord, targetCoord := d.coordAt(start), d.coordAt(target)
	// Local routes must not eagerly walk a long road away from a nearby target.
	// Tight time potentials already prune most chain interiors. Eager batches
	// increased their labels/allocation footprint, so retain normal time search.
	walkChains := d.chainsOffset != 0 && !opt.EmergencyMode && opt.Objective != ObjectiveDuration && (math.Abs(startCoord.Lat-targetCoord.Lat) >= .01 || math.Abs(startCoord.Lon-targetCoord.Lon) >= .02)
	labels := newIndexedRouteLabels[uint32](wantPath, min(64, d.nodes))
	labels.relax(fromIndex, 0, 0)
	pq := nodeValueHeap[uint32]{}
	pq.push(nodeCandidate[uint32]{id: fromIndex, priority: heuristic.estimateIndex(d, fromIndex)})
	for pops := 0; len(pq) > 0; pops++ {
		if ctx != nil && pops%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
		current := pq.pop()
		if current.dist != labels.distance(current.id) {
			continue
		}
		if current.id == toIndex {
			if !wantPath {
				return nil, current.dist, nil
			}
			path := make([]int64, 0, 64)
			for node := toIndex; ; node = labels.slot(node).previous {
				path = append(path, int64(binary.LittleEndian.Uint64(d.nodeRecord(int(node)))))
				if node == fromIndex {
					break
				}
			}
			slices.Reverse(path)
			return path, current.dist, nil
		}
		for it := d.edgesAt(int(current.id)); it.next(); {
			next, attribute, distance := it.compactFields()
			previous, cost := current.id, current.dist
			for steps := 0; ; steps++ {
				if ctx != nil && steps > 0 && steps%16 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, 0, err
					}
				}
				edgeCost, allowed := costs.edgeCost(d, attribute, distance)
				if !allowed {
					break
				}
				cost += edgeCost
				if !labels.relax(next, cost, previous) {
					break
				}
				// Packed interiors have exactly two reciprocal neighbors and shared
				// attributes. With no transition costs, going back cannot improve g.
				// Keep the target in the queue, even if it lies inside a chain; bound
				// each batch so a high-cost branch never scans an entire long road.
				if walkChains && next != toIndex && steps < 31 && edgeCost >= 0 && !math.IsInf(edgeCost, 0) {
					if following, attr, length, ok := d.chainContinuation(next, previous); ok {
						previous, next, attribute, distance = next, following, attr, length
						continue
					}
				}
				var estimate float64
				if !heuristic.disabled {
					estimate = heuristic.estimateIndex(d, next)
				}
				pq.push(nodeCandidate[uint32]{id: next, dist: cost, priority: cost + estimate})
				break
			}
		}
	}
	return nil, 0, ErrRouteNoPath
}

func (d *diskGraph) chainContinuation(node, previous uint32) (next, attribute uint32, distance float64, ok bool) {
	chain, position, _ := d.chainNode(int(node))
	if chain == nil {
		return
	}
	start := binary.LittleEndian.Uint32(chain)
	left := d.data[d.verticesOffset+int(start+position-1)*graphChainVertexSize:]
	right := d.data[d.verticesOffset+int(start+position+1)*graphChainVertexSize:]
	if binary.LittleEndian.Uint32(left) == previous {
		next = binary.LittleEndian.Uint32(right)
		distance = math.Float64frombits(binary.LittleEndian.Uint64(d.data[d.verticesOffset+int(start+position)*graphChainVertexSize+4:]))
	} else if binary.LittleEndian.Uint32(right) == previous {
		next = binary.LittleEndian.Uint32(left)
		distance = math.Float64frombits(binary.LittleEndian.Uint64(left[4:]))
	} else {
		return
	}
	attribute = binary.LittleEndian.Uint32(chain[8:])
	ok = true
	return
}
