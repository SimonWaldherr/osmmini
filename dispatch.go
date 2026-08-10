package osmmini

import (
	"fmt"
	"math"
	"strings"
)

// Point is one location to assign to a territory: a parcel destination, a
// customer address, an incident coordinate, or any other geocoded point.
type Point struct {
	ID  string
	Lat float64
	Lon float64
}

// TerritoryAssignment is the result of matching one Point against a
// territory layer.
type TerritoryAssignment struct {
	PointID     string
	TerritoryID string
	Matched     bool
	Properties  map[string]any
}

// UnassignedPolicy controls what AssignPointsWithOptions does with a point
// that falls in no territory. The zero value behaves as PolicyUnassigned.
type UnassignedPolicy string

const (
	// PolicyUnassigned leaves the point unmatched (Matched=false, no
	// TerritoryID/Properties). This is the default: a destination outside
	// every territory is never silently assigned to an arbitrary one.
	PolicyUnassigned UnassignedPolicy = "unassigned"
	// PolicyError makes AssignPointsWithOptions fail on the first
	// unmatched point.
	PolicyError UnassignedPolicy = "error"
	// PolicyNearest assigns an unmatched point to the territory whose
	// boundary is closest (approximate distance), always succeeding as
	// long as the layer has at least one territory.
	PolicyNearest UnassignedPolicy = "nearest"
	// PolicyFallbackPrefix, followed by a territory ID (e.g.
	// UnassignedPolicy("fallback:north-01")), assigns every unmatched
	// point to that fixed territory.
	PolicyFallbackPrefix = "fallback:"
)

// AssignOptions configures AssignPointsWithOptions.
type AssignOptions struct {
	Layer string
	// Include restricts which territory properties are copied into each
	// assignment's Properties. Empty means "all properties". Field
	// selection is generic -- callers name whichever properties their
	// territory data happens to carry (vehicle, depot, employee, ...);
	// nothing here is hard-coded to one business domain.
	Include      []string
	OnUnassigned UnassignedPolicy
}

// AssignPoints assigns each point to the territory in layer that contains
// it, copying every territory property into the result. Unmatched points
// are left unassigned (see PolicyUnassigned). The result is deterministic:
// the same store and the same points slice always produce the same
// assignments in the same order, so re-running a batch never silently
// reshuffles it.
func (s *TerritoryStore) AssignPoints(points []Point, layer string) []TerritoryAssignment {
	out, _ := s.AssignPointsWithOptions(points, AssignOptions{Layer: layer})
	return out
}

// AssignPointsWithOptions is AssignPoints with field selection and an
// explicit unassigned-destination policy. It reuses the layer's spatial
// index (built once at load time) and scans points in a single pass, so it
// stays efficient from a handful of records to millions.
func (s *TerritoryStore) AssignPointsWithOptions(points []Point, opt AssignOptions) ([]TerritoryAssignment, error) {
	policy := opt.OnUnassigned
	if policy == "" {
		policy = PolicyUnassigned
	}
	var fallbackID string
	if strings.HasPrefix(string(policy), PolicyFallbackPrefix) {
		fallbackID = strings.TrimPrefix(string(policy), PolicyFallbackPrefix)
		if fallbackID == "" {
			return nil, fmt.Errorf("dispatch: empty fallback territory id")
		}
	} else if policy != PolicyUnassigned && policy != PolicyError && policy != PolicyNearest {
		return nil, fmt.Errorf("dispatch: unknown unassigned policy %q", policy)
	}

	out := make([]TerritoryAssignment, len(points))
	for i, p := range points {
		t := s.FindTerritory(opt.Layer, p.Lat, p.Lon)
		if t == nil {
			switch {
			case fallbackID != "":
				t = s.FindTerritoryByID(opt.Layer, fallbackID)
				if t == nil {
					return nil, fmt.Errorf("dispatch: fallback territory %q not found in layer %q", fallbackID, opt.Layer)
				}
			case policy == PolicyNearest:
				t = s.nearestTerritory(opt.Layer, p.Lat, p.Lon)
			case policy == PolicyError:
				return nil, fmt.Errorf("dispatch: point %q matched no territory in layer %q", p.ID, opt.Layer)
			}
		}
		if t == nil {
			out[i] = TerritoryAssignment{PointID: p.ID}
			continue
		}
		out[i] = TerritoryAssignment{
			PointID:     p.ID,
			TerritoryID: t.ID,
			Matched:     true,
			Properties:  selectProperties(t.Properties, opt.Include),
		}
	}
	return out, nil
}

func (s *TerritoryStore) nearestTerritory(layer string, lat, lon float64) *Territory {
	tl := s.layer(layer)
	if tl == nil || len(tl.order) == 0 {
		return nil
	}
	c := Coord{Lat: lat, Lon: lon}
	var best *Territory
	bestDist := math.Inf(1)
	for _, t := range tl.order { // tl.order is ID-sorted, so ties keep the lowest ID
		if d := t.Geometry.DistanceToBoundaryMeters(c); d < bestDist {
			bestDist = d
			best = t
		}
	}
	return best
}

func selectProperties(props map[string]any, include []string) map[string]any {
	if len(include) == 0 {
		out := make(map[string]any, len(props))
		for k, v := range props {
			out[k] = v
		}
		return out
	}
	out := make(map[string]any, len(include))
	for _, k := range include {
		if k == "territory_id" {
			continue // already exposed as TerritoryAssignment.TerritoryID
		}
		if v, ok := props[k]; ok {
			out[k] = v
		}
	}
	return out
}

// Vehicle is a generic dispatch resource (a van, a technician, a unit, ...)
// that an AssignmentConstraint can accept or price a Shipment against.
type Vehicle struct {
	ID         string
	Properties map[string]any
}

// Shipment is a generic unit of work (a parcel, a service ticket, an
// incident, ...) to be assigned to a Vehicle.
type Shipment struct {
	ID         string
	Lat        float64
	Lon        float64
	Properties map[string]any
}

// AssignmentConstraint is the extension point for vehicle/shipment
// assignment rules: capacity, package weight/volume, time windows, working
// hours, territory membership, and so on. It deliberately does not
// prescribe how constraints combine into a route or a full VRP solve --
// today's assignment stage (AssignPoints/AssignPointsWithOptions) is
// territory lookup only.
type AssignmentConstraint interface {
	Accept(vehicle Vehicle, shipment Shipment) bool
	Cost(vehicle Vehicle, shipment Shipment) float64
}

// TerritoryConstraint is one concrete AssignmentConstraint: it accepts a
// shipment for a vehicle only when the shipment's destination falls in the
// same territory (in Layer, looked up via Store) as the value of the
// vehicle's VehicleKey property -- e.g. VehicleKey "territory_id" if
// vehicles carry their home territory directly, or any other property name
// a caller's vehicle data happens to use.
type TerritoryConstraint struct {
	Store      *TerritoryStore
	Layer      string
	VehicleKey string
}

func (c TerritoryConstraint) Accept(vehicle Vehicle, shipment Shipment) bool {
	want, ok := vehicle.Properties[c.VehicleKey]
	if !ok {
		return false
	}
	t := c.Store.FindTerritory(c.Layer, shipment.Lat, shipment.Lon)
	if t == nil {
		return false
	}
	return PropertyString(want) == t.ID
}

func (c TerritoryConstraint) Cost(vehicle Vehicle, shipment Shipment) float64 {
	if c.Accept(vehicle, shipment) {
		return 0
	}
	return math.Inf(1)
}
