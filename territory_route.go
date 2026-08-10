package osmmini

// TerritoryEvent is one contiguous stretch of a route that lies inside a
// single territory.
type TerritoryEvent struct {
	TerritoryID string  `json:"territory_id"`
	EnteredAtKM float64 `json:"entered_at_km"`
	LeftAtKM    float64 `json:"left_at_km"`
}

// TerritoryEventsForPath walks coords (typically a RouteResult.PathCoords
// polyline) against layer in store and reports each contiguous stretch of
// territory the route passes through, with the cumulative distance in
// kilometers at which it was entered and left. Stretches outside every
// territory in the layer produce no event -- this only reports what the
// route crossed; it never turns a territory boundary into a routing
// restriction.
func TerritoryEventsForPath(store *TerritoryStore, layer string, coords []Coord) []TerritoryEvent {
	if store == nil || len(coords) == 0 {
		return nil
	}

	var events []TerritoryEvent
	var cur *TerritoryEvent
	km := 0.0

	closeCur := func() {
		if cur != nil {
			cur.LeftAtKM = km
			events = append(events, *cur)
			cur = nil
		}
	}

	for i, c := range coords {
		if i > 0 {
			km += haversineMeters(coords[i-1].Lat, coords[i-1].Lon, c.Lat, c.Lon) / 1000
		}
		t := store.FindTerritory(layer, c.Lat, c.Lon)
		switch {
		case t == nil:
			closeCur()
		case cur == nil:
			cur = &TerritoryEvent{TerritoryID: t.ID, EnteredAtKM: km}
		case t.ID != cur.TerritoryID:
			closeCur()
			cur = &TerritoryEvent{TerritoryID: t.ID, EnteredAtKM: km}
		}
	}
	closeCur()
	return events
}

// TerritoryCostPolicy applies an optional cost multiplier to routing edges
// based on which territory they fall in, relative to a "home" territory --
// e.g. to help a dispatch algorithm prefer routes that stay inside a
// vehicle's or employee's own area without making the boundary an absolute
// barrier. It only applies when a caller explicitly sets it on
// RouteOptions.TerritoryCost; by default routing ignores territories
// entirely.
type TerritoryCostPolicy struct {
	Store           *TerritoryStore
	Layer           string
	HomeTerritoryID string
	// SameFactor, NeighborFactor and ForeignFactor multiply an edge's base
	// cost when its territory is the home territory, a declared neighbor of
	// it (see TerritoryStore.Neighbors), or neither. A factor <= 0 is
	// treated as 1 (no adjustment), so a caller only needs to set the
	// factors it actually cares about.
	SameFactor     float64
	NeighborFactor float64
	ForeignFactor  float64
}

func (p *TerritoryCostPolicy) factorFor(c Coord) float64 {
	if p == nil || p.Store == nil {
		return 1
	}
	t := p.Store.FindTerritory(p.Layer, c.Lat, c.Lon)
	if t == nil {
		return 1
	}

	factor := p.ForeignFactor
	if t.ID == p.HomeTerritoryID {
		factor = p.SameFactor
	} else {
		for _, n := range p.Store.Neighbors(p.Layer, p.HomeTerritoryID) {
			if n == t.ID {
				factor = p.NeighborFactor
				break
			}
		}
	}
	if factor <= 0 {
		return 1
	}
	return factor
}
