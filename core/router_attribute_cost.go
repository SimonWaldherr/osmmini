package osmmini

import "math"

const routeAttributeCacheSize = 64

type routeAttributeCost struct {
	tag       uint32 // attribute index + 1; zero is an empty cache entry
	allowed   bool
	parameter float64
}

// A query-local, direct-mapped cache keeps storage bounded independently of
// region size. Collisions simply replace an entry. No profile/vehicle settings
// or mutable cache state are shared between concurrent routing requests.
type compactRouteCosts struct {
	evaluator routeEvaluator
	mode      Objective
	slots     [routeAttributeCacheSize]routeAttributeCost
}

func newCompactRouteCosts(eval routeEvaluator) compactRouteCosts {
	mode := eval.options.Objective
	if eval.options.EmergencyMode {
		mode = ObjectiveDuration
	}
	return compactRouteCosts{evaluator: eval, mode: mode}
}

func (c *compactRouteCosts) edgeCost(d *diskGraph, attribute uint32, distance float64) (float64, bool) {
	slot := &c.slots[attribute%routeAttributeCacheSize]
	if slot.tag != attribute+1 {
		// Decode shared attributes only on a miss. The target ID is irrelevant
		// to profile restrictions and base costs, so node zero is a safe dummy.
		edge := d.compactEdge(0, attribute, 0)
		*slot = routeAttributeCost{tag: attribute + 1, allowed: c.evaluator.edgeAllowed(edge)}
		if slot.allowed {
			if c.evaluator.options.EmergencyMode {
				v := edge.SpeedKph
				if v <= 0 {
					v = 60
				}
				v *= 1.3
				if v < 10 {
					v = 10
				}
				slot.parameter = v
			} else if c.mode == ObjectiveDuration {
				slot.parameter = c.evaluator.effectiveSpeedKph(edge)
			} else if c.mode == ObjectiveEconomy {
				v := c.evaluator.effectiveSpeedKph(edge)
				slot.parameter = 1.0 + 0.7*math.Pow(v/100.0, 2)
			}
		}
	}
	if !slot.allowed {
		return 0, false
	}
	// Preserve baseEdgeCost's operation order: replacing division with a
	// precomputed multiplier would change rounding and possibly route ties.
	switch c.mode {
	case ObjectiveDuration:
		return distance * 3.6 / slot.parameter, true
	case ObjectiveEconomy:
		return (distance / 1000) * slot.parameter, true
	default:
		return distance, true
	}
}
