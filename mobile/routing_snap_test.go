package mobile

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func snapFixture(t *testing.T, connectedLongitude float64, highway string, incomingOnly bool) (*osm.Router, map[int64]osm.Coord) {
	t.Helper()
	coordinates := map[int64]osm.Coord{
		1: {Lat: 0, Lon: 0}, 2: {Lat: .0001, Lon: 0},
		3: {Lat: 0, Lon: connectedLongitude}, 4: {Lat: 0, Lon: .02},
	}
	edges := map[int64][]osm.Edge{
		1: {{To: 2, DistM: 11, SpeedKph: 20, HwyType: "service"}},
		2: {{To: 1, DistM: 11, SpeedKph: 20, HwyType: "service"}},
		3: {{To: 4, DistM: 2000, SpeedKph: 50, HwyType: highway}},
	}
	if !incomingOnly {
		edges[4] = []osm.Edge{{To: 3, DistM: 2000, SpeedKph: 50, HwyType: highway}}
	}
	router := osm.NewRouterFromGraph(coordinates, edges)
	t.Cleanup(func() { router.Close() })
	return router, coordinates
}

func snapEngine(router *osm.Router) *Engine {
	return &Engine{
		router:   router,
		Manifest: Manifest{Bounds: osm.CoordWindow{MinLat: -1, MaxLat: 1, MinLon: -1, MaxLon: 1}},
	}
}

func TestRouteNearbySnapEscapesIsolatedServiceAndRetainsActualEndpoints(t *testing.T) {
	for _, storage := range []string{"memory", "disk"} {
		t.Run(storage, func(t *testing.T) {
			router, coordinates := snapFixture(t, .001, "residential", false)
			if storage == "disk" {
				file := filepath.Join(t.TempDir(), "graph.bin")
				if err := router.WriteGraph(t.Context(), file); err != nil {
					t.Fatal(err)
				}
				var err error
				router, err = osm.OpenGraph(file)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { router.Close() })
			}
			options := osm.RouteOptions{Profile: osm.ProfileCar, Objective: osm.ObjectiveDistance}
			if _, err := router.RouteWithOptions(t.Context(), 1, 4, options); !errors.Is(err, osm.ErrRouteNoPath) {
				t.Fatalf("fixture nearest pair must be disconnected: %v", err)
			}
			got, err := snapEngine(router).Route(t.Context(), RouteRequest{
				From: coordinates[1], To: coordinates[4], Options: options,
			})
			if err != nil || !reflect.DeepEqual(got.Path, []osm.Coord{coordinates[3], coordinates[4]}) {
				t.Fatalf("nearby connected road route = %+v, %v", got, err)
			}
			if got.DistanceM != 2000 || got.DurationS != 144 || math.Abs(got.FromSnapM-111.1949) > .01 || got.ToSnapM != 0 {
				t.Fatalf("fallback lost actual metrics/snaps: %+v", got)
			}
			if len(got.Steps) < 2 || got.Steps[0].Node != 3 || got.Steps[len(got.Steps)-1].Node != 4 {
				t.Fatalf("fallback maneuvers refer to old endpoints: %+v", got.Steps)
			}
		})
	}
}

func TestRouteNearbySnapPreservesValidIsolatedLocalRouteAndNilContext(t *testing.T) {
	router, coordinates := snapFixture(t, .001, "residential", false)
	request := RouteRequest{From: coordinates[1], To: coordinates[2], Options: osm.RouteOptions{Profile: osm.ProfileCar}}
	for _, ctx := range []context.Context{t.Context(), nil} {
		got, err := snapEngine(router).Route(ctx, request)
		if err != nil || !reflect.DeepEqual(got.Path, []osm.Coord{coordinates[1], coordinates[2]}) || got.DistanceM != 11 || got.FromSnapM != 0 || got.ToSnapM != 0 {
			t.Fatalf("valid local route was resnapped: %+v, %v", got, err)
		}
	}
}

func TestRouteNearbySnapStaysWithin250MetersAndHonorsProfile(t *testing.T) {
	for _, tc := range []struct {
		name      string
		longitude float64
		highway   string
	}{
		{"connected road outside radius", .003, "residential"},
		{"nearby path forbidden to cars", .001, "path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, coordinates := snapFixture(t, tc.longitude, tc.highway, false)
			request := RouteRequest{
				From: coordinates[1], To: coordinates[4], Options: osm.RouteOptions{Profile: osm.ProfileCar},
			}
			got, err := snapEngine(router).Route(t.Context(), request)
			if !errors.Is(err, osm.ErrRouteNoPath) || len(got.Path) != 0 {
				t.Fatalf("unsafe or excessive resnap: %+v, %v", got, err)
			}
			if tc.highway == "path" {
				request.Options.Profile = osm.ProfileWalking
				if got, err := snapEngine(router).Route(t.Context(), request); err != nil || len(got.Path) != 2 {
					t.Fatalf("same path should remain available to pedestrians: %+v, %v", got, err)
				}
			}
		})
	}
}

func TestRouteNearbySnapCanReachIncomingOnlyDestination(t *testing.T) {
	router, coordinates := snapFixture(t, .001, "residential", true)
	got, err := snapEngine(router).Route(t.Context(), RouteRequest{
		From: coordinates[1], To: coordinates[4], Options: osm.RouteOptions{Profile: osm.ProfileCar},
	})
	if err != nil || !reflect.DeepEqual(got.Path, []osm.Coord{coordinates[3], coordinates[4]}) || got.ToSnapM != 0 {
		t.Fatalf("incoming-only destination was excluded: %+v, %v", got, err)
	}
}

func TestRouteNearbySnapSkipsIncomingOnlyStartBeforeConnectedCandidate(t *testing.T) {
	coordinates := map[int64]osm.Coord{
		1: {Lat: 0, Lon: 0}, 10: {Lat: .01, Lon: 0},
		2: {Lat: 0, Lon: .00009}, 3: {Lat: 0, Lon: .00018},
		4: {Lat: 0, Lon: .02},
	}
	router := osm.NewRouterFromGraph(coordinates, map[int64][]osm.Edge{
		1:  {{To: 10, DistM: 1112, SpeedKph: 20, HwyType: "service"}},
		10: {{To: 1, DistM: 1112, SpeedKph: 20, HwyType: "service"}},
		3: {
			{To: 2, DistM: 10, SpeedKph: 50, HwyType: "residential"},
			{To: 4, DistM: 2200, SpeedKph: 50, HwyType: "residential"},
		},
		4: {{To: 3, DistM: 2200, SpeedKph: 50, HwyType: "residential"}},
	})
	t.Cleanup(func() { router.Close() })
	options := osm.RouteOptions{Profile: osm.ProfileCar}
	if _, err := router.RouteWithOptions(t.Context(), 2, 4, options); !errors.Is(err, osm.ErrRouteStartUnreachable) {
		t.Fatalf("fixture's nearer incoming-only sink must not start a route: %v", err)
	}
	got, err := snapEngine(router).Route(t.Context(), RouteRequest{
		From: coordinates[1], To: coordinates[4], Options: options,
	})
	if err != nil || !reflect.DeepEqual(got.Path, []osm.Coord{coordinates[3], coordinates[4]}) || math.Abs(got.FromSnapM-20.0151) > .01 || got.ToSnapM != 0 {
		t.Fatalf("incoming-only start hid later connected candidate: %+v, %v", got, err)
	}
}

type testNearbySnapRouter struct {
	starts, destinations []osm.NodeSnap
	calls                [][2]int64
	options              []osm.RouteOptions
	nearCalls            int
	nearError            error
	route                func(context.Context, int64, int64) (osm.RouteResult, error)
	stepsPath            []int64
	stepsOptions         osm.RouteOptions
}

func (router *testNearbySnapRouter) RouteWithOptions(ctx context.Context, from, to int64, options osm.RouteOptions) (osm.RouteResult, error) {
	router.calls = append(router.calls, [2]int64{from, to})
	router.options = append(router.options, options)
	if router.route != nil {
		return router.route(ctx, from, to)
	}
	return osm.RouteResult{}, osm.ErrRouteNoPath
}

func (router *testNearbySnapRouter) NearestNodes(ctx context.Context, lat, lon float64, count int, radius float64) ([]osm.NodeSnap, error) {
	router.nearCalls++
	if count != 3 || radius != 250 {
		return nil, errors.New("unexpected geographic retry bounds")
	}
	if router.nearError != nil {
		return nil, router.nearError
	}
	if router.nearCalls == 1 {
		return router.starts, nil
	}
	return router.destinations, nil
}

func (router *testNearbySnapRouter) ManeuversForPath(path []int64, options osm.RouteOptions) []osm.Maneuver {
	router.stepsPath, router.stepsOptions = path, options
	return []osm.Maneuver{{Type: "arrive", Node: path[len(path)-1]}}
}

func TestRouteNearbySnapOrdersByTotalDistanceThenIDsAndKeepsOptions(t *testing.T) {
	router := &testNearbySnapRouter{
		starts:       []osm.NodeSnap{{ID: 2, DistanceM: 20}, {ID: 1, DistanceM: 10}, {ID: 1, DistanceM: 10}},
		destinations: []osm.NodeSnap{{ID: 5, DistanceM: 20}, {ID: 4, DistanceM: 10}, {ID: 2, DistanceM: 10}},
	}
	router.route = func(ctx context.Context, from, to int64) (osm.RouteResult, error) {
		if from == 2 && to == 5 {
			return osm.RouteResult{
				Path: []int64{2, 5}, PathCoords: []osm.Coord{{Lat: 1}, {Lat: 2}}, DistanceM: 123, DurationS: 45,
			}, nil
		}
		return osm.RouteResult{}, osm.ErrRouteNoPath
	}
	options := osm.RouteOptions{
		Engine: osm.EngineDijkstra, Profile: osm.ProfileTruck, Objective: osm.ObjectiveEconomy,
		Weights: osm.ProWeights{VehicleHeightM: 3.5, VehicleWeightT: 12, NoLeftTurn: true},
	}
	got, err := routeWithNearbySnaps(t.Context(), router, RouteRequest{Options: options},
		osm.NodeSnap{ID: 1, DistanceM: 10}, osm.NodeSnap{ID: 4, DistanceM: 10})
	wantCalls := [][2]int64{{1, 4}, {1, 2}, {1, 5}, {2, 4}, {2, 5}}
	if err != nil || !reflect.DeepEqual(router.calls, wantCalls) || got.FromSnapM != 20 || got.ToSnapM != 20 || got.DistanceM != 123 || got.DurationS != 45 {
		t.Fatalf("retry order/result = %+v %+v, %v", router.calls, got, err)
	}
	for _, observed := range router.options {
		if !reflect.DeepEqual(observed, options) {
			t.Fatalf("retry changed route options: %+v", observed)
		}
	}
	if !reflect.DeepEqual(router.stepsPath, []int64{2, 5}) || !reflect.DeepEqual(router.stepsOptions, options) {
		t.Fatalf("maneuvers changed path/options: %v %+v", router.stepsPath, router.stepsOptions)
	}
}

func TestRouteNearbySnapTriesAtMostEightAdditionalPairs(t *testing.T) {
	router := &testNearbySnapRouter{
		starts:       []osm.NodeSnap{{ID: 1}, {ID: 2}, {ID: 3}},
		destinations: []osm.NodeSnap{{ID: 4}, {ID: 5}, {ID: 6}},
	}
	_, err := routeWithNearbySnaps(t.Context(), router, RouteRequest{}, osm.NodeSnap{ID: 100}, osm.NodeSnap{ID: 200})
	if !errors.Is(err, osm.ErrRouteNoPath) || len(router.calls) != 9 || router.nearCalls != 2 {
		t.Fatalf("fallback exceeded original + eight attempts: %v, %v, lookups %d", router.calls, err, router.nearCalls)
	}
	if router.calls[len(router.calls)-1] != [2]int64{3, 5} {
		t.Fatalf("eight-attempt cap did not follow sorted IDs: %v", router.calls)
	}
}

func TestRouteNearbySnapPropagatesOtherErrorsAndCancellationImmediately(t *testing.T) {
	for _, tc := range []struct {
		name       string
		initialErr error
		retryErr   error
		nearErr    error
		wantCalls  int
		wantNear   int
	}{
		{"unsupported initial engine", osm.ErrRouteEngineUnsupported, nil, nil, 1, 0},
		{"unreachable initial start", osm.ErrRouteStartUnreachable, nil, nil, 1, 0},
		{"initial cancellation", context.Canceled, nil, nil, 1, 0},
		{"lookup cancellation", osm.ErrRouteNoPath, nil, context.Canceled, 1, 1},
		{"retry cancellation", osm.ErrRouteNoPath, context.Canceled, nil, 2, 2},
		{"retry different error", osm.ErrRouteNoPath, osm.ErrRouteTargetUnreachable, nil, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := &testNearbySnapRouter{
				starts: []osm.NodeSnap{{ID: 1}, {ID: 2}}, destinations: []osm.NodeSnap{{ID: 3}}, nearError: tc.nearErr,
			}
			router.route = func(ctx context.Context, from, to int64) (osm.RouteResult, error) {
				if len(router.calls) == 1 {
					return osm.RouteResult{}, tc.initialErr
				}
				return osm.RouteResult{}, tc.retryErr
			}
			wantError := tc.initialErr
			if tc.nearErr != nil {
				wantError = tc.nearErr
			} else if tc.retryErr != nil {
				wantError = tc.retryErr
			}
			got, err := routeWithNearbySnaps(t.Context(), router, RouteRequest{}, osm.NodeSnap{ID: 1}, osm.NodeSnap{ID: 3})
			if !errors.Is(err, wantError) || len(got.Path) != 0 || len(router.calls) != tc.wantCalls || router.nearCalls != tc.wantNear {
				t.Fatalf("error was swallowed/retried: %+v %v; calls %v, lookups %d", got, err, router.calls, router.nearCalls)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	router := &testNearbySnapRouter{}
	if _, err := routeWithNearbySnaps(ctx, router, RouteRequest{}, osm.NodeSnap{ID: 1}, osm.NodeSnap{ID: 2}); !errors.Is(err, context.Canceled) || len(router.calls) != 0 {
		t.Fatalf("pre-cancelled route started work: %v, calls %v", err, router.calls)
	}
}
