package osmmini

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestAngleDifferenceMatchesModuloAtTurnBoundaries(t *testing.T) {
	check := func(a, b float64) {
		t.Helper()
		want := math.Mod(a-b+540, 360) - 180
		if got := angleDiffDeg(a, b); math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("difference(%g, %g) = %.17g; want %.17g", a, b, got, want)
		}
	}
	for _, incoming := range []float64{0, 30, 90, 160, 180, 270, math.Nextafter(360, 0)} {
		for _, delta := range []float64{-180, -160, -30, 0, 30, 160, 180} {
			outgoing := math.Mod(incoming+delta+360, 360)
			for _, value := range []float64{math.Nextafter(outgoing, 0), outgoing, math.Nextafter(outgoing, 360)} {
				check(value, incoming)
			}
		}
	}
	random := rand.New(rand.NewPCG(52, 13))
	for range 100000 {
		check(random.Float64()*360, random.Float64()*360)
	}
}

func TestBearingMatchesModuloNormalization(t *testing.T) {
	check := func(a, b Coord) {
		t.Helper()
		latitude1, latitude2, longitude := deg2rad(a.Lat), deg2rad(b.Lat), deg2rad(b.Lon-a.Lon)
		y := math.Sin(longitude) * math.Cos(latitude2)
		x := math.Cos(latitude1)*math.Sin(latitude2) - math.Sin(latitude1)*math.Cos(latitude2)*math.Cos(longitude)
		want := math.Mod(rad2deg(math.Atan2(y, x))+360, 360)
		if got := bearingDeg(a.Lat, a.Lon, b.Lat, b.Lon); math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("bearing(%+v, %+v) = %.17g; want %.17g", a, b, got, want)
		}
	}
	coords := []Coord{{0, 0}, {0, math.Copysign(0, -1)}, {90, 180}, {-90, -180}, {48, 179.999}, {48, -179.999}}
	for _, a := range coords {
		for _, b := range coords {
			check(a, b)
		}
	}
	random := rand.New(rand.NewPCG(71, 33))
	for range 10000 {
		check(Coord{random.Float64()*180 - 90, random.Float64()*360 - 180}, Coord{random.Float64()*180 - 90, random.Float64()*360 - 180})
	}
}
