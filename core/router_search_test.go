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
	if q.Street != "Hauptstraße" {
		t.Fatalf("street = %q, want %q", q.Street, "Hauptstraße")
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

func TestParseAddressGuessSeparatesAddressFields(t *testing.T) {
	for _, tc := range []struct{ raw, street, house, postcode, city string }{
		{"Hauptstraße 5, Berlin", "Hauptstraße", "5", "", "Berlin"},
		{"12a Lange Gasse", "Lange Gasse", "12a", "", ""},
		{"12a Lange Gasse, München", "Lange Gasse", "12a", "", "München"},
		{"Lange Gasse 12a", "Lange Gasse", "12a", "", ""},
		{"Lange Gasse 12/1, 80331 München", "Lange Gasse", "12/1", "80331", "München"},
		{"Lange Gasse 12-14 80331 München", "Lange Gasse", "12-14", "80331", "München"},
		{"Lange Gasse 5 Bad Griesbach", "Lange Gasse", "5", "", "Bad Griesbach"},
		{"Straße des 17. Juni 5, Berlin", "Straße des 17. Juni", "5", "", "Berlin"},
		{"B12 Raststätte", "B12 Raststätte", "", "", ""},
		{"Berlin Hauptbahnhof", "Berlin Hauptbahnhof", "", "", ""},
		{"80331 München", "", "", "80331", "München"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got := ParseAddressGuess(tc.raw)
			want := AddressQuery{Street: tc.street, Housenumber: tc.house, Postcode: tc.postcode, City: tc.city, Raw: tc.raw}
			if got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestFindBestAddressRequiresRequestedAddressFields(t *testing.T) {
	entries := []AddressEntry{
		{ID: 1, Tags: Tags{"addr:street": "Hauptstraße", "addr:housenumber": "7", "addr:postcode": "80331", "addr:city": "München"}},
		{ID: 2, Tags: Tags{"addr:street": "Nebenstraße", "addr:housenumber": "5", "addr:postcode": "80331", "addr:city": "München"}},
	}
	index := NewAddressIndex(entries)
	for _, raw := range []string{"Hauptstraße 5, München", "Hauptstraße 7, Bonn", "Hauptstraße 7, 99999 München", "Unbekannt 5, München", "Unbekannt, München"} {
		for _, find := range []func(AddressQuery) (AddressEntry, bool){index.FindBest, func(q AddressQuery) (AddressEntry, bool) { return FindBestAddress(entries, q) }} {
			if got, ok := find(ParseAddressGuess(raw)); ok {
				t.Errorf("%q resolved to %+v", raw, got)
			}
		}
	}
	for _, find := range []func(AddressQuery) (AddressEntry, bool){index.FindBest, func(q AddressQuery) (AddressEntry, bool) { return FindBestAddress(entries, q) }} {
		if got, ok := find(ParseAddressGuess("Hauptstraße 7, 80331 München")); !ok || got.ID != 1 {
			t.Errorf("exact address = %+v, %v", got, ok)
		}
	}
}

func TestFindBestAddressPreservesNumericNamesAndTieOrder(t *testing.T) {
	entries := []AddressEntry{
		{ID: 20, Tags: Tags{"name": "Hotel 4 Jahreszeiten"}},
		{ID: 9, Tags: Tags{"name": "Hotel 4 Jahreszeiten"}},
		{ID: 3, Tags: Tags{"addr:street": "Straße des 1 Mai"}},
		{ID: 4, Tags: Tags{"addr:street": "Lange Gasse", "addr:housenumber": "12a", "addr:place": "München"}},
	}
	index := NewAddressIndex(entries)
	for _, tc := range []struct {
		raw string
		id  int64
	}{
		{"Hotel 4 Jahreszeiten", 9}, {"Straße des 1 Mai", 3}, {"Lange Gasse 12a, Mu\u0308nchen", 4},
	} {
		for _, find := range []func(AddressQuery) (AddressEntry, bool){index.FindBest, func(q AddressQuery) (AddressEntry, bool) { return FindBestAddress(entries, q) }} {
			if got, ok := find(ParseAddressGuess(tc.raw)); !ok || got.ID != tc.id {
				t.Errorf("%q: %+v, %v", tc.raw, got, ok)
			}
		}
	}
}

func TestFindBestAddressDoesNotIgnoreExplicitFieldsForExactNames(t *testing.T) {
	entries := []AddressEntry{{ID: 1, Tags: Tags{"name": "Hotel 4 Jahreszeiten", "addr:street": "Lange Gasse", "addr:housenumber": "7", "addr:city": "München", "addr:postcode": "80331"}}}
	index := NewAddressIndex(entries)
	for _, query := range []AddressQuery{
		{Raw: "Hotel 4 Jahreszeiten", City: "Bonn"},
		{Raw: "Hotel 4 Jahreszeiten", Street: "Nebenstraße"},
		{Raw: "Hotel 4 Jahreszeiten", Housenumber: "99"},
		{Raw: "Hotel 4 Jahreszeiten", Postcode: "99999"},
		{Raw: "Lange Gasse", Street: "Lange Gasse", Housenumber: "99"},
	} {
		for _, find := range []func(AddressQuery) (AddressEntry, bool){index.FindBest, func(q AddressQuery) (AddressEntry, bool) { return FindBestAddress(entries, q) }} {
			if got, ok := find(query); ok {
				t.Errorf("%+v incorrectly resolved to %+v", query, got)
			}
		}
	}
}
