package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	osmmini "simonwaldherr.de/go/osmmini/core"
	"testing"
)

func navigationFixture() *server {
	return &server{
		poiNodes: map[int64]osmmini.Coord{1: {Lat: 48.6, Lon: 12.7}},
		poiWays: map[int64]osmmini.Way{
			1: {ID: 1, NodeIDs: []int64{1}, Tags: osmmini.Tags{"building": "yes"}},
			2: {ID: 2, NodeIDs: []int64{1}, Tags: osmmini.Tags{"name": "Unrelated shop"}},
		},
		poiTaggedNodes: map[int64]osmmini.Node{
			3: {ID: 3, Lat: 48.61, Lon: 12.7, Tags: osmmini.Tags{"name": "Autohaus Dingolfing"}},
			4: {ID: 4, Lat: 48.63, Lon: 12.5, Tags: osmmini.Tags{"name": "Dingolfing", "place": "town"}},
			5: {ID: 5, Lat: 48.6, Lon: 12.7, Tags: osmmini.Tags{"brand": "Other brand"}},
		},
	}
}

func TestAINavigationNameRelevanceBeforeDistance(t *testing.T) {
	s := navigationFixture()
	for _, near := range []bool{false, true} {
		var coord osmmini.Coord
		var label string
		var ok bool
		if near {
			coord, label, ok = s.resolvePOIFuzzyNear("Dingolfing", 48.6, 12.7)
		} else {
			coord, label, ok = s.resolvePOIFuzzy("Dingolfing")
		}
		if !ok || label != "Dingolfing" || coord.Lon != 12.5 {
			t.Fatalf("wrong destination: %v %s %v", coord, label, ok)
		}
	}
	for _, query := range []string{"", "Atlantis", "Unrelated shop on a nonexistent street"} {
		if _, label, ok := s.resolvePOIFuzzyNear(query, 48.6, 12.7); ok {
			t.Fatalf("%q matched %q", query, label)
		}
		if _, label, ok := s.resolvePOIFuzzy(query); ok {
			t.Fatalf("%q matched %q without position", query, label)
		}
	}
}

func TestAINavigationPreservesRequestedDestination(t *testing.T) {
	s := navigationFixture()
	s.settings = NewSettingsStore("", DefaultSettings("", ""))
	s.router = osmmini.NewRouterFromGraph(map[int64]osmmini.Coord{1: {Lat: 48.6, Lon: 12.7}, 2: {Lat: 48.63, Lon: 12.5}}, map[int64][]osmmini.Edge{
		1: {{To: 2, DistM: 16000, SpeedKph: 50, HwyType: "primary"}},
		2: {{To: 1, DistM: 16000, SpeedKph: 50, HwyType: "primary"}},
	})
	lat, lon := 48.6, 12.7
	recorder := httptest.NewRecorder()
	req := aiQueryRequest{Prompt: "route nach Dingolfing", MapLat: &lat, MapLon: &lon, RouteFrom: "48.6,12.7"}
	lat, lon = 48.63, 12.5 // A panned map must not replace the selected start.
	if !s.handleIntentLocally(context.Background(), recorder, req, promptIntent{Type: intentNavigate, Destination: "Dingolfing"}) {
		t.Fatal("not handled")
	}
	if recorder.Code != 200 {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response aiQueryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.To.Label != "Dingolfing" || response.To.Query != "Dingolfing" || *response.To.Lon != 12.5 || response.Route.From.Node != 1 || response.Route.To.Node != 2 || response.Route.To.Label != "Dingolfing" {
		t.Fatalf("wrong route: %s", recorder.Body.String())
	}
}

func TestAIScopedCinemaTargets(t *testing.T) {
	s := navigationFixture()
	s.poiTaggedNodes[10] = osmmini.Node{ID: 10, Lat: 48.64688, Lon: 12.48680, Tags: osmmini.Tags{"amenity": "cinema", "name": "cinema filmpalais"}}
	s.poiTaggedNodes[11] = osmmini.Node{ID: 11, Lat: 48.778, Lon: 12.875, Tags: osmmini.Tags{"place": "town", "name": "Plattling"}}
	s.poiTaggedNodes[12] = osmmini.Node{ID: 12, Lat: 48.78, Lon: 12.87, Tags: osmmini.Tags{"amenity": "cinema", "name": "FOCUS Cinemas Kino Center"}}
	for _, tc := range []struct{ prompt, label string }{
		{"Kino Dingolfing", "cinema filmpalais"},
		{"oder Focus Cinema Plattling", "FOCUS Cinemas Kino Center"},
		{"ok, bitte alternativ Focus Cinema Plattling", "FOCUS Cinemas Kino Center"},
	} {
		intent := classifyPromptIntent(tc.prompt)
		if intent.Type != intentNavigate {
			t.Fatalf("wrong intent: %#v", intent)
		}
		targets, scoped := s.aiScopedPOITargets(intent.Destination)
		if !scoped || len(targets) != 1 || targets[0].Label != tc.label {
			t.Fatalf("%s: %#v scoped=%v", tc.prompt, targets, scoped)
		}
	}
	if targets, scoped := s.aiScopedPOITargets("Focus Cinema Dingolfing"); !scoped || len(targets) != 0 {
		t.Fatalf("matched wrong town: %#v", targets)
	}
	if _, scoped := s.aiScopedPOITargets("Dingolfing"); scoped {
		t.Fatal("plain town search intercepted")
	}
	s.poiTaggedNodes[13] = osmmini.Node{ID: 13, Lat: 48.647, Lon: 12.487, Tags: osmmini.Tags{"amenity": "cinema", "name": "Zweites Kino"}}
	targets, _ := s.aiScopedPOITargets("Kino Dingolfing")
	if len(targets) != 2 {
		t.Fatalf("ambiguity lost: %#v", targets)
	}
	s.settings = NewSettingsStore("", DefaultSettings("", ""))
	lat, lon := 48.6, 12.7
	rec := httptest.NewRecorder()
	s.handleIntentLocally(context.Background(), rec, aiQueryRequest{MapLat: &lat, MapLon: &lon}, promptIntent{Type: intentNavigate, Destination: "Kino Dingolfing"})
	var response aiQueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || response.Model != "target-choice" || len(response.Suggestions) != 2 || response.Route != nil {
		t.Fatalf("no actionable choice: %s", rec.Body.String())
	}
}
