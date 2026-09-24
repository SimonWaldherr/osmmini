package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini"
)

func TestPOICategoryPromptBoundariesAndAliases(t *testing.T) {
	cases := map[string]string{
		"Wo ist der nächste Parkplatz?": "parking", "Zahnarzt in der Nähe": "dentist",
		"Finde CAFÉS!": "cafe", "Finde Cafe\u0301s": "cafe", "Baeckereien in der Nähe": "bakery",
		"Wo sind Krankenhäuser?": "hospital", "Zeige Fahrradparkplätze": "bicycle_parking",
		"next train station": "station", "Fast-Food in der Nähe": "fast_food",
		"Ladestationen in der Nähe": "charging_station", "Hallenbad in der Nähe": "swimming_pool",
		"Parkstraße 1": "", "Seestraße 4": "", "Bankett": "", "Poststraße": "", "Isar": "",
	}
	for prompt, want := range cases {
		t.Run(prompt, func(t *testing.T) {
			for i := 0; i < 30; i++ {
				_, cat := categoryInPrompt(prompt)
				got := ""
				if cat != nil {
					got = cat.ID
				}
				if got != want {
					t.Fatalf("%q: got %q want %q", prompt, got, want)
				}
			}
		})
	}
}
func TestPOICategoryTagAlternatives(t *testing.T) {
	cases := []struct {
		query string
		tags  osmmini.Tags
		want  bool
	}{
		{"Krankenhäuser", osmmini.Tags{"healthcare": "hospital"}, true},
		{"doctors", osmmini.Tags{"healthcare": "doctor"}, true},
		{"Wald", osmmini.Tags{"natural": "wood"}, true},
		{"forest", osmmini.Tags{"landuse": "forest"}, true},
		{"See", osmmini.Tags{"natural": "water", "water": "lake"}, true},
		{"See", osmmini.Tags{"natural": "water", "water": "river"}, false},
		{"See", osmmini.Tags{"natural": "water"}, false},
		{"water", osmmini.Tags{"natural": "water"}, true},
		{"Schwimmbad", osmmini.Tags{"leisure": "sports_centre", "sport": "fitness; swimming"}, true},
		{"Schwimmbad", osmmini.Tags{"leisure": "sports_centre", "sport": "tennis"}, false},
		{"Bahnhof", osmmini.Tags{"railway": "station"}, true},
		{"Bahnhof", osmmini.Tags{"amenity": "station"}, false},
		{"emergency=fire_hydrant", osmmini.Tags{"emergency": "fire_hydrant"}, true},
		{"amenity=hospital", osmmini.Tags{"healthcare": "hospital"}, false},
		{"physiotherapist", osmmini.Tags{"healthcare": "physiotherapist"}, true},
		{"unknown", osmmini.Tags{"name": "unknown"}, false},
	}
	for _, tt := range cases {
		if got := matchesPOICategory(tt.tags, tt.query); got != tt.want {
			t.Errorf("%s %v: got %v", tt.query, tt.tags, got)
		}
	}
}
func TestGeoCategoryAliasesUseAlternativeTags(t *testing.T) {
	s := &server{poiGeo: buildPOIGeoIndex(nil, map[int64]osmmini.Node{
		1: {ID: 1, Lat: 48, Lon: 12, Tags: osmmini.Tags{"healthcare": "hospital"}},
		2: {ID: 2, Lat: 48, Lon: 12, Tags: osmmini.Tags{"amenity": "hospital"}},
		3: {ID: 3, Lat: 48, Lon: 12, Tags: osmmini.Tags{"amenity": "clinic"}},
	}, nil)}
	for _, query := range []string{"hospital", "Krankenh%C3%A4user"} {
		rec := httptest.NewRecorder()
		s.handleGeoPOIs(rec, httptest.NewRequest("GET", "/?lat=48&lon=12&radius_m=1000&category="+query, nil))
		var got struct{ Matched int }
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || got.Matched != 2 {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
	}
}
func TestAISearchSharesAlternativeCategoryFilters(t *testing.T) {
	s := &server{poiTaggedNodes: map[int64]osmmini.Node{1: {ID: 1, Lat: 48, Lon: 12, Tags: osmmini.Tags{"healthcare": "hospital", "name": "Klinikum"}}}}
	_, key, value := extractPOIFromPrompt("Krankenhäuser in der Nähe")
	if found := s.searchPOIsNear(48, 12, key, value, 10); len(found) != 1 {
		t.Fatalf("AI missed healthcare hospital: %#v", found)
	}
	if len(s.searchPOIMatches("Krankenhäuser", 5)) != 1 {
		t.Fatal("search did not resolve category synonym")
	}
}
func TestScopedCategoryQuerySupportsPhrases(t *testing.T) {
	s := &server{poiTaggedNodes: map[int64]osmmini.Node{
		1: {ID: 1, Lat: 48, Lon: 12, Tags: osmmini.Tags{"place": "town", "name": "Teststadt"}},
		2: {ID: 2, Lat: 48, Lon: 12, Tags: osmmini.Tags{"amenity": "fast_food", "name": "Snack"}},
		3: {ID: 3, Lat: 48, Lon: 12, Tags: osmmini.Tags{"amenity": "restaurant", "name": "Restaurant"}},
	}}
	results, ok := s.aiScopedPOITargets("Fast Food in Teststadt")
	if !ok || len(results) != 1 || results[0].ID != 2 {
		t.Fatalf("scoped category: %v %#v", ok, results)
	}
}

func TestPOICategoryCatalogAliasesAndFilters(t *testing.T) {
	for i := range poiCategories {
		category := &poiCategories[i]
		for _, alias := range append([]string{category.ID}, category.Aliases...) {
			if got := lookupPOICategory(alias); got != category {
				t.Fatalf("alias %q resolves to wrong category", alias)
			}
			_, got := categoryInPrompt("Finde " + alias + " in der Nähe")
			if got != category {
				t.Fatalf("prompt alias %q resolves to wrong category", alias)
			}
		}
		for _, filter := range category.Filters {
			if !matchesPOITag(filter, category.Key, category.Value) {
				t.Fatalf("AI does not match %s %v", category.ID, filter)
			}
		}
	}
}

// docs/osm-categories.md lists every category; keep it in sync when adding one.
func TestPOICategoryDocumentationIsComplete(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "docs", "osm-categories.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range poiCategories {
		if !strings.Contains(string(doc), "| `"+c.ID+"` |") {
			t.Errorf("docs/osm-categories.md is missing category %q", c.ID)
		}
	}
	if want := fmt.Sprintf("%d Kategorien", len(poiCategories)); !strings.Contains(string(doc), want) {
		t.Errorf("docs/osm-categories.md must mention %q", want)
	}
}
