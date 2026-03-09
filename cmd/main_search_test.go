package main

import (
	"testing"

	osmmini "simonwaldherr.de/go/osmmini"
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
