package osmmini

import (
	"fmt"
	"reflect"
	"testing"
)

func TestStreetLabelsUsesOnlyVisibleSampleNodes(t *testing.T) {
	r := &Router{
		g: Graph{coords: map[int64]Coord{
			1: {Lat: 48.10, Lon: 11.10},
			2: {Lat: 49.10, Lon: 12.10},
		}},
		streets: map[string]streetEntry{
			"main":  {Display: "Hauptstraße", NodeIDs: []int64{1}},
			"other": {Display: "Fernstraße", NodeIDs: []int64{2}},
		},
	}
	labels := r.StreetLabels(CoordWindow{MinLat: 48, MaxLat: 48.5, MinLon: 11, MaxLon: 11.5}, 10)
	if len(labels) != 1 || labels[0].Name != "Hauptstraße" || labels[0].Coord.Lat != 48.10 {
		t.Fatalf("StreetLabels() = %#v", labels)
	}
	if got := r.StreetLabels(CoordWindow{}, 10); got != nil {
		t.Fatalf("StreetLabels invalid window = %#v, want nil", got)
	}
}

func TestStreetLabelsCoverViewportDespiteDenseCenter(t *testing.T) {
	r := &Router{g: Graph{coords: map[int64]Coord{}}, streets: map[string]streetEntry{}}
	for i := 1; i <= 100; i++ {
		name := fmt.Sprintf("Center %03d", i)
		r.g.coords[int64(i)] = Coord{Lat: 48.5 + float64(i)*0.00001, Lon: 11.5}
		r.streets[name] = streetEntry{Display: name, NodeIDs: []int64{int64(i)}}
	}
	for i, coord := range []Coord{{48.05, 11.05}, {48.05, 11.95}, {48.95, 11.05}, {48.95, 11.95}} {
		id := int64(101 + i)
		name := fmt.Sprintf("Edge %d", i)
		r.g.coords[id] = coord
		r.streets[name] = streetEntry{Display: name, NodeIDs: []int64{id}}
	}
	window := CoordWindow{MinLat: 48, MaxLat: 49, MinLon: 11, MaxLon: 12}
	scan := r.StreetLabels(window, 32)
	r.streetLabelCells = buildStreetLabelCells(r.streets, r.g.coords)
	indexed := r.StreetLabels(window, 32)
	if !reflect.DeepEqual(scan, indexed) {
		t.Fatal("indexed and fallback label selection differ")
	}
	if len(indexed) != 32 {
		t.Fatalf("got %d labels, want 32", len(indexed))
	}
	names := map[string]bool{}
	for _, label := range indexed {
		names[label.Name] = true
	}
	for i := 0; i < 4; i++ {
		if !names[fmt.Sprintf("Edge %d", i)] {
			t.Fatalf("missing viewport corner %d", i)
		}
	}
}

func TestManeuverStreetNamesIncludeUnmappedNodes(t *testing.T) {
	path := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	coords := make(map[int64]Coord, len(path))
	for i, id := range path {
		coords[id] = Coord{Lat: 48 + float64(i)*0.0001, Lon: 12}
	}
	for _, scenario := range []struct {
		name    string
		streets map[string]streetEntry
		want    int
	}{
		{
			name: "mixed",
			streets: map[string]streetEntry{
				"main":  {Display: "Hauptstraße", NodeIDs: []int64{2, 3}},
				"side":  {Display: "Nebenstraße", NodeIDs: []int64{5}},
				"north": {Display: "Nordstraße", NodeIDs: []int64{6}},
			},
			want: 3,
		},
		{
			name: "unnamed",
			streets: map[string]streetEntry{
				"other": {Display: "Andere Straße", NodeIDs: []int64{999}},
			},
			want: 2,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			router := &Router{g: Graph{coords: coords}, streets: scenario.streets}
			opt := RouteOptions{Objective: ObjectiveDuration}.withDefaults()
			fallback := router.ManeuversForPath(path, opt)
			router.streetNameByNode = buildStreetNameIndex(scenario.streets)
			indexed := router.ManeuversForPath(path, opt)
			if !reflect.DeepEqual(indexed, fallback) {
				t.Fatalf("indexed and fallback instructions differ: indexed=%+v fallback=%+v", indexed, fallback)
			}
			if len(indexed) != scenario.want || indexed[0].Type != "depart" || indexed[len(indexed)-1].Type != "arrive" {
				t.Fatalf("unexpected instructions: %+v", indexed)
			}
			if scenario.name == "mixed" && (indexed[1].Node != 5 || indexed[1].Instruction != "Abbiegen auf Nordstraße") {
				t.Fatalf("lost street-name transition: %+v", indexed[1])
			}
			if name := router.streetNameForNode(4); name != "" {
				t.Fatalf("unmapped node got street name %q", name)
			}
		})
	}
}

func TestStreetNameIndexUsesStableJunctionName(t *testing.T) {
	streets := map[string]streetEntry{
		"z": {Display: "Zollstraße", NodeIDs: []int64{1, 2}},
		"a": {Display: "Allee", NodeIDs: []int64{1}},
	}
	router := &Router{streets: streets, streetNameByNode: buildStreetNameIndex(streets)}
	for _, lookup := range []struct {
		node int64
		want string
	}{{1, "Allee"}, {2, "Zollstraße"}, {3, ""}} {
		if got := router.streetNameForNode(lookup.node); got != lookup.want {
			t.Fatalf("node %d got street name %q, want %q", lookup.node, got, lookup.want)
		}
	}
}
