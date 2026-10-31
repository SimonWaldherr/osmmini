package osmmini

// Compact graphs have indices below MaxUint32. Node indices and packed turn
// states cannot equal their type's maximum value. Storing key+1 reserves zero
// for an empty slot without excluding real graph nodes (including index zero).
// The table grows only with visited nodes, never with the region's node count.
// Entries contain no pointers. A tiny initial table also keeps short queries cheap.
type indexedRouteLabel[ID ~uint32 | ~uint64] struct {
	key, previous ID
	distance      float64
}

type indexedRouteLabels[ID ~uint32 | ~uint64] struct {
	entries []indexedRouteLabel[ID]
	used    int
	costs   map[ID]float64
}

// Cost-only searches retain the existing compact distance map, without
// allocating predecessor fields that their caller will never read.
func newIndexedRouteLabels[ID ~uint32 | ~uint64](wantPath bool, capacity int) indexedRouteLabels[ID] {
	labels := indexedRouteLabels[ID]{}
	if !wantPath {
		labels.costs = make(map[ID]float64, capacity)
	}
	return labels
}

func (labels *indexedRouteLabels[ID]) distance(node ID) float64 {
	if labels.costs != nil {
		return labels.costs[node]
	}
	return labels.slot(node).distance
}

func (labels *indexedRouteLabels[ID]) slot(node ID) *indexedRouteLabel[ID] {
	hash := uint32(node)*0x9e3779b1 ^ uint32(uint64(node)>>32)*0x85ebca6b
	hash ^= hash >> 16
	mask := uint(len(labels.entries) - 1)
	// Triangular probing visits every slot of a power-of-two table and avoids
	// long primary clusters at the 7/8 load limit.
	for i, step := uint(hash)&mask, uint(1); ; i, step = (i+step)&mask, step+1 {
		entry := &labels.entries[i]
		if entry.key == 0 || entry.key == node+1 {
			return entry
		}
	}
}

func (labels *indexedRouteLabels[ID]) grow() {
	old := labels.entries
	labels.entries = make([]indexedRouteLabel[ID], max(16, len(old)*2))
	for _, entry := range old {
		if entry.key != 0 {
			*labels.slot(entry.key - 1) = entry
		}
	}
}

// relax combines the lookup and update needed for every examined edge.
func (labels *indexedRouteLabels[ID]) relax(node ID, distance float64, previous ID) bool {
	if labels.costs != nil {
		if old, exists := labels.costs[node]; exists && distance >= old {
			return false
		}
		labels.costs[node] = distance
		return true
	}
	if len(labels.entries) == 0 {
		labels.grow()
	}
	entry := labels.slot(node)
	if entry.key != 0 {
		if distance >= entry.distance {
			return false
		}
	} else {
		if labels.used >= len(labels.entries)*7/8 {
			labels.grow()
			entry = labels.slot(node)
		}
		labels.used++
	}
	*entry = indexedRouteLabel[ID]{key: node + 1, previous: previous, distance: distance}
	return true
}
