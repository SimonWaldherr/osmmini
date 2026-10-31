package osmmini

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestBearingCacheMatchesExactFormulaAfterCollisions(t *testing.T) {
	var cache routeBearingCache[uint32]
	random := rand.New(rand.NewPCG(33, 71))
	coords := make([]Coord, 2048)
	for i := range coords {
		coords[i] = Coord{Lat: random.Float64()*180 - 90, Lon: random.Float64()*360 - 180}
	}
	coords[0] = Coord{Lat: 90, Lon: 180}
	coords[1] = Coord{Lat: -90, Lon: -180}
	for pass := 0; pass < 3; pass++ {
		for from, a := range coords {
			for _, to := range []int{from, (from + 1) % len(coords), (from + 23) % len(coords)} {
				b := coords[to]
				want := bearingDeg(a.Lat, a.Lon, b.Lat, b.Lon)
				got := cache.bearing(uint32(from), uint32(to), a, b)
				if math.Float64bits(got) != math.Float64bits(want) {
					t.Fatalf("%d -> %d: %g want %g", from, to, got, want)
				}
			}
		}
	}
}
