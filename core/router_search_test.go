package osmmini

import (
	"reflect"
	"testing"
)

func TestParseAddressGuessKeepsPOINamesIntact(t *testing.T) {
	q := ParseAddressGuess("Berlin Hauptbahnhof")
	if q.Street != "Berlin Hauptbahnhof" {
		t.Fatalf("street = %q, want full raw query", q.Street)
	}
	if q.City != "" {
		t.Fatalf("city = %q, want empty city for unstructured POI queries", q.City)
	}
}

func TestParseAddressGuessSplitsCommaSeparatedAddress(t *testing.T) {
	q := ParseAddressGuess("Hauptstraße 5, Berlin")
	if q.Street != "Hauptstraße 5" {
		t.Fatalf("street = %q, want %q", q.Street, "Hauptstraße 5")
	}
	if q.City != "Berlin" {
		t.Fatalf("city = %q, want %q", q.City, "Berlin")
	}
}

func TestSearchAddressesMatchesRawPOIName(t *testing.T) {
	entries := []AddressEntry{
		{
			ID:    1,
			Coord: Coord{Lat: 52.52, Lon: 13.405},
			Tags: Tags{
				"name":          "Berlin Hauptbahnhof",
				"amenity":       "station",
				"addr:street":   "Europaplatz",
				"addr:city":     "Berlin",
				"addr:postcode": "10557",
			},
		},
		{
			ID:    2,
			Coord: Coord{Lat: 52.51, Lon: 13.39},
			Tags: Tags{
				"name":        "Berlin Apotheke",
				"amenity":     "pharmacy",
				"addr:street": "Friedrichstraße",
				"addr:city":   "Berlin",
			},
		},
	}

	results := SearchAddresses(entries, ParseAddressGuess("Berlin Hauptbahnhof"), 5)
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if got := results[0].Tags["name"]; got != "Berlin Hauptbahnhof" {
		t.Fatalf("top result = %q, want Berlin Hauptbahnhof", got)
	}
}

func TestFindBestAddressRejectsHouseNumberOnlyMatch(t *testing.T) {
	entries := []AddressEntry{
		{
			ID:    1,
			Coord: Coord{Lat: 48.5473443, Lon: 12.1205542},
			Tags: Tags{
				"name":             "Arche Noah",
				"addr:street":      "Wilhelm-Dieß-Straße",
				"addr:housenumber": "3",
				"addr:city":        "Landshut",
			},
		},
		{
			ID:    2,
			Coord: Coord{Lat: 48.7971234, Lon: 12.884135},
			Tags: Tags{
				"name":             "RUBIX",
				"addr:street":      "Scheiblerstraße",
				"addr:housenumber": "3",
			},
		},
	}

	// This is the display label a route response returns. It must not degrade
	// into the first arbitrary address with house number 3.
	if got, ok := FindBestAddress(entries, ParseAddressGuess("RUBIX — Scheiblerstraße 3")); ok {
		t.Fatalf("FindBestAddress() = %#v, want no house-number-only match", got)
	}
}

func TestSearchAddressesUnlimitedKeepsBoundedRanking(t *testing.T) {
	entries := []AddressEntry{
		{ID: 3, Tags: Tags{"name": "Hauptstraße"}},
		{ID: 2, Tags: Tags{"name": "Hauptstraße"}},
		{ID: 1, Tags: Tags{"name": "Hauptstraße"}},
		{ID: 4, Tags: Tags{"name": "Nebenstraße"}},
	}
	q := ParseAddressGuess("Hauptstraße")
	all := SearchAddresses(entries, q, 0)
	if len(all) != 3 {
		t.Fatalf("results = %#v", all)
	}
	for _, limit := range []int{1, 2, 3, 4, int(^uint(0) >> 1)} {
		got := SearchAddresses(entries, q, limit)
		want := all[:min(limit, len(all))]
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("limit %d: got %#v, want %#v", limit, got, want)
		}
	}
	if got := SearchAddresses(nil, q, 100); len(got) != 0 {
		t.Fatalf("empty index: %#v", got)
	}
}
