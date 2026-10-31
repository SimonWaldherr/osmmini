package osmmini

import (
	"math"
	"math/rand/v2"
	"testing"
)

func exerciseIndexedLabels[ID ~uint32 | ~uint64](t *testing.T, high ID) {
	t.Helper()
	labels := indexedRouteLabels[ID]{}
	reference := map[ID]indexedRouteLabel[ID]{}
	random := rand.New(rand.NewPCG(417, 993))
	keys := []ID{0, 1, high, high - 1}
	for range 20000 {
		keys = append(keys, ID(random.Uint64())%high)
	}
	for pass := 0; pass < 3; pass++ {
		for _, key := range keys {
			distance := float64(random.IntN(1000))
			previous := ID(random.Uint64()) % high
			old, exists := reference[key]
			want := !exists || distance < old.distance
			if got := labels.relax(key, distance, previous); got != want {
				t.Fatalf("relax(%d,%g)=%v want %v", key, distance, got, want)
			}
			if want {
				reference[key] = indexedRouteLabel[ID]{key: key + 1, distance: distance, previous: previous}
			}
		}
		for key, want := range reference {
			if got := *labels.slot(key); got != want {
				t.Fatalf("label %d: %+v want %+v", key, got, want)
			}
			if labels.relax(key, want.distance, 0) {
				t.Fatal("equal-cost candidate changed the predecessor")
			}
		}
		if labels.used != len(reference) {
			t.Fatalf("used=%d want %d", labels.used, len(reference))
		}
	}
}

func TestIndexedRouteLabelsGrowthAndRelaxation(t *testing.T) {
	t.Run("nodes", func(t *testing.T) { exerciseIndexedLabels(t, uint32(math.MaxUint32-1)) })
	t.Run("turns", func(t *testing.T) { exerciseIndexedLabels(t, uint64(math.MaxUint64-1)) })
}

func TestIndexedRouteLabelsPreserveInfiniteCost(t *testing.T) {
	labels := indexedRouteLabels[uint32]{}
	if !labels.relax(0, math.Inf(1), 1) || labels.relax(0, math.Inf(1), 2) {
		t.Fatal("infinite costs must retain ordinary relaxation semantics")
	}
	if !labels.relax(0, 7, 3) || labels.slot(0).previous != 3 {
		t.Fatal("finite improvement lost")
	}
}

func TestIndexedCostOnlyLabelsDoNotRetainPredecessors(t *testing.T) {
	labels := newIndexedRouteLabels[uint64](false, 16)
	for node := uint64(0); node < 1000; node++ {
		if !labels.relax(node, 100, node+10) || labels.relax(node, 100, node+11) || !labels.relax(node, 50, node+12) {
			t.Fatal("incorrect cost relaxation")
		}
		if labels.distance(node) != 50 {
			t.Fatal("cost lost")
		}
	}
	if labels.entries != nil || len(labels.costs) != 1000 {
		t.Fatal("cost-only query allocated a path table")
	}
}
