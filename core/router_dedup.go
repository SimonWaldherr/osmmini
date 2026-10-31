package osmmini

import (
	"context"
	"math"
)

type importedEdgeKey struct {
	to                              int64
	distance, speed, height, weight uint64
	highway                         string
}

func keyForImportedEdge(edge Edge) importedEdgeKey {
	return importedEdgeKey{
		to: edge.To, distance: math.Float64bits(edge.DistM), speed: math.Float64bits(edge.SpeedKph),
		height: math.Float64bits(edge.MaxHeightM), weight: math.Float64bits(edge.MaxWeightT), highway: edge.HwyType,
	}
}

// Overlapping OSM ways can emit the same directed edge more than once. Compact
// only the importer's owned adjacency, retaining the first edge in source order.
// Float bits deliberately distinguish signed zero and different NaN payloads;
// edges with different routing attributes must remain independent alternatives.
func deduplicateImportedEdges(ctx context.Context, adjacency map[int64][]Edge) (int, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	removed := 0
	visited := 0
	var seen map[importedEdgeKey]struct{}
	for id, edges := range adjacency {
		visited++
		if visited%4096 == 0 && ctx != nil {
			if err := ctx.Err(); err != nil {
				return removed, err
			}
		}
		if len(edges) < 2 {
			continue
		}
		// Typical road nodes have two edges. Linear comparison avoids a map
		// allocation there; a reusable map bounds work at unusually large nodes.
		large := len(edges) > 8
		if large {
			if seen == nil {
				seen = make(map[importedEdgeKey]struct{}, min(len(edges), 64))
			} else {
				clear(seen)
			}
		}
		kept := 0
		for i, edge := range edges {
			if (i+1)%256 == 0 && ctx != nil {
				if err := ctx.Err(); err != nil {
					return removed, err
				}
			}
			duplicate := false
			if large {
				key := keyForImportedEdge(edge)
				_, duplicate = seen[key]
				seen[key] = struct{}{}
			} else {
				for _, previous := range edges[:kept] {
					if previous.To == edge.To && keyForImportedEdge(previous) == keyForImportedEdge(edge) {
						duplicate = true
						break
					}
				}
			}
			if !duplicate {
				edges[kept] = edge
				kept++
			}
		}
		if kept < len(edges) {
			removed += len(edges) - kept
			clear(edges[kept:])
			adjacency[id] = edges[:kept:kept]
		}
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
	}
	return removed, nil
}
