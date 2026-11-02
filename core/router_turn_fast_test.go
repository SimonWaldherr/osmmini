package osmmini

import (
	"math"
	"math/rand/v2"
	"testing"
)

// destination moves from c by lengthDeg degrees of planar distance in the given
// compass bearing, using the same east/north scaling a map would use.
func turnTestStep(c Coord, bearing, lengthDeg float64) Coord {
	rad := deg2rad(bearing)
	return Coord{
		Lat: c.Lat + lengthDeg*math.Cos(rad),
		Lon: c.Lon + lengthDeg*math.Sin(rad)/math.Cos(deg2rad(c.Lat)),
	}
}

func exactTurnKind(p, c, n Coord) turnKind {
	return turnKindForBearings(
		bearingDeg(p.Lat, p.Lon, c.Lat, c.Lon),
		bearingDeg(c.Lat, c.Lon, n.Lat, n.Lon),
	)
}

// The fast classifier may decline (ok=false) but must never disagree with the
// reference implementation. The cases cover thresholds from both sides at
// every latitude band, very short and very long edges, and coordinates beyond
// the proven range.
func TestFastTurnKindMatchesBearings(t *testing.T) {
	random := rand.New(rand.NewPCG(31, 4711))
	thresholds := []float64{30, -30, 160, -160, 0, 180, 90, -90}
	var decided, total, adversarialDecided, adversarialTotal int
	var worst float64
	check := func(p, c, n Coord, adversarial bool) {
		t.Helper()
		turn := newFastTurn(p, c)
		kind, ok := turn.classify(n)
		total++
		if adversarial {
			adversarialTotal++
		}
		if !ok {
			return
		}
		decided++
		if adversarial {
			adversarialDecided++
		}
		if want := exactTurnKind(p, c, n); kind != want {
			t.Fatalf("p=%+v c=%+v n=%+v: fast=%d exact=%d", p, c, n, kind, want)
		}
	}
	for range 3_000_000 {
		lat := (random.Float64()*2 - 1) * 80 // beyond the fast range on purpose
		c := Coord{Lat: lat, Lon: (random.Float64()*2 - 1) * 180}
		in := random.Float64() * 360
		lengthIn := math.Pow(10, -7+random.Float64()*6.5) // 1e-7 .. ~0.3 degrees
		lengthOut := math.Pow(10, -7+random.Float64()*6.5)
		p := turnTestStep(c, in+180, lengthIn)
		// Uniform angles, then angles hugging each threshold.
		turnAngle := random.Float64()*360 - 180
		adversarial := random.IntN(2) == 0
		if adversarial {
			base := thresholds[random.IntN(len(thresholds))]
			delta := (random.Float64()*2 - 1) * math.Pow(10, -9+random.Float64()*9.5) // up to ~3 degrees
			turnAngle = base + delta
		}
		n := turnTestStep(c, in+turnAngle, lengthOut)
		check(p, c, n, adversarial)
		// Quantised coordinates like the stored graph.
		quant := func(v Coord) Coord {
			return Coord{Lat: math.Round(v.Lat*1e7) / 1e7, Lon: math.Round(v.Lon*1e7) / 1e7}
		}
		check(quant(p), quant(c), quant(n), adversarial)
	}
	// Measure the approximation error inside the proven range for the record.
	for range 1_000_000 {
		c := Coord{Lat: (random.Float64()*2 - 1) * turnFastMaxLatitudeDeg, Lon: (random.Float64()*2 - 1) * 180}
		in := random.Float64() * 360
		length := func() float64 { return turnFastMaxSpanDeg * (0.2 + 0.8*random.Float64()) / 1.5 }
		p := turnTestStep(c, in+180, length())
		turnAngle := random.Float64()*360 - 180
		n := turnTestStep(c, in+turnAngle, length())
		turn := newFastTurn(p, c)
		if !turn.valid {
			continue
		}
		dx, dy := (n.Lon-c.Lon)*turn.cosLat, n.Lat-c.Lat
		planar := math.Atan2(turn.inY*dx-turn.inX*dy, turn.inX*dx+turn.inY*dy) * 180 / math.Pi
		exact := angleDiffDeg(bearingDeg(c.Lat, c.Lon, n.Lat, n.Lon), bearingDeg(p.Lat, p.Lon, c.Lat, c.Lon))
		diff := math.Abs(angleDiffDeg(planar+360, exact+360))
		worst = max(worst, diff)
	}
	if worst >= turnFastMarginDeg/1.5 {
		t.Fatalf("planar approximation error %.3f degrees leaves less than a 1.5x margin below %.1f", worst, turnFastMarginDeg)
	}
	t.Logf("worst planar error %.3f deg (margin %.1f); decided %.1f%% of all, %.1f%% of threshold-hugging cases",
		worst, turnFastMarginDeg, 100*float64(decided)/float64(total), 100*float64(adversarialDecided)/float64(adversarialTotal))
	if decided*100 < total*60 {
		t.Fatalf("fast path decided only %d of %d cases", decided, total)
	}
}

func TestFastTurnKindRejectsDegenerateInput(t *testing.T) {
	c := Coord{Lat: 48, Lon: 12}
	p := Coord{Lat: 48, Lon: 11.999}
	for name, tc := range map[string]struct{ p, c, n Coord }{
		"NaN next":      {p, c, Coord{Lat: math.NaN(), Lon: 12.001}},
		"NaN previous":  {Coord{Lat: math.NaN(), Lon: 11.9}, c, Coord{Lat: 48, Lon: 12.001}},
		"Inf next":      {p, c, Coord{Lat: 48, Lon: math.Inf(1)}},
		"same point":    {c, c, Coord{Lat: 48, Lon: 12.001}},
		"zero outgoing": {p, c, c},
		"polar":         {Coord{Lat: 89.9, Lon: 0}, Coord{Lat: 89.95, Lon: 0}, Coord{Lat: 89.9, Lon: 1}},
		"long edge":     {Coord{Lat: 48, Lon: 10}, c, Coord{Lat: 48.1, Lon: 12.1}},
	} {
		turn := newFastTurn(tc.p, tc.c)
		if kind, ok := turn.classify(tc.n); ok {
			if want := exactTurnKind(tc.p, tc.c, tc.n); kind != want {
				t.Errorf("%s: fast=%d exact=%d", name, kind, want)
			}
			if name != "" && (name == "NaN next" || name == "NaN previous" || name == "Inf next" || name == "same point" || name == "zero outgoing") {
				t.Errorf("%s: fast path must decline", name)
			}
		}
	}
}

func BenchmarkTurnClassification(b *testing.B) {
	c := Coord{Lat: 48.5, Lon: 12.5}
	p := turnTestStep(c, 200, 0.002)
	next := make([]Coord, 64)
	random := rand.New(rand.NewPCG(1, 2))
	for i := range next {
		next[i] = turnTestStep(c, random.Float64()*360, 0.002)
	}
	b.Run("exact", func(b *testing.B) {
		var sink turnKind
		for i := range b.N {
			sink += exactTurnKind(p, c, next[i%len(next)])
		}
		_ = sink
	})
	b.Run("fast", func(b *testing.B) {
		var sink turnKind
		for i := range b.N {
			turn := newFastTurn(p, c) // charged per call: worst case, one state per edge
			kind, ok := turn.classify(next[i%len(next)])
			if !ok {
				kind = exactTurnKind(p, c, next[i%len(next)])
			}
			sink += kind
		}
		_ = sink
	})
}
