package osmmini

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"testing"
)

// TestRealGraphEdgeRedundancy is an opt-in, read-only diagnostic for an already
// imported graph. It does not claim that degree-two chains can be contracted
// without retaining their geometry, road attributes and turn behavior.
func TestRealGraphEdgeRedundancy(t *testing.T) {
	path := os.Getenv("OSMMINI_REAL_GRAPH")
	if path == "" {
		t.Skip("set OSMMINI_REAL_GRAPH to an existing graph.bin")
	}
	router, err := OpenGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	type exactKey struct {
		to                              int64
		distance, speed, height, weight uint64
		highway                         string
	}
	var seen []exactKey
	var targets []int64
	var edges, duplicates, selfLoops, parallelVariants, twoOutgoing, duplicateNodes int
	byHighway := map[string]int{}
	var examples []string
	for i := 0; i < router.g.store.nodes; i++ {
		if i%4096 == 0 {
			if err := t.Context().Err(); err != nil {
				t.Fatal(err)
			}
		}
		from := int64(binary.LittleEndian.Uint64(router.g.store.nodeRecord(i)))
		seen, targets = seen[:0], targets[:0]
		_, degree := router.g.store.edgeRange(i)
		if degree == 2 {
			twoOutgoing++
		}
		nodeHasDuplicate := false
		for it := router.g.store.edgesAt(i); it.next(); {
			edge := it.edge()
			edges++
			key := exactKey{edge.To, math.Float64bits(edge.DistM), math.Float64bits(edge.SpeedKph), math.Float64bits(edge.MaxHeightM), math.Float64bits(edge.MaxWeightT), edge.HwyType}
			duplicate := false
			for _, previous := range seen {
				if previous == key {
					duplicate = true
					break
				}
			}
			if edge.To == from {
				selfLoops++
			}
			if duplicate {
				duplicates++
				nodeHasDuplicate = true
				byHighway[edge.HwyType]++
				if len(examples) < 8 {
					examples = append(examples, fmt.Sprintf("%d→%d %s %.2fm", from, edge.To, edge.HwyType, edge.DistM))
				}
			} else {
				for _, target := range targets {
					if target == edge.To {
						parallelVariants++
						break
					}
				}
				seen = append(seen, key)
				targets = append(targets, edge.To)
			}
		}
		if nodeHasDuplicate {
			duplicateNodes++
		}
	}
	var classes []string
	for highway := range byHighway {
		classes = append(classes, highway)
	}
	sort.Strings(classes)
	fmt.Printf("REAL GRAPH nodes=%d directed_edges=%d exact_duplicate_edges=%d duplicate_nodes=%d self_loops=%d parallel_different_attributes=%d nodes_outdegree2=%d compact_edge_bytes_saved=%d\n", router.NodeCount(), edges, duplicates, duplicateNodes, selfLoops, parallelVariants, twoOutgoing, duplicates*graphEdgeSize)
	for _, highway := range classes {
		fmt.Printf("REAL GRAPH duplicate_class %s=%d\n", highway, byHighway[highway])
	}
	fmt.Printf("REAL GRAPH duplicate_examples=%v\n", examples)
	d := router.g.store
	if d.chainsOffset != 0 {
		chains, vertices := binary.LittleEndian.Uint64(d.data[80:]), binary.LittleEndian.Uint64(d.data[88:])
		oldEdgeBytes := d.edgeCount * graphEdgeSize
		packedEdgeBytes := d.storedEdges*graphEdgeSize + int(chains)*graphChainSize + int(vertices)*graphChainVertexSize
		fmt.Printf("REAL GRAPH CHAIN STORAGE explicit_directed_edges=%d chain_records=%d chain_vertices=%d logical_directed_edges=%d adjacency_bytes=%d→%d saved_bytes=%d graph_bytes=%d\n", d.storedEdges, chains, vertices, d.edgeCount, oldEdgeBytes, packedEdgeBytes, oldEdgeBytes-packedEdgeBytes, len(d.data))
	}
}

type graphAuditEdgeKey struct {
	to                              int64
	distance, speed, height, weight uint64
	highway                         string
}

func auditGraphEdgeKey(edge Edge) graphAuditEdgeKey {
	return graphAuditEdgeKey{
		edge.To, math.Float64bits(edge.DistM), math.Float64bits(edge.SpeedKph),
		math.Float64bits(edge.MaxHeightM), math.Float64bits(edge.MaxWeightT), edge.HwyType,
	}
}

// TestRealGraphDedupEquivalence compares a new import against an earlier graph
// while retaining only one node's edges. It permits only the stable removal of
// bit-identical edges; coordinates, sinks and every other edge must match.
func TestRealGraphDedupEquivalence(t *testing.T) {
	originalPath, optimizedPath := os.Getenv("OSMMINI_ORIGINAL_GRAPH"), os.Getenv("OSMMINI_REAL_GRAPH")
	if originalPath == "" || optimizedPath == "" {
		t.Skip("set OSMMINI_ORIGINAL_GRAPH and OSMMINI_REAL_GRAPH to existing graph files")
	}
	original, err := OpenGraph(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	optimized, err := OpenGraph(optimizedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer optimized.Close()
	if original.NodeCount() != optimized.NodeCount() {
		t.Fatalf("node count changed: %d → %d", original.NodeCount(), optimized.NodeCount())
	}
	var expected []graphAuditEdgeKey
	removed, sinks := 0, 0
	for index := 0; index < original.g.store.nodes; index++ {
		if index%4096 == 0 {
			if err := t.Context().Err(); err != nil {
				t.Fatal(err)
			}
		}
		oldID := int64(binary.LittleEndian.Uint64(original.g.store.nodeRecord(index)))
		newID := int64(binary.LittleEndian.Uint64(optimized.g.store.nodeRecord(index)))
		oldCoord, newCoord := original.g.store.coordAt(index), optimized.g.store.coordAt(index)
		if oldID != newID || math.Float64bits(oldCoord.Lat) != math.Float64bits(newCoord.Lat) || math.Float64bits(oldCoord.Lon) != math.Float64bits(newCoord.Lon) {
			t.Fatalf("node %d changed: old %d/%+v, new %d/%+v", index, oldID, oldCoord, newID, newCoord)
		}
		expected = expected[:0]
		for it := original.g.store.edgesAt(index); it.next(); {
			key := auditGraphEdgeKey(it.edge())
			if slices.Contains(expected, key) {
				removed++
			} else {
				expected = append(expected, key)
			}
		}
		if len(expected) == 0 {
			sinks++
		}
		position := 0
		for it := optimized.g.store.edgesAt(index); it.next(); {
			got := auditGraphEdgeKey(it.edge())
			if position >= len(expected) || got != expected[position] {
				t.Fatalf("node %d edge %d is not the stable exact deduplication: %+v; expected %+v", oldID, position, got, expected)
			}
			position++
		}
		if position != len(expected) {
			t.Fatalf("node %d lost distinct edges: got %d, want %d", oldID, position, len(expected))
		}
	}
	if original.EdgeCount()-removed != optimized.EdgeCount() {
		t.Fatalf("edge count changed beyond exact duplicates: %d - %d != %d", original.EdgeCount(), removed, optimized.EdgeCount())
	}
	if len(original.streets) != len(optimized.streets) {
		t.Fatalf("street count changed: %d → %d", len(original.streets), len(optimized.streets))
	}
	for key, oldStreet := range original.streets {
		newStreet, ok := optimized.streets[key]
		if !ok || oldStreet.Display != newStreet.Display || !slices.Equal(oldStreet.NodeIDs, newStreet.NodeIDs) {
			t.Fatalf("street metadata changed for %q", key)
		}
	}
	for _, name := range []string{"Hauptstraße", "Bahnhofstraße", "Passau", "Landshut"} {
		oldID, oldOK := original.StreetNode(name)
		newID, newOK := optimized.StreetNode(name)
		if oldID != newID || oldOK != newOK {
			t.Fatalf("street lookup %q changed: %d/%t → %d/%t", name, oldID, oldOK, newID, newOK)
		}
	}
	fmt.Printf("REAL GRAPH EQUIVALENCE original=%s optimized=%s nodes=%d sinks=%d streets=%d edges=%d→%d removed_exact=%d; all coordinates/ordered edge bits/street metadata preserved\n", originalPath, optimizedPath, optimized.NodeCount(), sinks, len(optimized.streets), original.EdgeCount(), optimized.EdgeCount(), removed)
	from, to := Coord{Lat: 48.574, Lon: 13.450}, Coord{Lat: 48.549, Lon: 12.137}
	oldFrom, oldFromSnap, oldFromOK := original.NearestNode(from.Lat, from.Lon)
	oldTo, oldToSnap, oldToOK := original.NearestNode(to.Lat, to.Lon)
	newFrom, newFromSnap, newFromOK := optimized.NearestNode(from.Lat, from.Lon)
	newTo, newToSnap, newToOK := optimized.NearestNode(to.Lat, to.Lon)
	if !oldFromOK || !oldToOK || !newFromOK || !newToOK || oldFrom != newFrom || oldTo != newTo || math.Float64bits(oldFromSnap) != math.Float64bits(newFromSnap) || math.Float64bits(oldToSnap) != math.Float64bits(newToSnap) {
		t.Fatal("Hbf endpoint snapping changed")
	}
	for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration} {
		options := RouteOptions{Profile: ProfileCar, Objective: objective}
		oldRoute, err := original.RouteWithOptions(t.Context(), oldFrom, oldTo, options)
		if err != nil {
			t.Fatalf("original %s route: %v", objective, err)
		}
		newRoute, err := optimized.RouteWithOptions(t.Context(), newFrom, newTo, options)
		if err != nil {
			t.Fatalf("optimized %s route: %v", objective, err)
		}
		if !slices.Equal(oldRoute.Path, newRoute.Path) || math.Float64bits(oldRoute.DistanceM) != math.Float64bits(newRoute.DistanceM) || math.Float64bits(oldRoute.DurationS) != math.Float64bits(newRoute.DurationS) || math.Float64bits(oldRoute.Cost) != math.Float64bits(newRoute.Cost) {
			t.Fatalf("Hbf %s route path/metrics changed: old distance=%g duration=%g cost=%g, new distance=%g duration=%g cost=%g", objective, oldRoute.DistanceM, oldRoute.DurationS, oldRoute.Cost, newRoute.DistanceM, newRoute.DurationS, newRoute.Cost)
		}
		hash := sha256.New()
		var idBytes [8]byte
		for _, id := range newRoute.Path {
			binary.LittleEndian.PutUint64(idBytes[:], uint64(id))
			_, _ = hash.Write(idBytes[:])
		}
		fmt.Printf("REAL GRAPH ROUTE EQUIVALENCE %s PassauHbf→LandshutHbf from=%d/%.6fm to=%d/%.6fm points=%d path_sha256=%x distance_m=%.9f duration_s=%.9f cost=%.9f\n", objective, newFrom, newFromSnap, newTo, newToSnap, len(newRoute.Path), hash.Sum(nil), newRoute.DistanceM, newRoute.DurationS, newRoute.Cost)
	}
}

// A no-op territory policy selects the retained ID-based scalar scorer. Compare
// it against compact-field relaxation on the same real graph, including economy.
func TestRealGraphScoringEquivalence(t *testing.T) {
	path := os.Getenv("OSMMINI_REAL_GRAPH")
	if path == "" {
		t.Skip("set OSMMINI_REAL_GRAPH to a Niederbayern graph.bin")
	}
	r, err := OpenGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	from, _, fromOK := r.NearestNode(48.574, 13.450)
	to, _, toOK := r.NearestNode(48.549, 12.137)
	if !fromOK || !toOK {
		t.Fatal("station endpoints unavailable")
	}
	for _, pair := range [][2]int64{{from, to}, {to, from}} {
		for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
			opt := RouteOptions{Profile: ProfileCar, Objective: objective}
			got, err := r.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
			if err != nil {
				t.Fatal(err)
			}
			opt.TerritoryCost = &TerritoryCostPolicy{}
			want, err := r.RouteWithOptions(t.Context(), pair[0], pair[1], opt)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Path, want.Path) || math.Float64bits(got.Cost) != math.Float64bits(want.Cost) || math.Float64bits(got.DistanceM) != math.Float64bits(want.DistanceM) || math.Float64bits(got.DurationS) != math.Float64bits(want.DurationS) {
				t.Fatalf("compact scoring changed %s route %v", objective, pair)
			}
			fmt.Printf("REAL GRAPH SCORING EQUIVALENCE %s %d→%d points=%d distance_m=%.9f duration_s=%.9f cost=%.9f\n", objective, pair[0], pair[1], len(got.Path), got.DistanceM, got.DurationS, got.Cost)
		}
	}
}
