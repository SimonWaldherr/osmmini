package osmmini

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func addressIndexCatalog() []AddressEntry {
	return []AddressEntry{
		{ID: 20, Tags: Tags{"addr:street": "Hauptstraße", "addr:housenumber": "5", "addr:city": "München", "addr:postcode": "80331"}},
		{ID: 3, Tags: Tags{"addr:street": "Hauptstraße", "addr:housenumber": "7", "addr:city": "München", "addr:postcode": "80331"}},
		{ID: 9, Tags: Tags{"name": "Café Müller", "amenity": "cafe"}},
		{ID: 4, Tags: Tags{"name": "Müller Markt", "brand": "Müller", "shop": "supermarket"}},
		{ID: 12, Tags: Tags{"addr:street": "Andere Gasse", "addr:housenumber": "7", "addr:city": "Bonn"}},
	}
}

func TestAddressIndexRankingAndHouseNumberSafety(t *testing.T) {
	entries := addressIndexCatalog()
	index := NewAddressIndex(entries)
	cases := []struct {
		name  string
		query AddressQuery
		limit int
		want  []int64
	}{
		{"street and city", AddressQuery{Street: "HAUPTSTRASSE", City: "Muenchen"}, 10, []int64{3, 20}},
		{"best house", AddressQuery{Street: "Hauptstraße", Housenumber: "7", City: "München"}, 1, []int64{3}},
		{"business name", AddressQuery{Raw: "Café Müller"}, 10, []int64{9}},
		{"category", AddressQuery{Raw: "supermarket"}, 10, []int64{4}},
		{"unlimited stable ranks", AddressQuery{Housenumber: "7"}, 0, []int64{3, 12}},
		{"missing", AddressQuery{Raw: "unfindbar"}, 20, []int64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, search := range []func(AddressQuery, int) []AddressEntry{index.Search, func(q AddressQuery, n int) []AddressEntry { return SearchAddresses(entries, q, n) }} {
				got := search(tc.query, tc.limit)
				ids := make([]int64, len(got))
				for i, entry := range got {
					ids[i] = entry.ID
				}
				if !reflect.DeepEqual(ids, tc.want) {
					t.Fatalf("IDs = %v, want %v", ids, tc.want)
				}
			}
		})
	}
	if _, ok := index.FindBest(AddressQuery{Housenumber: "7"}); ok {
		t.Fatal("accepted ambiguous house number alone")
	}
	if got, ok := index.FindBest(AddressQuery{Street: "Hauptstraße", Housenumber: "7"}); !ok || got.ID != 3 {
		t.Fatalf("best address = %+v, %v", got, ok)
	}
}

func TestAddressIndexConcurrentQueriesAndSnapshot(t *testing.T) {
	entries := addressIndexCatalog()
	index := NewAddressIndex(entries)
	entries[0].ID = 999 // Slice metadata is snapshotted; maps remain read-only.
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Go(func() {
			for n := 0; n < 50; n++ {
				got := index.Search(AddressQuery{Street: "Hauptstraße"}, 10)
				if len(got) != 2 || got[0].ID != 3 || got[1].ID != 20 {
					t.Errorf("concurrent result = %v", got)
					return
				}
				best, ok := index.FindBest(AddressQuery{Raw: "Café Müller"})
				if !ok || best.ID != 9 {
					t.Errorf("concurrent best = %+v, %v", best, ok)
					return
				}
			}
		})
	}
	group.Wait()
}

func benchmarkAddressCatalog(n int) []AddressEntry {
	entries := make([]AddressEntry, n)
	for i := range entries {
		entries[i] = AddressEntry{ID: int64(i + 1), Tags: Tags{"addr:street": fmt.Sprintf("Nebenstraße %d", i), "addr:city": "Musterstadt"}}
	}
	for _, i := range []int{17, 12345, 36789} {
		entries[i].Tags["addr:street"] = "Hauptstraße"
	}
	return entries
}

func BenchmarkAddressIndexSearch(b *testing.B) {
	index := NewAddressIndex(benchmarkAddressCatalog(50_000))
	query := AddressQuery{Street: "Hauptstraße", City: "Musterstadt"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = index.Search(query, 10)
	}
}

func BenchmarkAddressIndexBuild(b *testing.B) {
	entries := benchmarkAddressCatalog(50_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewAddressIndex(entries)
	}
}
