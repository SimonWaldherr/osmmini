package osmmini

// A bounded, query-local cache reuses exact bearings when several incoming
// turn states expand the same directed road. It does not retain graph-sized
// arrays or share mutable state between concurrent requests.
const routeBearingCacheSize = 256

type routeBearingEntry[ID ~int64 | ~uint32] struct {
	from, to ID
	bearing  float64
	valid    bool
}

type routeBearingCache[ID ~int64 | ~uint32] [routeBearingCacheSize]routeBearingEntry[ID]

func (c *routeBearingCache[ID]) slot(from, to ID) *routeBearingEntry[ID] {
	key := uint64(from)*0x9e3779b97f4a7c15 ^ uint64(to)*0xc2b2ae3d27d4eb4f
	key ^= key >> 32
	return &c[key&(routeBearingCacheSize-1)]
}

func (c *routeBearingCache[ID]) bearing(from, to ID, a, b Coord) float64 {
	slot := c.slot(from, to)
	if slot.valid && slot.from == from && slot.to == to {
		return slot.bearing
	}
	value := bearingDeg(a.Lat, a.Lon, b.Lat, b.Lon)
	*slot = routeBearingEntry[ID]{from: from, to: to, bearing: value, valid: true}
	return value
}
