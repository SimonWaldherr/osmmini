package osmmini

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPackedHighwayReferencesPreserveSignedIDsAndIndependentStorage(t *testing.T) {
	for _, ids := range [][]int64{
		{1, 2, 10000000000, 3, 3, -7},
		{math.MinInt64, math.MaxInt64, 0, math.MinInt64, -1, math.MaxInt64},
	} {
		packed := packHighwayRefs(ids)
		original := append([]byte(nil), packed...)
		_ = packHighwayRefs([]int64{20, 21, 22})
		if !reflect.DeepEqual(packed, original) {
			t.Fatal("packing another way overwrote retained references")
		}
		var decoded []int64
		var id int64
		for len(packed) > 0 {
			delta, n := binary.Varint(packed)
			if n <= 0 {
				t.Fatal("invalid retained reference encoding")
			}
			id += delta
			decoded = append(decoded, id)
			packed = packed[n:]
		}
		if !reflect.DeepEqual(ids, decoded) {
			t.Fatalf("references changed: %v, want %v", decoded, ids)
		}
	}
}

func roadImportFixture(t *testing.T) string {
	t.Helper()
	table := []string{"", "highway", "residential", "name", "Teststraße", "oneway", "yes", "addr:street", "Nebenstraße", "addr:housenumber", "9", "access", "no"}
	// Put ways before nodes: the importer must not rely on PBF entity order.
	var ways []byte
	ways = append(ways, testPBFPrimitiveGroup(3, testPBFWay(1, []uint64{1, 3, 5}, []uint64{2, 4, 6}, []int64{10, 10, 10, 60}))...)
	ways = append(ways, testPBFPrimitiveGroup(3, testPBFWay(2, []uint64{1, 11}, []uint64{2, 12}, []int64{100, 10}))...)
	data := testPBFData(table, ways)
	var nodes []byte
	for _, id := range []int64{10, 20, 30} {
		nodes = append(nodes, testPBFPrimitiveGroup(1, testPBFNode(id, nil, nil, 480000000+id*1000, 120000000))...)
	}
	// Dense non-road nodes exceed the graph limit on their own. An unrelated
	// address must still be collected by server callers that request addresses.
	ids, lats, lons := make([]int64, 256), make([]int64, 256), make([]int64, 256)
	for i := range ids {
		ids[i], lats[i], lons[i] = 1, 1, 1
	}
	ids[0], lats[0], lons[0] = 1000, 480000000, 120000000
	nodes = append(nodes, testPBFPrimitiveGroup(2, testPBFDenseNodes(ids, lats, lons, nil))...)
	nodes = append(nodes, testPBFPrimitiveGroup(1, testPBFNode(4000, []uint64{7, 9}, []uint64{8, 10}, 480000000, 120000000))...)
	data = append(data, testPBFData(table, nodes)...)
	path := filepath.Join(t.TempDir(), "roads.osm.pbf")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRoadImportIgnoresUnrelatedNodesAndKeepsAddresses(t *testing.T) {
	path := roadImportFixture(t)
	for _, skipAddresses := range []bool{false, true} {
		router, addresses, err := BuildRouterWithAddressesOptions(path, BuildOptions{Context: t.Context(), MaxNodes: 4, SkipAddresses: skipAddresses})
		if err != nil {
			t.Fatal(err)
		}
		if router.NodeCount() != 3 || router.EdgeCount() != 2 {
			t.Fatalf("graph = %d nodes / %d edges, want 3 / 2", router.NodeCount(), router.EdgeCount())
		}
		coords, _ := router.ExportGraph()
		if _, invented := coords[90]; invented {
			t.Fatal("missing road coordinate was replaced with a zero coordinate")
		}
		if _, sink := coords[30]; !sink {
			t.Fatal("pruning lost the sink of the one-way road")
		}
		wantAddresses := 1
		if skipAddresses {
			wantAddresses = 0
		}
		if len(addresses) != wantAddresses || !skipAddresses && addresses[0].ID != 4000 {
			t.Fatalf("addresses = %+v (skip=%v)", addresses, skipAddresses)
		}
		route, err := router.RouteWithOptions(t.Context(), 10, 30, RouteOptions{Engine: EngineDijkstra})
		if err != nil || len(route.Path) != 3 || route.DistanceM <= 0 {
			t.Fatalf("route through retained road: %+v, %v", route, err)
		}
		if _, err := router.RouteWithOptions(t.Context(), 30, 10, RouteOptions{Engine: EngineDijkstra}); err == nil {
			t.Fatal("one-way direction was lost")
		}
	}
}

func TestRoadImportStillEnforcesItsNodeLimit(t *testing.T) {
	_, _, err := BuildRouterWithAddressesOptions(roadImportFixture(t), BuildOptions{Context: t.Context(), MaxNodes: 2, SkipAddresses: true})
	if err == nil || !strings.Contains(err.Error(), "exceeds 2 road nodes") {
		t.Fatalf("road-node bound was not enforced: %v", err)
	}
}

func TestRoadImportWindowCountsOnlyRetainedCoordinates(t *testing.T) {
	window := CoordWindow{MinLat: 48.0015, MaxLat: 48.0035, MinLon: 11.99, MaxLon: 12.01}
	router, addresses, err := BuildRouterWithAddressesOptions(roadImportFixture(t), BuildOptions{Context: t.Context(), MaxNodes: 2, Window: &window})
	if err != nil {
		t.Fatal(err)
	}
	if router.NodeCount() != 2 || router.EdgeCount() != 1 || len(addresses) != 0 {
		t.Fatalf("window = %d nodes / %d edges / %d addresses, want 2 / 1 / 0", router.NodeCount(), router.EdgeCount(), len(addresses))
	}
	if _, err := router.RouteWithOptions(t.Context(), 20, 30, RouteOptions{Engine: EngineDijkstra}); err != nil {
		t.Fatalf("windowed route: %v", err)
	}
}

func TestRoadImportKeepsExactIndependentAdjacencySpans(t *testing.T) {
	table := []string{"", "highway", "residential", "name", "Main Street", "Side Street", "oneway", "-1", "maxspeed", "80", "maxheight", "2.5", "maxweight", "7.5"}
	var group []byte
	for _, id := range []int64{10, 20, 30, 40, 50, 60, 70} {
		lat := 480000000 + id*1000
		if id == 60 {
			lat = 480010000 // coincides with 10; do not count a zero-length edge
		}
		group = append(group, testPBFPrimitiveGroup(1, testPBFNode(id, nil, nil, lat, 120000000))...)
	}
	group = append(group, testPBFPrimitiveGroup(3, testPBFWay(1, []uint64{1, 3}, []uint64{2, 4}, []int64{10, 10, 10}))...)
	group = append(group, testPBFPrimitiveGroup(3, testPBFWay(2, []uint64{1, 3, 8, 10, 12}, []uint64{2, 5, 9, 11, 13}, []int64{20, 20}))...)
	group = append(group, testPBFPrimitiveGroup(3, testPBFWay(3, []uint64{1, 6}, []uint64{2, 7}, []int64{40, 10}))...)
	group = append(group, testPBFPrimitiveGroup(3, testPBFWay(4, []uint64{1}, []uint64{2}, []int64{10, 50}))...)
	group = append(group, testPBFPrimitiveGroup(3, testPBFWay(5, []uint64{1}, []uint64{2}, []int64{70, 10}))...)
	path := filepath.Join(t.TempDir(), "junction.osm.pbf")
	if err := os.WriteFile(path, testPBFData(table, group), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _, err := BuildRouterWithAddressesOptions(path, BuildOptions{Context: t.Context(), SkipAddresses: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.NodeCount() != 5 || r.EdgeCount() != 7 {
		t.Fatalf("graph = %d nodes / %d edges, want 5 / 7", r.NodeCount(), r.EdgeCount())
	}
	coords, adj := r.ExportGraph()
	for id, edges := range adj {
		if len(edges) != cap(edges) {
			t.Fatalf("node %d retains %d spare edge slots", id, cap(edges)-len(edges))
		}
	}
	if edges := adj[20]; len(edges) != 3 || edges[0].To != 10 || edges[1].To != 30 || edges[2].To != 40 {
		t.Fatalf("junction edge ordering changed: %+v", edges)
	}
	if edge := adj[20][2]; edge.SpeedKph != 80 || edge.MaxHeightM != 2.5 || edge.MaxWeightT != 7.5 || edge.HwyType != "residential" {
		t.Fatalf("edge attributes changed: %+v", edge)
	}
	if edges := adj[40]; len(edges) != 1 || edges[0].To != 20 || adj[50][0].To != 40 {
		t.Fatalf("reverse oneway changed: 40=%+v, 50=%+v", edges, adj[50])
	}
	// The shared allocation remains an implementation detail: appending to one
	// exported slice must allocate rather than mutate a neighbouring node.
	want := make(map[int64][]Edge, len(adj))
	for id, edges := range adj {
		want[id] = append([]Edge(nil), edges...)
	}
	expanded := append(adj[20], Edge{To: 999})
	expanded[len(expanded)-1].To = 1000
	if !reflect.DeepEqual(adj, want) {
		t.Fatal("append to one node corrupted another node's edges")
	}
	graphPath := filepath.Join(t.TempDir(), "graph.bin")
	if err := r.WriteGraph(t.Context(), graphPath); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(graphPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	diskCoords, diskAdj := disk.ExportGraph()
	if !reflect.DeepEqual(coords, diskCoords) || !reflect.DeepEqual(adj, diskAdj) {
		t.Fatal("compact adjacency changed the portable graph snapshot")
	}
}
