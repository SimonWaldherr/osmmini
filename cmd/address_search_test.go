package main

import (
	"reflect"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

func TestPreparedAddressIndexPreservesServerResults(t *testing.T) {
	entries := []osmmini.AddressEntry{
		{ID: 1, Coord: osmmini.Coord{Lat: 48, Lon: 12}, Tags: osmmini.Tags{"name": "Café Müller", "amenity": "cafe", "addr:street": "Hauptstraße", "addr:housenumber": "3", "addr:city": "Musterstadt"}},
		{ID: 2, Coord: osmmini.Coord{Lat: 49, Lon: 13}, Tags: osmmini.Tags{"addr:street": "Nebenstraße", "addr:housenumber": "3", "addr:city": "Beispielstadt"}},
	}
	plain := &server{router: &osmmini.Router{}, addrs: entries}
	prepared := &server{router: &osmmini.Router{}, addrs: entries, addressIndex: osmmini.NewAddressIndex(entries)}
	for _, query := range []string{"Café Müller", "Hauptstraße 3, Musterstadt", "3", "unbekannt"} {
		want := plain.searchLocationResults(query, 10)
		got := prepared.searchLocationResults(query, 10)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: indexed %#v, plain %#v", query, got, want)
		}
		q := osmmini.ParseAddressGuess(query)
		wantBest, wantOK := plain.findBestAddress(q)
		gotBest, gotOK := prepared.findBestAddress(q)
		if gotOK != wantOK || !reflect.DeepEqual(gotBest, wantBest) {
			t.Fatalf("%q: index changed best-address resolution", query)
		}
	}
}
