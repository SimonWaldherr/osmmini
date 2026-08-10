package main

import (
	"testing"

	osmmini "simonwaldherr.de/go/osmmini"
)

func TestGroupPostalTerritoriesByPrefix(t *testing.T) {
	postcodes := []*osmmini.Territory{
		postalTerritory("12345"),
		postalTerritory("12399"),
		postalTerritory("98765"),
	}
	grouped, err := groupPostalTerritories(postcodes, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(grouped) != 2 || grouped[0].ID != "123" || grouped[1].ID != "987" {
		t.Fatalf("groups = %#v", grouped)
	}
	if got := len(grouped[0].Geometry.Polygons); got != 2 {
		t.Fatalf("PLZ3 123 polygons = %d, want 2", got)
	}
	if got := grouped[0].Properties["source"]; got != "OpenStreetMap boundary=postal_code" {
		t.Fatalf("source = %#v", got)
	}

	data, err := marshalTerritoryFeatureCollection(grouped)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := osmmini.ParseTerritoriesGeoJSON(data)
	if err != nil {
		t.Fatalf("generated GeoJSON does not reload: %v\n%s", err, data)
	}
	if len(loaded) != 2 || loaded[0].ID != "123" || loaded[1].ID != "987" {
		t.Fatalf("reloaded = %#v", loaded)
	}
}

func TestGroupPostalTerritoriesRejectsMissingFiveDigitCodes(t *testing.T) {
	_, err := groupPostalTerritories([]*osmmini.Territory{postalTerritory("1234")}, 3)
	if err == nil {
		t.Fatal("short postcode was accepted")
	}
}

func postalTerritory(postcode string) *osmmini.Territory {
	return &osmmini.Territory{
		ID: postcode,
		Properties: map[string]any{
			"postcode": postcode,
		},
		Geometry: osmmini.Geometry{Polygons: []osmmini.Polygon{{Outer: osmmini.Ring{
			{Lat: 48, Lon: 12}, {Lat: 48, Lon: 13}, {Lat: 49, Lon: 13}, {Lat: 48, Lon: 12},
		}}}},
	}
}
