package osmmini

import "math"

// Turn-aware searches classify the turn at every expanded edge. The exact
// classification takes two great-circle bearings (sin, cos and atan2 each),
// which dominated CPU time for profiles with turn penalties.
//
// Almost every turn is far from the 30 and 160 degree thresholds, so a local
// planar approximation decides it with a dot and cross product. Whenever the
// approximated angle lies within turnFastMarginDeg of a threshold, or the
// geometry leaves the range in which the approximation error is proven small,
// the caller falls back to the exact bearing computation. Results are therefore
// identical to turnKindForBearings(bearingDeg(...), bearingDeg(...)).
//
// Error budget (see TestFastTurnKindMatchesBearings): within turnFastMaxSpanDeg
// and turnFastMaxLatitudeDeg the planar direction differs from the initial
// great-circle bearing by at most about 0.4 degrees; the difference of two
// bearings by at most about 0.8 degrees. The margin leaves a factor of two.
const (
	turnFastMarginDeg      = 1.5
	turnFastMaxSpanDeg     = 0.25
	turnFastMaxLatitudeDeg = 70.0
	// Edges shorter than this (about one centimetre) have floating-point
	// dominated directions; leave them to the reference implementation.
	turnFastMinLengthSquared = 1e-14
)

var (
	turnCosUTurnCertain    = math.Cos((160 + turnFastMarginDeg) * math.Pi / 180)
	turnCosUTurnUncertain  = math.Cos((160 - turnFastMarginDeg) * math.Pi / 180)
	turnCosStraightCertain = math.Cos((30 - turnFastMarginDeg) * math.Pi / 180)
	turnCosStraightMargin  = math.Cos((30 + turnFastMarginDeg) * math.Pi / 180)
)

// fastTurn holds the incoming direction of one popped search state. All
// outgoing edges of that state are classified against it.
type fastTurn struct {
	valid    bool
	centre   Coord
	cosLat   float64
	inX, inY float64 // incoming direction: east (scaled by cos latitude), north
	inLenSq  float64
}

// newFastTurn prepares the incoming direction previous -> centre. The result is
// invalid, and every classification then reports "unknown", outside the range
// where the planar approximation is proven.
func newFastTurn(previous, centre Coord) fastTurn {
	// Negated comparisons also reject NaN coordinates.
	if !(math.Abs(centre.Lat) <= turnFastMaxLatitudeDeg) {
		return fastTurn{}
	}
	dLat := centre.Lat - previous.Lat
	dLon := centre.Lon - previous.Lon
	if !(math.Abs(dLat) <= turnFastMaxSpanDeg && math.Abs(dLon) <= turnFastMaxSpanDeg) {
		return fastTurn{}
	}
	cosLat := math.Cos(deg2rad(centre.Lat))
	x, y := dLon*cosLat, dLat
	lengthSq := x*x + y*y
	if !(lengthSq >= turnFastMinLengthSquared) {
		return fastTurn{}
	}
	return fastTurn{valid: true, centre: centre, cosLat: cosLat, inX: x, inY: y, inLenSq: lengthSq}
}

// classify returns the turn kind for centre -> next, or ok=false when the exact
// computation is required.
func (t *fastTurn) classify(next Coord) (kind turnKind, ok bool) {
	if !t.valid {
		return turnStraight, false
	}
	dLat := next.Lat - t.centre.Lat
	dLon := next.Lon - t.centre.Lon
	if !(math.Abs(dLat) <= turnFastMaxSpanDeg && math.Abs(dLon) <= turnFastMaxSpanDeg) {
		return turnStraight, false
	}
	x, y := dLon*t.cosLat, dLat
	lengthSq := x*x + y*y
	if !(lengthSq >= turnFastMinLengthSquared) {
		return turnStraight, false
	}
	norm := math.Sqrt(t.inLenSq * lengthSq)
	cosine := (t.inX*x + t.inY*y) / norm
	switch {
	case cosine < turnCosUTurnCertain:
		return turnUTurn, true
	case cosine > turnCosStraightCertain:
		return turnStraight, true
	case cosine <= turnCosUTurnUncertain || cosine >= turnCosStraightMargin:
		return turnStraight, false
	}
	// Bearings grow clockwise: a positive clockwise rotation is a right turn.
	if t.inY*x-t.inX*y > 0 {
		return turnRight, true
	}
	return turnLeft, true
}
