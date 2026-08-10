package osmmini

import "testing"

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
