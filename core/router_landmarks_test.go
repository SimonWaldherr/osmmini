package osmmini

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func landmarkTestGraph(t *testing.T) (*Router, string) {
	t.Helper()
	coords := map[int64]Coord{}
	adjacency := map[int64][]Edge{}
	random := rand.New(rand.NewPCG(81, 73))
	for y := 0; y < 6; y++ {
		for x := 0; x < 6; x++ {
			id := int64(y*6 + x + 1)
			coords[id] = Coord{Lat: 48 + float64(y)*.00001, Lon: 12 + float64(x)*.00001}
			for _, delta := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+delta[0], y+delta[1]
				if nx < 0 || nx >= 6 || ny < 0 || ny >= 6 || random.IntN(5) == 0 {
					continue
				}
				adjacency[id] = append(adjacency[id], Edge{To: int64(ny*6 + nx + 1), DistM: 10 + random.Float64()*40, SpeedKph: 20 + float64(random.IntN(100)), HwyType: "residential"})
			}
		}
	}
	// A disconnected one-way component exercises infinite forward/reverse values.
	coords[100] = Coord{Lat: 48, Lon: 12}
	coords[101] = coords[100]
	adjacency[100] = []Edge{{To: 101, DistM: 10, SpeedKph: 50}}
	r, _ := writeChainTestGraph(t, NewRouterFromGraph(coords, adjacency))
	path := filepath.Join(t.TempDir(), "routing.alt")
	if err := r.WriteLandmarks(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadLandmarks(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	return r, path
}

func TestLandmarkBoundsAndRoutesMatchDirectedDijkstra(t *testing.T) {
	r, _ := landmarkTestGraph(t)
	options := []RouteOptions{
		{Objective: ObjectiveDistance}, {Profile: ProfileCar}, {Profile: ProfileWalking}, {Profile: ProfileCycling},
		{Profile: ProfileTruck}, {Profile: ProfileTravel}, {Objective: ObjectiveEconomy},
		{Profile: ProfileCar, Weights: ProWeights{MaxSpeedKph: 25, LeftTurn: 4, RightTurn: 1, UTurn: 20, Crossing: 3}},
	}
	for _, opt := range options {
		opt = opt.withDefaults()
		for _, target := range []int64{1, 6, 15, 31, 36, 100, 101} {
			coord, _ := r.Coord(target)
			h := r.queryHeuristic(target, coord, opt)
			for _, from := range []int64{1, 8, 21, 32, 36, 100, 101} {
				expectedOpt := opt
				expectedOpt.Engine = EngineDijkstra
				want, wantErr := r.RouteWithOptions(t.Context(), from, target, expectedOpt)
				got, err := r.RouteWithOptions(t.Context(), from, target, opt)
				if !errors.Is(err, wantErr) || (err == nil && math.Abs(got.Cost-want.Cost) > 1e-9) {
					t.Fatalf("%+v %d→%d: cost %g/%g, error %v/%v", opt, from, target, got.Cost, want.Cost, err, wantErr)
				}
				if err == nil {
					bound := h.estimate(r, from)
					if bound < 0 || bound > want.Cost+1e-9 {
						t.Fatalf("inadmissible %d→%d: %g > %g", from, target, bound, want.Cost)
					}
					cost, err := r.RouteCostWithOptions(t.Context(), from, target, opt)
					if err != nil || cost != got.Cost {
						t.Fatalf("cost-only differs: %g, %v", cost, err)
					}
				}
			}
		}
	}
}

func TestLandmarkQuantizedIntervalsContainExactDistances(t *testing.T) {
	r, path := landmarkTestGraph(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reverse, _, err := buildLandmarkReverse(t.Context(), r.g.store)
	if err != nil {
		t.Fatal(err)
	}
	for landmark := 0; landmark < landmarkCount; landmark++ {
		seed := binary.LittleEndian.Uint32(data[24+landmark*4:])
		for direction, graph := range []*landmarkReverse{nil, reverse} {
			distances, err := landmarkDistances(t.Context(), r.g.store, graph, seed, landmark >= landmarkCount/2, nil)
			if err != nil {
				t.Fatal(err)
			}
			table := landmark*2 + direction
			step := r.landmarks.steps[table]
			for node, distance := range distances {
				value := binary.LittleEndian.Uint16(r.landmarks.data[node*landmarkStride+table*2:])
				if math.IsInf(distance, 1) {
					if value != landmarkInfinity {
						t.Fatal("unreachable node not marked")
					}
					continue
				}
				lower := float64(value) * step
				if value == landmarkInfinity || lower > distance || distance >= lower+step {
					t.Fatalf("distance %g outside interval [%g,%g)", distance, lower, lower+step)
				}
			}
		}
	}
}

func TestLandmarksRejectCorruptionAndWrongGraphWithoutReplacingIndex(t *testing.T) {
	r, path := landmarkTestGraph(t)
	original := r.landmarks
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{0, 8, 16, 24, 40, 104, 136, 168, landmarkHeaderSize, len(data) - 1} {
		corrupt := append([]byte(nil), data...)
		corrupt[offset] ^= 0x80
		file := filepath.Join(t.TempDir(), "corrupt.alt")
		if err := os.WriteFile(file, corrupt, 0600); err != nil {
			t.Fatal(err)
		}
		if err := r.LoadLandmarks(t.Context(), file); err == nil {
			t.Fatalf("accepted corruption at %d", offset)
		}
		if r.landmarks != original {
			t.Fatal("failed load replaced live index")
		}
	}
	corrupt := append([]byte(nil), data...)
	step := math.Float64frombits(binary.LittleEndian.Uint64(corrupt[40:]))
	binary.LittleEndian.PutUint64(corrupt[40:], math.Float64bits(step*2))
	file := filepath.Join(t.TempDir(), "changed-step.alt")
	if err := os.WriteFile(file, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadLandmarks(t.Context(), file); err == nil {
		t.Fatal("accepted valid-looking but altered quantization step")
	}
	other, _ := writeChainTestGraph(t, chainTestRouter(38, false))
	if err := other.LoadLandmarks(t.Context(), path); err == nil {
		t.Fatal("accepted landmarks for different graph")
	}
	if err := r.WriteLandmarks(t.Context(), path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrote existing file: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("existing index changed")
	}
}

type landmarkCancelContext struct {
	context.Context
	checks int
	after  int
}

func (c *landmarkCancelContext) Err() error {
	c.checks++
	if c.checks >= c.after {
		return context.Canceled
	}
	return nil
}

func TestLandmarkCancellationAndConcurrentQueries(t *testing.T) {
	r, path := landmarkTestGraph(t)
	ctx := &landmarkCancelContext{Context: context.Background(), after: 4}
	cancelled := filepath.Join(t.TempDir(), "cancelled.alt")
	if err := r.WriteLandmarks(ctx, cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(cancelled); !os.IsNotExist(err) {
		t.Fatal("partial index remains")
	}
	original := r.landmarks
	ctx = &landmarkCancelContext{Context: context.Background(), after: 2}
	if err := r.LoadLandmarks(ctx, path); !errors.Is(err, context.Canceled) || r.landmarks != original {
		t.Fatalf("cancelled load: %v", err)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 8 {
				_, err := r.RouteWithOptions(t.Context(), 1, 36, RouteOptions{Profile: ProfileCar})
				if err != nil {
					t.Error(err)
				}
			}
		})
	}
	group.Wait()
}

func TestLandmarkSpecialCostsUseConservativeFallback(t *testing.T) {
	r, _ := landmarkTestGraph(t)
	target, _ := r.Coord(36)
	for _, opt := range []RouteOptions{{EmergencyMode: true}, {TerritoryCost: &TerritoryCostPolicy{}}, {Engine: EngineDijkstra}, {Engine: EngineDijkstraNode}} {
		h := r.queryHeuristic(36, target, opt.withDefaults())
		if h.landmarks != nil || h.estimate(r, 1) != 0 {
			t.Fatalf("unsafe special-cost heuristic: %+v", opt)
		}
	}
}

func BenchmarkLandmarkRealRouting(b *testing.B) {
	graph, index := os.Getenv("OSMMINI_REAL_GRAPH"), os.Getenv("OSMMINI_LANDMARKS")
	if graph == "" || index == "" {
		b.Skip("set OSMMINI_REAL_GRAPH and OSMMINI_LANDMARKS")
	}
	r, err := OpenGraph(graph)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	if err := r.LoadLandmarks(b.Context(), index); err != nil {
		b.Fatal(err)
	}
	landmarks := r.landmarks
	defer func() { r.landmarks = landmarks }()
	from, _, _ := r.NearestNode(48.574, 13.450)
	to, _, _ := r.NearestNode(48.549, 12.137)
	for _, mode := range []string{"baseline", "alt"} {
		b.Run(mode, func(b *testing.B) {
			r.landmarks = nil
			if mode == "alt" {
				r.landmarks = landmarks
			}
			for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
				b.Run(string(objective), func(b *testing.B) {
					opt := RouteOptions{Profile: ProfileCar, Objective: objective}
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						route, err := r.RouteWithOptions(b.Context(), from, to, opt)
						if err != nil {
							b.Fatal(err)
						}
						graphStoreBenchmarkSum = int64(route.Cost)
					}
				})
			}
		})
	}
	r.landmarks = landmarks
}

func TestLandmarkLargeIntervalsAndChainEndpoints(t *testing.T) {
	memory := chainTestRouter(30, false)
	for node, edges := range memory.g.adj {
		for i := range edges {
			edges[i].DistM *= 100000
		}
		memory.g.adj[node] = edges
	}
	r, _ := writeChainTestGraph(t, memory)
	path := filepath.Join(t.TempDir(), "large.alt")
	if err := r.WriteLandmarks(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadLandmarks(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if r.landmarks.steps[0] <= 1 {
		t.Fatal("large distances did not increase interval width")
	}
	for _, pair := range [][2]int64{{1, 30}, {4, 27}, {29, 7}, {15, 15}} {
		for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
			opt := RouteOptions{Profile: ProfileCar, Objective: objective}
			got, err := r.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
			if err != nil {
				t.Fatal(err)
			}
			opt.Engine = EngineDijkstra
			want, err := r.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
			if err != nil || got.Cost != want.Cost {
				t.Fatalf("chain interior/endpoints differ: %+v, %v", pair, err)
			}
		}
	}
}

func TestLandmarkRealRoutingMatchesDijkstra(t *testing.T) {
	graph, index := os.Getenv("OSMMINI_REAL_GRAPH"), os.Getenv("OSMMINI_LANDMARKS")
	if graph == "" || index == "" {
		t.Skip("set OSMMINI_REAL_GRAPH and OSMMINI_LANDMARKS")
	}
	r, err := OpenGraph(graph)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.LoadLandmarks(t.Context(), index); err != nil {
		t.Fatal(err)
	}
	var nodes []int64
	for _, city := range []Coord{{Lat: 48.574, Lon: 13.450}, {Lat: 48.549, Lon: 12.137}, {Lat: 48.832, Lon: 12.960}, {Lat: 48.881, Lon: 12.573}} {
		node, _, ok := r.NearestNode(city.Lat, city.Lon)
		if !ok {
			t.Fatal("missing city")
		}
		nodes = append(nodes, node)
	}
	options := []RouteOptions{
		{Profile: ProfileCar, Objective: ObjectiveDistance}, {Profile: ProfileCar, Objective: ObjectiveDuration}, {Profile: ProfileCar, Objective: ObjectiveEconomy},
		{Profile: ProfileTruck}, {Profile: ProfileCar, Weights: ProWeights{LeftTurn: 4, RightTurn: 1, UTurn: 20, Crossing: 3}},
		{Profile: ProfileCar, Weights: ProWeights{MaxSpeedKph: 20}}, {Profile: ProfileWalking}, {EmergencyMode: true},
	}
	checked := 0
	for _, pair := range [][2]int{{0, 1}, {1, 0}, {2, 3}, {3, 2}, {0, 2}, {2, 0}} {
		for _, opt := range options {
			from, to := nodes[pair[0]], nodes[pair[1]]
			got, err := r.RouteWithOptions(t.Context(), from, to, opt)
			opt.Engine = EngineDijkstra
			want, wantErr := r.RouteWithOptions(t.Context(), from, to, opt)
			if !errors.Is(err, wantErr) || (err == nil && math.Abs(got.Cost-want.Cost) > 1e-10*max(1, want.Cost)) {
				t.Fatalf("real route %v/%+v cost %.17g vs %.17g, error %v/%v", pair, opt, got.Cost, want.Cost, err, wantErr)
			}
			if err == nil {
				checked++
			}
		}
	}
	t.Logf("%d real routes match directed Dijkstra including profiles, penalties and emergency costs", checked)
}

func BenchmarkLandmarkRealShortRouting(b *testing.B) {
	graph, index := os.Getenv("OSMMINI_REAL_GRAPH"), os.Getenv("OSMMINI_LANDMARKS")
	if graph == "" || index == "" {
		b.Skip("set OSMMINI_REAL_GRAPH and OSMMINI_LANDMARKS")
	}
	r, err := OpenGraph(graph)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	if err := r.LoadLandmarks(b.Context(), index); err != nil {
		b.Fatal(err)
	}
	landmarks := r.landmarks
	defer func() { r.landmarks = landmarks }()
	from, _, _ := r.NearestNode(48.574, 13.450)
	to, _, _ := r.NearestNode(48.5745, 13.4505)
	for _, mode := range []string{"baseline", "alt"} {
		b.Run(mode, func(b *testing.B) {
			r.landmarks = nil
			if mode == "alt" {
				r.landmarks = landmarks
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				route, err := r.RouteWithOptions(b.Context(), from, to, RouteOptions{Profile: ProfileCar})
				if err != nil {
					b.Fatal(err)
				}
				graphStoreBenchmarkSum = int64(route.Cost)
			}
		})
	}
}

func BenchmarkLandmarkRealOpen(b *testing.B) {
	graph, index := os.Getenv("OSMMINI_REAL_GRAPH"), os.Getenv("OSMMINI_LANDMARKS")
	if graph == "" || index == "" {
		b.Skip("set OSMMINI_REAL_GRAPH and OSMMINI_LANDMARKS")
	}
	for _, mode := range []string{"baseline", "alt"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r, err := OpenGraph(graph)
				if err != nil {
					b.Fatal(err)
				}
				if mode == "alt" {
					if err := r.LoadLandmarks(b.Context(), index); err != nil {
						r.Close()
						b.Fatal(err)
					}
				}
				if err := r.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func FuzzLandmarkRoutesMatchDijkstra(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 17, 21, 25, 32, 40, 81, 250})
	f.Add([]byte{0, 0, 255, 3, 2, 89, 12, 15, 61, 11, 3, 4, 0, 254, 255, 128, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 3 || len(data) > 256 {
			t.Skip()
		}
		count := int(data[0]%10) + 2
		coords := map[int64]Coord{}
		edges := map[int64][]Edge{}
		for node := 1; node <= count; node++ {
			coords[int64(node)] = Coord{Lat: 48, Lon: 12}
		}
		for i := 1; i+2 < len(data); i += 3 {
			from, to := int64(data[i]%byte(count))+1, int64(data[i+1]%byte(count))+1
			if from == to {
				continue
			}
			edges[from] = append(edges[from], Edge{To: to, DistM: float64(data[i+2])*1000 + 0.125, SpeedKph: float64(data[i]%200) + 1, HwyType: "residential"})
		}
		r, _ := writeChainTestGraph(t, NewRouterFromGraph(coords, edges))
		path := filepath.Join(t.TempDir(), "fuzz.alt")
		if err := r.WriteLandmarks(t.Context(), path); err != nil {
			t.Fatal(err)
		}
		if err := r.LoadLandmarks(t.Context(), path); err != nil {
			t.Fatal(err)
		}
		for _, pair := range [][2]int64{{1, int64(count)}, {int64(count), 1}, {int64(count/2 + 1), int64(count)}} {
			for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration} {
				opt := RouteOptions{Objective: objective, Weights: ProWeights{MaxSpeedKph: 130}}
				got, err := r.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
				opt.Engine = EngineDijkstra
				want, wantErr := r.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
				if !errors.Is(err, wantErr) || (err == nil && math.Abs(got.Cost-want.Cost) > 1e-10*max(1, want.Cost)) {
					t.Fatalf("%v %s: cost %.17g/%.17g, errors %v/%v", pair, objective, got.Cost, want.Cost, err, wantErr)
				}
			}
		}
	})
}

func TestLandmarkRandomDirectedGraphsMatchDijkstra(t *testing.T) {
	comparisons := 0
	for seed := uint64(0); seed < 64; seed++ {
		random := rand.New(rand.NewPCG(seed, 7919))
		count := random.IntN(24) + 2
		coords := map[int64]Coord{}
		edges := map[int64][]Edge{}
		for node := int64(1); node <= int64(count); node++ {
			coords[node] = Coord{Lat: 48, Lon: 12}
			for range random.IntN(6) {
				target := int64(random.IntN(count) + 1)
				if node == target {
					continue
				}
				edges[node] = append(edges[node], Edge{To: target, DistM: random.Float64()*10000 + .125, SpeedKph: float64(random.IntN(200) + 1), HwyType: "residential"})
			}
		}
		func() {
			// Release each mapping before preparing the next graph, including when
			// the test is run with the race detector on a small descriptor budget.
			path := filepath.Join(t.TempDir(), "random.bin")
			if err := NewRouterFromGraph(coords, edges).WriteGraph(t.Context(), path); err != nil {
				t.Fatal(err)
			}
			r, err := OpenGraph(path)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			index := filepath.Join(t.TempDir(), "random.alt")
			if err := r.WriteLandmarks(t.Context(), index); err != nil {
				t.Fatal(err)
			}
			if err := r.LoadLandmarks(t.Context(), index); err != nil {
				t.Fatal(err)
			}
			for range 20 {
				from, to := int64(random.IntN(count)+1), int64(random.IntN(count)+1)
				for _, opt := range []RouteOptions{{Objective: ObjectiveDistance}, {Objective: ObjectiveDuration}, {Profile: ProfileCar, Weights: ProWeights{MaxSpeedKph: 5, LeftTurn: 7, RightTurn: 3, Crossing: 2}}} {
					got, err := r.RouteWithOptions(t.Context(), from, to, opt)
					opt.Engine = EngineDijkstra
					want, wantErr := r.RouteWithOptions(t.Context(), from, to, opt)
					if !errors.Is(err, wantErr) || (err == nil && math.Abs(got.Cost-want.Cost) > 1e-10*max(1, want.Cost)) {
						t.Fatalf("seed %d %d→%d %+v: cost %.17g/%.17g errors %v/%v", seed, from, to, opt, got.Cost, want.Cost, err, wantErr)
					}
					comparisons++
				}
			}
		}()
	}
	t.Logf("%d ALT/Dijkstra comparisons across 64 directed random graphs", comparisons)
}
