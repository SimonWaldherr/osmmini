package mobile

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func engineWithPlaceCatalog(t *testing.T, path string) *Engine {
	t.Helper()
	reader, err := openSearch(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.close)
	catalog, err := loadPlaceCatalog(t.Context(), reader)
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{search: reader, places: catalog.labels, settlements: catalog.exact}
}

func TestEngineSearchExactSettlementPrecedesEarlierPOIsBeforeLimit(t *testing.T) {
	var records []SearchResult
	for i := range 120 {
		records = append(records, SearchResult{
			ID: int64(i + 1), Kind: "node", Label: fmt.Sprintf("Bank %03d · Passau", i), Category: "bank",
		})
	}
	city := SearchResult{
		ID: 9999, Kind: "node", Label: "Passau", Category: "city", Place: "city",
		Coordinate: osm.Coord{Lat: 48.5748, Lon: 13.4609},
	}
	records = append(records, city)
	legacy := writeSearchCatalog(t, records)
	for _, format := range []string{"legacy", "compact", "compressed"} {
		t.Run(format, func(t *testing.T) {
			path := legacy
			if format != "legacy" {
				path = compactSearchCatalog(t, path)
			}
			if format == "compressed" {
				path = compressedSearchCatalog(t, path)
			}
			engine := engineWithPlaceCatalog(t, path)
			for _, limit := range []int{-1, 0, 1, 2, 20, 100, 101} {
				wantCount := limit
				if wantCount < 1 || wantCount > 100 {
					wantCount = 20
				}
				got, err := engine.Search(t.Context(), "  PASSAU  ", limit)
				if err != nil || len(got) != wantCount || got[0] != city {
					t.Fatalf("limit %d: got %+v, %v; want city first and %d rows", limit, got, err, wantCount)
				}
				for i, result := range got[1:] {
					if result.ID != int64(i+1) || result.Kind != "node" || result.Text != "" {
						t.Fatalf("limit %d changed remaining result order: %+v", limit, got)
					}
				}
			}
		})
	}
}

func TestEngineSearchExactSettlementDeduplicatesKindAndIDAndCopiesRows(t *testing.T) {
	engine := engineWithPlaceCatalog(t, writeSearchCatalog(t, []SearchResult{
		{ID: 10, Kind: "node", Label: "Passau Café", Category: "cafe"},
		{ID: 20, Kind: "node", Label: "Passau", Category: "city", Details: &PlaceDetails{Website: "https://example.org"}},
		{ID: 20, Kind: "way", Label: "Passau Bank", Category: "bank"},
		{ID: 30, Kind: "node", Label: "Passau Hotel", Category: "hotel"},
	}))
	want := []struct {
		kind string
		id   int64
	}{{"node", 20}, {"node", 10}, {"way", 20}, {"node", 30}}
	for range 2 {
		got, err := engine.Search(t.Context(), "Passau", 4)
		if err != nil || len(got) != len(want) {
			t.Fatalf("search = %+v, %v", got, err)
		}
		for i, result := range got {
			if result.Kind != want[i].kind || result.ID != want[i].id || result.Text != "" {
				t.Fatalf("result %d = %+v, want %v", i, result, want[i])
			}
		}
		if got[0].Label != "Passau" {
			t.Fatal("caller mutation changed immutable place lookup")
		}
		if got[0].Details == nil || got[0].Details.Website != "https://example.org" {
			t.Fatal("caller mutation changed immutable place details")
		}
		got[0].Label = "Changed by caller"
		got[0].Details.Website = "Changed by caller"
	}
}

func TestEngineSearchPreservesPrefixANDAndCategoryAliasQueries(t *testing.T) {
	records := []SearchResult{
		{ID: 1, Kind: "node", Label: "Bank · Bad Birnbach", Category: "bank"},
		{ID: 2, Kind: "node", Label: "Bad Birnbach", Category: "town"},
		{ID: 3, Kind: "node", Label: "Passau Süd", Category: "bank"},
		{ID: 4, Kind: "node", Label: "Passau", Category: "city"},
	}
	aliases := []struct{ query, category string }{
		{"Apotheke", "pharmacy"}, {"Tankstelle", "fuel"}, {"Krankenhaus", "hospital"},
		{"Supermarkt", "supermarket"}, {"Feuerwehr", "fire_station"},
	}
	for i, alias := range aliases {
		records = append(records,
			SearchResult{ID: int64(10 + i), Kind: "node", Label: alias.query, Category: "village"},
			SearchResult{ID: int64(20 + i), Kind: "node", Label: "POI", Category: alias.category},
		)
	}
	engine := engineWithPlaceCatalog(t, writeSearchCatalog(t, records))
	queries := []string{"", "missing", "pass", "Bad Birn", "Birnbach Bad", "Bad Bank"}
	for _, alias := range aliases {
		queries = append(queries, alias.query)
	}
	for _, query := range queries {
		for _, limit := range []int{1, 20, 100} {
			want, err := engine.search.search(t.Context(), query, limit)
			if err != nil {
				t.Fatal(err)
			}
			got, err := engine.Search(t.Context(), query, limit)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("%q limit %d changed: %+v, %v; want %+v", query, limit, got, err, want)
			}
		}
	}
	got, err := engine.Search(t.Context(), "bad birnbach", 1)
	if err != nil || len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("exact multiword name = %+v, %v", got, err)
	}
}

func TestExactPlaceLookupPrioritizesSettlementClassAndBoundsDuplicateNames(t *testing.T) {
	var records []SearchResult
	for i := range 150 {
		records = append(records, SearchResult{ID: int64(200 - i), Kind: "node", Label: "Neustadt", Category: "village"})
	}
	records = append(records,
		SearchResult{ID: 1000, Kind: "node", Label: "Neustadt", Category: "city"},
		SearchResult{ID: 999, Kind: "node", Label: "Neustadt", Category: "restaurant", Place: "town"},
		SearchResult{ID: 1001, Kind: "node", Label: "Neustadt", Category: "village", Place: "continent"},
	)
	var first []SearchResult
	for range 2 {
		engine := engineWithPlaceCatalog(t, writeSearchCatalog(t, records))
		matches := engine.settlements.matches("neustadt")
		if len(matches) != 100 || matches[0].ID != 1000 || matches[1].ID != 999 {
			t.Fatalf("bounded exact-name priority = %+v", matches)
		}
		for i, result := range matches[2:] {
			if result.ID != int64(51+i) {
				t.Fatalf("village priority depends on source order: %+v", matches)
			}
		}
		got, err := engine.Search(t.Context(), "Neustadt", 100)
		if err != nil || !reflect.DeepEqual(got, matches) {
			t.Fatalf("limited exact search = %+v, %v; want %+v", got, err, matches)
		}
		if first != nil && !reflect.DeepEqual(got, first) {
			t.Fatal("reversing source records changed deterministic settlement order")
		}
		first = got
		slices.Reverse(records)
	}
}

type cancelExactPlaceContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (ctx *cancelExactPlaceContext) Err() error {
	ctx.checks++
	if ctx.checks == 2 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestEngineExactSettlementSearchCancellationDoesNotReturnPartialRows(t *testing.T) {
	engine := engineWithPlaceCatalog(t, writeSearchCatalog(t, []SearchResult{
		{ID: 1, Kind: "node", Label: "Großköllnbach", Category: "village"},
	}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := engine.Search(ctx, "grosskollnbach", 1); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("pre-cancelled exact search = %+v, %v", got, err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	during := &cancelExactPlaceContext{Context: ctx, cancel: cancel}
	if got, err := engine.Search(during, "grosskollnbach", 1); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("exact search cancelled before return = %+v, %v", got, err)
	}
	if got, err := engine.Search(t.Context(), "grosskollnbach", 1); err != nil || len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("cancellation changed subsequent search = %+v, %v", got, err)
	}
	engine.closed = true
	if got, err := engine.Search(t.Context(), "grosskollnbach", 1); err == nil || got != nil {
		t.Fatalf("closed region returned cached exact rows = %+v, %v", got, err)
	}
}

func TestEngineExactSettlementSearchPropagatesDiskErrorsWithoutPartialRows(t *testing.T) {
	engine := engineWithPlaceCatalog(t, writeSearchCatalog(t, []SearchResult{
		{ID: 1, Kind: "node", Label: "Bank · Passau", Category: "bank"},
		{ID: 2, Kind: "node", Label: "Passau", Category: "city"},
	}))
	if err := engine.search.data.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := engine.Search(t.Context(), "Passau", 2); err == nil || got != nil {
		t.Fatalf("failed remaining search returned partial rows: %+v, %v", got, err)
	}
}
