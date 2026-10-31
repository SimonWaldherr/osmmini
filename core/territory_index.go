package osmmini

import "math"

// territoryIndexTargetCells is the rough number of grid cells the index aims
// to span across a layer's bounding box, in each axis. It scales the cell
// size to the layer's own extent instead of a fixed constant, so a
// city-sized layer and a country-sized layer both get a useful grid instead
// of one or the other degenerating to a single cell or a huge cell count.
const territoryIndexTargetCells = 32

// territoryGridIndex is a bbox-bucketed grid index over a layer's
// territories: it mirrors the existing spatialIndex grid used for road-node
// lookup in router.go, adapted so a polygon (not a single point) can span
// several cells. Queries only run the exact Geometry.Contains test against
// the small set of candidates the query point's own cell holds -- built
// once per layer load, never rebuilt on query.
type territoryGridIndex struct {
	cellSize float64
	minLat   float64
	minLon   float64
	cells    map[int64][]*Territory
}

func buildTerritoryIndex(territories []*Territory) *territoryGridIndex {
	idx := &territoryGridIndex{cellSize: 0.01, cells: make(map[int64][]*Territory)}
	if len(territories) == 0 {
		return idx
	}

	minLat, minLon := math.Inf(1), math.Inf(1)
	maxLat, maxLon := math.Inf(-1), math.Inf(-1)
	for _, t := range territories {
		bb := t.Geometry.BBox()
		minLat = math.Min(minLat, bb.MinLat)
		minLon = math.Min(minLon, bb.MinLon)
		maxLat = math.Max(maxLat, bb.MaxLat)
		maxLon = math.Max(maxLon, bb.MaxLon)
	}
	idx.minLat, idx.minLon = minLat, minLon

	span := math.Max(maxLat-minLat, maxLon-minLon)
	if span > 0 && !math.IsInf(span, 0) {
		cellSize := span / territoryIndexTargetCells
		if cellSize < 1e-4 {
			cellSize = 1e-4
		}
		idx.cellSize = cellSize
	}

	for _, t := range territories {
		idx.insert(t)
	}
	return idx
}

func (idx *territoryGridIndex) cellCoords(lat, lon float64) (int32, int32) {
	ix := int32(math.Floor((lat - idx.minLat) / idx.cellSize))
	iy := int32(math.Floor((lon - idx.minLon) / idx.cellSize))
	return ix, iy
}

func (idx *territoryGridIndex) insert(t *Territory) {
	bb := t.Geometry.BBox()
	ixMin, iyMin := idx.cellCoords(bb.MinLat, bb.MinLon)
	ixMax, iyMax := idx.cellCoords(bb.MaxLat, bb.MaxLon)
	for ix := ixMin; ix <= ixMax; ix++ {
		for iy := iyMin; iy <= iyMax; iy++ {
			k := cellKey(ix, iy)
			idx.cells[k] = append(idx.cells[k], t)
		}
	}
}

// candidatesAt returns the territories whose bbox overlaps the grid cell
// containing (lat, lon). Callers still need an exact Geometry.Contains
// check -- this only narrows the scan.
func (idx *territoryGridIndex) candidatesAt(lat, lon float64) []*Territory {
	if idx == nil {
		return nil
	}
	ix, iy := idx.cellCoords(lat, lon)
	return idx.cells[cellKey(ix, iy)]
}
