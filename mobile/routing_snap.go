package mobile

import (
	"cmp"
	"context"
	"errors"
	"slices"

	osm "simonwaldherr.de/go/osmmini/core"
)

const (
	routeSnapCandidates = 3
	routeSnapRadiusM    = 250
	routeSnapRetries    = 8
)

type nearbySnapRouter interface {
	RouteWithOptions(context.Context, int64, int64, osm.RouteOptions) (osm.RouteResult, error)
	NearestNodes(context.Context, float64, float64, int, float64) ([]osm.NodeSnap, error)
	ManeuversForPath([]int64, osm.RouteOptions) []osm.Maneuver
}

type routeSnapPair struct{ from, to osm.NodeSnap }

// A city's nearest road can be a disconnected service segment. Keep an
// already valid route unchanged, but after NoPath try nearby endpoints with
// the original routing options. Radius/count bound geographic changes and
// attempts; they do not impose a time limit on an individual graph search.
func routeWithNearbySnaps(ctx context.Context, router nearbySnapRouter, request RouteRequest, from, to osm.NodeSnap) (Route, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	finish := func(result osm.RouteResult, pair routeSnapPair) (Route, error) {
		if err := ctx.Err(); err != nil {
			return Route{}, err
		}
		return Route{
			Path: result.PathCoords, DistanceM: result.DistanceM, DurationS: result.DurationS,
			FromSnapM: pair.from.DistanceM, ToSnapM: pair.to.DistanceM,
			Steps: router.ManeuversForPath(result.Path, request.Options),
		}, nil
	}
	result, originalError := router.RouteWithOptions(ctx, from.ID, to.ID, request.Options)
	if originalError == nil {
		return finish(result, routeSnapPair{from, to})
	}
	if !errors.Is(originalError, osm.ErrRouteNoPath) {
		return Route{}, originalError
	}
	starts, err := router.NearestNodes(ctx, request.From.Lat, request.From.Lon, routeSnapCandidates, routeSnapRadiusM)
	if err != nil {
		return Route{}, err
	}
	destinations, err := router.NearestNodes(ctx, request.To.Lat, request.To.Lon, routeSnapCandidates, routeSnapRadiusM)
	if err != nil {
		return Route{}, err
	}
	pairs := make([]routeSnapPair, 0, routeSnapCandidates*routeSnapCandidates)
	seen := map[[2]int64]bool{{from.ID, to.ID}: true}
	for _, start := range starts {
		for _, destination := range destinations {
			// Do not manufacture a zero-distance route by moving both failed
			// distinct endpoints to the same nearby intersection.
			key := [2]int64{start.ID, destination.ID}
			if start.ID == destination.ID || seen[key] {
				continue
			}
			seen[key] = true
			pairs = append(pairs, routeSnapPair{start, destination})
		}
	}
	slices.SortFunc(pairs, func(a, b routeSnapPair) int {
		if order := cmp.Compare(a.from.DistanceM+a.to.DistanceM, b.from.DistanceM+b.to.DistanceM); order != 0 {
			return order
		}
		if order := cmp.Compare(a.from.ID, b.from.ID); order != 0 {
			return order
		}
		return cmp.Compare(a.to.ID, b.to.ID)
	})
	for _, pair := range pairs[:min(len(pairs), routeSnapRetries)] {
		if err := ctx.Err(); err != nil {
			return Route{}, err
		}
		result, err := router.RouteWithOptions(ctx, pair.from.ID, pair.to.ID, request.Options)
		if err == nil {
			return finish(result, pair)
		}
		// Candidate lookup includes incoming-only sinks, which are valid
		// destinations but cannot start a route. Try the next start candidate.
		if !errors.Is(err, osm.ErrRouteNoPath) && !errors.Is(err, osm.ErrRouteStartUnreachable) {
			return Route{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	return Route{}, originalError
}
