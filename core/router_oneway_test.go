package osmmini

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildRouterRoundaboutOnewayOverrides(t *testing.T) {
	previousAllowed, previousSpeeds := AllowedHighwayTypesMap, DefaultHighwaySpeeds
	AllowedHighwayTypesMap = nil
	DefaultHighwaySpeeds = map[string]float64{"residential": 30}
	t.Cleanup(func() {
		AllowedHighwayTypesMap, DefaultHighwaySpeeds = previousAllowed, previousSpeeds
	})
	for _, tc := range []struct {
		name, junction, oneway string
		forward, backward      bool
	}{
		{"implicit", "roundabout", "", true, false},
		{"explicitYes", "roundabout", "yes", true, false},
		{"explicitNo", "roundabout", "no", true, true},
		{"explicitFalse", "roundabout", "false", true, true},
		{"explicitZero", "roundabout", "0", true, true},
		{"normalizedNo", "roundabout", " NO ", true, true},
		{"reverse", "roundabout", "-1", false, true},
		{"reverseAlias", "roundabout", "reverse", false, true},
		{"ordinaryStreet", "", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A roundabout can consist of several open ways. Three nodes model
			// one such section, so the reverse direction cannot take another arc.
			var group []byte
			for id := int64(1); id <= 3; id++ {
				node := testPBFNode(id, nil, nil, 480000000+id*10000, 120000000)
				group = append(group, testPBFPrimitiveGroup(1, node)...)
			}
			keys, values := []uint64{1}, []uint64{2}
			if tc.junction != "" {
				keys, values = append(keys, 3), append(values, 4)
			}
			if tc.oneway != "" {
				keys, values = append(keys, 5), append(values, 6)
			}
			way := testPBFWay(100, keys, values, []int64{1, 1, 1})
			group = append(group, testPBFPrimitiveGroup(3, way)...)
			data := testPBFData([]string{"", "highway", "residential", "junction", tc.junction, "oneway", tc.oneway}, group)
			path := filepath.Join(t.TempDir(), "roundabout.osm.pbf")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			router, _, err := BuildRouterWithAddressesOptions(path, BuildOptions{Context: t.Context(), MaxNodes: 10, SkipAddresses: true})
			if err != nil {
				t.Fatal(err)
			}
			wantEdges := 0
			if tc.forward {
				wantEdges += 2
			}
			if tc.backward {
				wantEdges += 2
			}
			if router.NodeCount() != 3 || router.EdgeCount() != wantEdges {
				t.Errorf("graph = %d nodes / %d edges, want 3 / %d", router.NodeCount(), router.EdgeCount(), wantEdges)
			}
			for _, direction := range []struct {
				from, to int64
				allowed  bool
			}{{1, 3, tc.forward}, {3, 1, tc.backward}} {
				result, err := router.RouteWithOptions(t.Context(), direction.from, direction.to, RouteOptions{Engine: EngineDijkstra})
				if direction.allowed {
					if err != nil || len(result.Path) != 3 || result.DistanceM <= 0 {
						t.Errorf("allowed %d -> %d: %+v, %v", direction.from, direction.to, result, err)
					}
				} else if err == nil {
					t.Errorf("forbidden %d -> %d returned path %v", direction.from, direction.to, result.Path)
				}
			}
		})
	}
}
