package mobile

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSearchNormalization(t *testing.T) {
	cases := map[string]string{
		"already clean cafe 42":                  "already clean cafe 42",
		" Café Müller · München\tHauptstraße 7 ": "cafe muller munchen hauptstrasse 7",
		"ÄÖÜẞ ÉCOLE":                             "aouss ecole",
		"αθήνα 東京 ٢٣":                            "αθήνα 東京 ٢٣",
		"--___\n":                                "",
		"Mu\u0308nchen Hauptstraße":              "munchen hauptstrasse",
		"Cafe\u0301 Mueller":                     "cafe mueller",
		"a  b":                                   "a b",
		"a\x00b":                                 "a b",
	}
	for input, want := range cases {
		if got := normalized(input); got != want {
			t.Errorf("normalized(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDiskSearchPrefixSubstringAliasesAndLimits(t *testing.T) {
	results := make([]SearchResult, 200)
	for i := range results {
		results[i] = SearchResult{ID: int64(i + 1), Kind: "node", Label: fmt.Sprintf("München Hauptstraße %03d", i), Category: "pharmacy"}
	}
	results = append(results, SearchResult{ID: 1001, Kind: "node", Label: "Café Cafeteria", Category: "cafe"})
	s, err := openSearch(writeSearchCatalog(t, results))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for _, query := range []string{"munch 123", "123 munch", "munchen 123"} {
		got, err := s.search(t.Context(), query, 20)
		if err != nil || len(got) != 1 || got[0].ID != 124 || got[0].Text != "" {
			t.Fatalf("%q: %+v, %v", query, got, err)
		}
	}
	got, err := s.search(t.Context(), "cafe", 20)
	if err != nil || len(got) != 1 || got[0].ID != 1001 {
		t.Fatalf("duplicate prefix terms: %+v, %v", got, err)
	}
	got, err = s.search(t.Context(), "APOTHEKE", 7)
	if err != nil || len(got) != 7 || got[0].ID != 1 || got[6].ID != 7 {
		t.Fatalf("alias/limit: %+v, %v", got, err)
	}
	got, err = s.search(t.Context(), "missing", 20)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty result: %+v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.search(ctx, "munchen missing", 20); err == nil {
		t.Fatal("cancelled search succeeded")
	}
}

func TestDiskSearchConcurrentQueries(t *testing.T) {
	results := []SearchResult{{ID: 1, Kind: "node", Label: "Café Müller", Category: "cafe"}, {ID: 2, Kind: "node", Label: "Apotheke Nord", Category: "pharmacy"}, {ID: 3, Kind: "street", Label: "Hauptstraße"}}
	s, err := openSearch(writeSearchCatalog(t, results))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Go(func() {
			for n := 0; n < 30; n++ {
				for _, tc := range []struct {
					query string
					ids   []int64
				}{{"cafe", []int64{1}}, {"apotheke", []int64{2}}, {"haupt", []int64{3}}, {"nichts", []int64{}}} {
					got, err := s.search(t.Context(), tc.query, 10)
					ids := make([]int64, len(got))
					for j, r := range got {
						ids[j] = r.ID
					}
					if err != nil || !reflect.DeepEqual(ids, tc.ids) {
						t.Errorf("%q: %v, %v", tc.query, ids, err)
						return
					}
				}
			}
		})
	}
	group.Wait()
}

func TestDiskSearchRejectsInvalidRecordBounds(t *testing.T) {
	for _, offset := range []uint64{1 << 63, 1 << 40} {
		path := writeSearchCatalog(t, []SearchResult{{ID: 1, Kind: "node", Label: "Cafe"}})
		index, err := os.OpenFile(path+".idx", os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		var value [8]byte
		binary.LittleEndian.PutUint64(value[:], offset)
		_, err = index.WriteAt(value[:], 96)
		index.Close()
		if err != nil {
			t.Fatal(err)
		}
		s, err := openSearch(path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.search(t.Context(), "cafe", 10)
		s.close()
		if err == nil {
			t.Fatalf("accepted invalid record offset %d", offset)
		}
	}
}

func TestDiskSearchCategoryAliasesWithLocalities(t *testing.T) {
	path := writeSearchCatalog(t, []SearchResult{
		{ID: 1, Label: "Sonnen-Apotheke", Category: "pharmacy", Text: "München"},
		{ID: 2, Label: "Süd", Category: "fuel", Text: "München"},
		{ID: 3, Label: "Klinikum", Category: "hospital", Text: "München"},
		{ID: 4, Label: "Markt", Category: "supermarket", Text: "München"},
		{ID: 5, Label: "Wache", Category: "fire_station", Text: "München"},
		{ID: 6, Label: "Sonnen-Apotheke", Category: "pharmacy", Text: "Berlin"},
		{ID: 7, Label: "Apothekenstraße", Category: "street", Text: "München"},
	})
	compact := compactSearchCatalog(t, path)
	for _, scenario := range []struct{ name, path string }{{"legacy", path}, {"compact", compact}, {"compressed", compressedSearchCatalog(t, compact)}} {
		t.Run(scenario.name, func(t *testing.T) {
			reader, err := openSearch(scenario.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.close()
			for _, tc := range []struct {
				query string
				id    int64
			}{
				{"Apotheke München", 1}, {"Apotheke Mu\u0308nchen", 1}, {"München Apotheke", 1},
				{"München Tankstelle", 2}, {"Krankenhaus München", 3},
				{"Supermarkt München", 4}, {"München Feuerwehr", 5},
				{"Feuerwehr Feuerwehr München", 5}, {"Apothekenstraße München", 7},
			} {
				got, err := reader.search(t.Context(), tc.query, 20)
				if err != nil || len(got) != 1 || got[0].ID != tc.id {
					t.Errorf("%q: %+v, %v", tc.query, got, err)
				}
			}
		})
	}
}

func TestDiskSearchLongTermsVerifyFullQuery(t *testing.T) {
	// The second prefix ends inside a three-byte rune at the byte limit.
	for _, prefix := range []string{strings.Repeat("a", searchKeyBytes), "a" + strings.Repeat("東", 32)} {
		path := writeSearchCatalog(t, []SearchResult{
			{ID: 1, Label: prefix + "alpha"},
			{ID: 2, Label: prefix + "beta"},
		})
		compact := compactSearchCatalog(t, path)
		for _, scenario := range []struct{ name, path string }{{"legacy", path}, {"compact", compact}, {"compressed", compressedSearchCatalog(t, compact)}} {
			t.Run(scenario.name+"/"+prefix, func(t *testing.T) {
				reader, err := openSearch(scenario.path)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.close()
				for _, tc := range []struct {
					suffix string
					count  int
					id     int64
				}{{"", 2, 1}, {"alp", 1, 1}, {"beta", 1, 2}, {"missing", 0, 0}} {
					got, err := reader.search(t.Context(), prefix+tc.suffix, 20)
					if err != nil || len(got) != tc.count || len(got) > 0 && got[0].ID != tc.id {
						t.Errorf("suffix %q: %+v, %v", tc.suffix, got, err)
					}
				}
			})
		}
	}
}
