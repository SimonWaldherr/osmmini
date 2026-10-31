package osmmini

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
)

// NodeSnap is a graph node and its great-circle distance from a query point.
type NodeSnap struct {
	ID        int64
	DistanceM float64
}

// NearestNodes returns up to limit nodes within maxMeters, ordered by distance
// and then signed node ID. It includes incoming-only nodes, which can be valid
// route destinations. It does not determine connectivity or profile access.
// The radius must be finite, positive and at most 1000 meters; limit must be
// positive and is capped at eight. File-backed graphs only visit local spatial
// cells. Routers without an index scan their in-memory coordinates instead.
func (r *Router) NearestNodes(ctx context.Context, lat, lon float64, limit int, maxMeters float64) ([]NodeSnap, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if math.IsNaN(lat) || math.IsInf(lat, 0) || math.Abs(lat) > 90 ||
		math.IsNaN(lon) || math.IsInf(lon, 0) || math.Abs(lon) > 180 {
		return nil, errors.New("invalid snap coordinate")
	}
	if limit < 1 || math.IsNaN(maxMeters) || math.IsInf(maxMeters, 0) || maxMeters <= 0 || maxMeters > 1000 {
		return nil, errors.New("invalid snap limit or radius")
	}
	limit = min(limit, 8)
	results := make([]NodeSnap, 0, limit)
	visited := 0
	visit := func(id int64, coordinate Coord) error {
		visited++
		if visited%64 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		distance := haversineMeters(lat, lon, coordinate.Lat, coordinate.Lon)
		if math.IsNaN(distance) || distance > maxMeters {
			return nil
		}
		node := NodeSnap{ID: id, DistanceM: distance}
		position, _ := slices.BinarySearchFunc(results, node, func(a, b NodeSnap) int {
			if order := cmp.Compare(a.DistanceM, b.DistanceM); order != 0 {
				return order
			}
			return cmp.Compare(a.ID, b.ID)
		})
		if position < limit {
			if len(results) < limit {
				results = append(results, NodeSnap{})
			}
			copy(results[position+1:], results[position:len(results)-1])
			results[position] = node
		}
		return nil
	}
	if r.idx == nil {
		if r.g.store != nil {
			return nil, errors.New("graph spatial index unavailable")
		}
		for id, coordinate := range r.g.coords {
			if err := visit(id, coordinate); err != nil {
				return nil, err
			}
		}
	} else {
		idx := r.idx
		window := snapWindow(lat, lon, maxMeters)
		for _, longitude := range window.longitudes {
			minX, minY := idx.cell(window.minLat, longitude[0])
			maxX, maxY := idx.cell(window.maxLat, longitude[1])
			minX, minY = max(0, minX), max(0, minY)
			maxX, maxY = min(idx.maxX, maxX), min(idx.maxY, maxY)
			for x := minX; x <= maxX; x++ {
				for y := minY; y <= maxY; y++ {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					if r.g.store != nil {
						for it := r.g.store.cellNodesIter(cellKey(x, y)); it.next(); {
							if err := visit(it.nodeID(), it.coord()); err != nil {
								return nil, err
							}
						}
					} else {
						for _, id := range idx.cells[cellKey(x, y)] {
							coordinate, _ := r.g.coord(id)
							if err := visit(id, coordinate); err != nil {
								return nil, err
							}
						}
					}
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

type nodeSnapWindow struct {
	minLat, maxLat float64
	longitudes     [][2]float64
}

// A spherical circle can straddle the date line or include a pole. Expanding
// the bounds by one float step keeps exact cell/radius boundaries conservative;
// the final haversine check determines membership in the requested radius.
func snapWindow(lat, lon, meters float64) nodeSnapWindow {
	const earthRadius = 6371000.0
	angular := meters / earthRadius
	degrees := angular * 180 / math.Pi
	minLat, maxLat := lat-degrees, lat+degrees
	window := nodeSnapWindow{
		minLat: math.Nextafter(max(-90, minLat), math.Inf(-1)),
		maxLat: math.Nextafter(min(90, maxLat), math.Inf(1)),
	}
	if minLat <= -90 || maxLat >= 90 {
		window.longitudes = [][2]float64{{-180, 180}}
		return window
	}
	delta := math.Asin(min(1, math.Sin(angular)/math.Cos(deg2rad(lat)))) * 180 / math.Pi
	minLon := math.Nextafter(lon-delta, math.Inf(-1))
	maxLon := math.Nextafter(lon+delta, math.Inf(1))
	switch {
	case minLon <= -180:
		window.longitudes = [][2]float64{{-180, maxLon}, {minLon + 360, 180}}
	case maxLon >= 180:
		window.longitudes = [][2]float64{{minLon, 180}, {-180, maxLon - 360}}
	default:
		window.longitudes = [][2]float64{{minLon, maxLon}}
	}
	return window
}
