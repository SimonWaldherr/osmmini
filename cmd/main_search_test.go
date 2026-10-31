package main

import (
	"errors"
	"strings"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

func TestFormatAddressLabelIncludesNameAndAddress(t *testing.T) {
	got := formatAddressLabel(osmmini.Tags{
		"name":             "REWE Center",
		"addr:street":      "Hauptstraße",
		"addr:housenumber": "5",
		"addr:postcode":    "50667",
		"addr:city":        "Köln",
	})
	want := "REWE Center — Hauptstraße 5, 50667 Köln"
	if got != want {
		t.Fatalf("formatAddressLabel() = %q, want %q", got, want)
	}
}

func TestSearchLocationResultsIncludesPOIWithContext(t *testing.T) {
	s := &server{
		router:   &osmmini.Router{},
		poiNodes: map[int64]osmmini.Coord{1: {Lat: 52.52, Lon: 13.405}, 2: {Lat: 52.5202, Lon: 13.4052}},
		poiWays: map[int64]osmmini.Way{
			10: {
				ID:      10,
				NodeIDs: []int64{1, 2},
				Tags: osmmini.Tags{
					"name":        "Berlin Hauptbahnhof",
					"amenity":     "station",
					"addr:street": "Europaplatz",
					"addr:city":   "Berlin",
				},
			},
		},
	}

	results := s.searchLocationResults("Berlin Hauptbahnhof", 5)
	if len(results) == 0 {
		t.Fatal("expected at least one search result")
	}

	found := false
	for _, result := range results {
		if result.Kind == "poi" && result.Label == "Berlin Hauptbahnhof — Europaplatz, Berlin" {
			found = true
			if result.Subtitle == "" {
				t.Fatal("expected subtitle for POI result")
			}
			if result.Match == "" {
				t.Fatal("expected match reason for POI result")
			}
		}
	}
	if !found {
		t.Fatalf("expected POI result in %#v", results)
	}
}

func TestSearchLocationResultsAddsNearbyTownForSparseAddress(t *testing.T) {
	plattling := osmmini.Node{
		ID:   446250344,
		Lat:  48.7766852,
		Lon:  12.8733655,
		Tags: osmmini.Tags{"name": "Plattling", "place": "town"},
	}
	s := &server{
		router:         &osmmini.Router{},
		poiTaggedNodes: map[int64]osmmini.Node{plattling.ID: plattling},
		addrs: []osmmini.AddressEntry{{
			ID:    12257954539,
			Coord: osmmini.Coord{Lat: 48.7971234, Lon: 12.884135},
			Tags: osmmini.Tags{
				"name":             "RUBIX",
				"addr:street":      "Scheiblerstraße",
				"addr:housenumber": "3",
			},
		}},
	}
	s.poiPlaceCells = buildPlaceLabelCells(s.poiTaggedNodes)

	results := s.searchLocationResults("Rubix", 5)
	if len(results) == 0 || !strings.Contains(results[0].Subtitle, "Plattling") {
		t.Fatalf("search results = %#v, want Plattling context", results)
	}
}

func TestSearchLocationResultsDoesNotDuplicateOneOSMFeature(t *testing.T) {
	const rubixID = int64(12257954539)
	coord := osmmini.Coord{Lat: 48.7971234, Lon: 12.884135}
	s := &server{
		router: &osmmini.Router{},
		addrs: []osmmini.AddressEntry{{
			ID:    rubixID,
			Coord: coord,
			Tags:  osmmini.Tags{"name": "RUBIX", "addr:street": "Scheiblerstraße", "addr:housenumber": "3"},
		}},
		poiTaggedNodes: map[int64]osmmini.Node{
			rubixID: {ID: rubixID, Lat: coord.Lat, Lon: coord.Lon, Tags: osmmini.Tags{"name": "RUBIX", "office": "company"}},
		},
	}

	results := s.searchLocationResults("Rubix", 5)
	if len(results) != 1 {
		t.Fatalf("search results = %#v, want one physical RUBIX feature", results)
	}
	if results[0].Label != "RUBIX — Scheiblerstraße 3" {
		t.Fatalf("result label = %q", results[0].Label)
	}
}

func TestResolveLocationReusesNameFromDisplayedPOILabel(t *testing.T) {
	landshut := osmmini.AddressEntry{
		ID:    1,
		Coord: osmmini.Coord{Lat: 48.5473443, Lon: 12.1205542},
		Tags: osmmini.Tags{
			"name":             "Arche Noah",
			"addr:street":      "Wilhelm-Dieß-Straße",
			"addr:housenumber": "3",
			"addr:city":        "Landshut",
		},
	}
	plattling := osmmini.AddressEntry{
		ID:    2,
		Coord: osmmini.Coord{Lat: 48.7971234, Lon: 12.884135},
		Tags: osmmini.Tags{
			"name":             "RUBIX",
			"addr:street":      "Scheiblerstraße",
			"addr:housenumber": "3",
		},
	}
	s := &server{router: &osmmini.Router{}, addrs: []osmmini.AddressEntry{landshut, plattling}}

	coord, label, _, err := s.resolveLocation(Location{Query: "RUBIX — Scheiblerstraße 3"})
	if err != nil {
		t.Fatalf("resolveLocation() error = %v", err)
	}
	if coord != plattling.Coord {
		t.Fatalf("resolveLocation() coord = %#v, want RUBIX in Plattling %#v", coord, plattling.Coord)
	}
	if label != "RUBIX — Scheiblerstraße 3" {
		t.Fatalf("resolveLocation() label = %q", label)
	}
}

func TestResolveLocationDoesNotPickAnArbitraryHouseNumber(t *testing.T) {
	s := &server{
		router: &osmmini.Router{},
		addrs: []osmmini.AddressEntry{
			{ID: 1, Coord: osmmini.Coord{Lat: 48.5473443, Lon: 12.1205542}, Tags: osmmini.Tags{"addr:street": "Wilhelm-Dieß-Straße", "addr:housenumber": "3", "addr:city": "Landshut"}},
			{ID: 2, Coord: osmmini.Coord{Lat: 48.7971234, Lon: 12.884135}, Tags: osmmini.Tags{"addr:street": "Scheiblerstraße", "addr:housenumber": "3"}},
		},
	}

	_, _, _, err := s.resolveLocation(Location{Query: "unbekannter Ort 3"})
	if err == nil {
		t.Fatal("resolveLocation() unexpectedly accepted a house-number-only match")
	}
	var resolveErr *locationResolveError
	if !errors.As(err, &resolveErr) || !resolveErr.Ambiguous {
		t.Fatalf("resolveLocation() error = %T %v, want ambiguous location error", err, err)
	}
	if len(resolveErr.Suggestions) != 2 {
		t.Fatalf("suggestions = %#v, want both address candidates", resolveErr.Suggestions)
	}
}

func TestExtractPOIFromPromptRecognizesGenericFastFood(t *testing.T) {
	intent := classifyPromptIntent("Wo ist der nächste Fast Food?")
	if intent.Type != intentPOINear || intent.POIType != "fast food" || intent.POITagKey != "amenity" || intent.POITagVal != "fast_food" {
		t.Fatalf("classifyPromptIntent() = %#v, want generic fast-food POI intent", intent)
	}
}
