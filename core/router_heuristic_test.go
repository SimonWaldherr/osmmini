package osmmini

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"math/rand/v2"
	"path/filepath"
	"testing"
)

func TestLazyGeometricEstimateMatchesEagerMaximum(t *testing.T) {
	random := rand.New(rand.NewPCG(17, 983))
	pairs := [][2]Coord{
		{{Lat: 48, Lon: 12}, {Lat: 48.1, Lon: 12.2}},
		{{Lat: 89.9, Lon: -179.9}, {Lat: 89.9, Lon: 179.9}},
		{{Lat: -89.9, Lon: 179.9}, {Lat: 89.9, Lon: -179.9}},
		{{Lat: 0, Lon: -180}, {Lat: 0, Lon: 180}},
		{{Lat: 0, Lon: 0}, {Lat: 0, Lon: 180}},
	}
	for range 1000 {
		pairs = append(pairs, [2]Coord{
			{Lat: random.Float64()*179.8 - 89.9, Lon: random.Float64()*360 - 180},
			{Lat: random.Float64()*179.8 - 89.9, Lon: random.Float64()*360 - 180}})
	}
	data := make([]byte, graphHeaderSize+graphNodeSize)
	d := &diskGraph{data: data, nodeSize: graphNodeSize, nodes: 1}
	index := &landmarkIndex{data: make([]byte, landmarkStride), steps: [8]float64{1 << 20, 1 << 20, 1 << 20, 1 << 20, 1024, 1024, 1024, 1024}}
	for _, pair := range pairs {
		binary.LittleEndian.PutUint64(data[graphHeaderSize+8:], math.Float64bits(pair[0].Lat))
		binary.LittleEndian.PutUint64(data[graphHeaderSize+16:], math.Float64bits(pair[0].Lon))
		for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration} {
			h := newRouteHeuristic(pair[1], (RouteOptions{Objective: objective}).withDefaults())
			h.landmarks = index
			h.landmarkScale = 1
			if objective == ObjectiveDuration {
				h.landmarkScale = 3.6 / h.maxSpeedKph
				h.landmarkTime = true
			}
			for _, value := range []uint16{0, 1, 2, 8, 16, 32, 64} {
				h.landmarkTarget[0] = value
				if h.landmarkTime {
					h.landmarkTarget[4] = value / 2
				}
				distance := max(0, float64(int(value)-1)*index.steps[0]) * (1 - 1e-12) * h.landmarkScale
				seconds := 0.0
				if h.landmarkTime {
					seconds = max(0, float64(int(value/2)-1)*index.steps[4]) * (1 - 1e-12)
				}
				want := max(h.estimateCoordinate(pair[0]), distance, seconds)
				got := h.estimateIndex(d, 0)
				if got != want && !(math.IsNaN(got) && math.IsNaN(want)) {
					t.Fatalf("%v %s bound %d: %.17g != %.17g", pair, objective, value, got, want)
				}
			}
		}
	}
}

func TestVerifiedGraphChecksumIsReusedForLandmarkBinding(t *testing.T) {
	r, _ := writeChainTestGraph(t, chainTestRouter(400, false))
	digest := sha256.Sum256(r.g.store.data)
	expected := hex.EncodeToString(digest[:])
	if r.g.store.checksumReady {
		t.Fatal("unverified graph already cached")
	}
	if err := r.VerifyGraphChecksum(t.Context(), expected); err != nil {
		t.Fatal(err)
	}
	if !r.g.store.checksumReady || r.g.store.checksum != digest {
		t.Fatal("verified digest was not retained")
	}
	// An existing verified digest cannot bypass a different manifest checksum.
	if err := r.VerifyGraphChecksum(t.Context(), hex.EncodeToString(make([]byte, 32))); err == nil {
		t.Fatal("wrong expected checksum accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.VerifyGraphChecksum(ctx, expected); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached cancellation: %v", err)
	}
	if err := r.VerifyGraphChecksum(nil, expected); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(t.TempDir(), "routing.alt")
	if err := r.WriteLandmarks(t.Context(), indexPath); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadLandmarks(t.Context(), indexPath); err != nil {
		t.Fatal(err)
	}
	if !r.g.store.checksumReady || r.g.store.checksum != digest {
		t.Fatal("index binding changed the verified digest")
	}
	if err := r.VerifyGraphChecksum(t.Context(), "invalid"); err == nil {
		t.Fatal("invalid checksum accepted")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.VerifyGraphChecksum(t.Context(), expected); err == nil {
		t.Fatal("closed graph accepted")
	}
}
