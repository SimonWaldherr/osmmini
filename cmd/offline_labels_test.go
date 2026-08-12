package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini"
)

func TestOfflineLabelsWindow(t *testing.T) {
	window, ok := offlineLabelsWindow("11.2,48.4,11.8,48.9")
	if !ok || window.MinLon != 11.2 || window.MaxLat != 48.9 {
		t.Fatalf("offlineLabelsWindow() = %#v, %v", window, ok)
	}
	for _, invalid := range []string{"", "11,48,12", "181,48,12,49", "nan,48,12,49", "12,49,11,48"} {
		if _, ok := offlineLabelsWindow(invalid); ok {
			t.Fatalf("offlineLabelsWindow(%q) accepted invalid bounds", invalid)
		}
	}
}

func TestHandleOfflineLabelsReturnsLocalPlaces(t *testing.T) {
	s := &server{poiTaggedNodes: map[int64]osmmini.Node{
		1: {ID: 1, Lat: 48.65, Lon: 12.49, Tags: osmmini.Tags{"name": "Landshut", "place": "town"}},
		2: {ID: 2, Lat: 49.20, Lon: 12.10, Tags: osmmini.Tags{"name": "Außerhalb", "place": "village"}},
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/offline-labels?bbox=12.4,48.6,12.6,48.7&zoom=10", nil)
	res := httptest.NewRecorder()
	s.handleOfflineLabels(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, `"name":"Landshut"`) || strings.Contains(body, "Außerhalb") {
		t.Fatalf("unexpected labels: %s", body)
	}
}

func TestPlaceLabelsUseVisibleCellOnly(t *testing.T) {
	nodes := map[int64]osmmini.Node{
		1: {ID: 1, Lat: 48.65, Lon: 12.49, Tags: osmmini.Tags{"name": "Landshut", "place": "town"}},
		2: {ID: 2, Lat: 49.20, Lon: 12.10, Tags: osmmini.Tags{"name": "Außerhalb", "place": "village"}},
	}
	s := &server{poiTaggedNodes: nodes, poiPlaceCells: buildPlaceLabelCells(nodes)}
	labels := s.placeLabels(osmmini.CoordWindow{MinLat: 48.6, MaxLat: 48.7, MinLon: 12.4, MaxLon: 12.6}, 10)
	if len(labels) != 1 || labels[0].Name != "Landshut" {
		t.Fatalf("placeLabels() = %#v", labels)
	}
}
