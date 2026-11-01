package mobile

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A synthetic regional catalog keeps performance measurements independent of PBF
// decoding and the operating system's file cache for large map packages.
func writeSearchCatalog(tb testing.TB, results []SearchResult) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "search.jsonl")
	var data []byte
	var postings []searchPosting
	for _, result := range results {
		result.Text = normalized(result.Text + " " + result.Label + " " + result.Category)
		b, err := json.Marshal(result)
		if err != nil {
			tb.Fatal(err)
		}
		b = append(b, '\n')
		seen := map[string]bool{}
		for _, word := range strings.Fields(result.Text) {
			if len(word) > searchKeyBytes {
				word = word[:searchKeyBytes]
			}
			if !seen[word] {
				postings = append(postings, searchPosting{word, uint64(len(data)), uint32(len(b))})
				seen[word] = true
			}
		}
		data = append(data, b...)
	}
	sort.Slice(postings, func(i, j int) bool {
		if postings[i].term != postings[j].term {
			return postings[i].term < postings[j].term
		}
		return postings[i].offset < postings[j].offset
	})
	index := make([]byte, len(postings)*searchIndexBytes)
	for i, posting := range postings {
		b := index[i*searchIndexBytes:][:searchIndexBytes]
		copy(b, posting.term)
		binary.LittleEndian.PutUint64(b[96:], posting.offset)
		binary.LittleEndian.PutUint32(b[104:], posting.length)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(path+".idx", index, 0o600); err != nil {
		tb.Fatal(err)
	}
	return path
}

func BenchmarkMobileSearch(b *testing.B) {
	results := make([]SearchResult, 20_000)
	for i := range results {
		results[i] = SearchResult{ID: int64(i + 1), Kind: "node", Label: fmt.Sprintf("Apotheke München %05d", i), Category: "pharmacy"}
	}
	path := writeSearchCatalog(b, results)
	for _, format := range []string{"legacy", "compact", "compressed"} {
		catalog := path
		if format != "legacy" {
			catalog = compactSearchCatalog(b, path)
		}
		if format == "compressed" {
			catalog = compressedSearchCatalog(b, catalog)
		}
		s, err := openSearch(catalog)
		if err != nil {
			b.Fatal(err)
		}
		defer s.close()
		b.Run(format, func(b *testing.B) {
			for _, query := range []string{"Apotheke", "munch 12345", "missing"} {
				b.Run(query, func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						if _, err := s.search(context.Background(), query, 20); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}

func BenchmarkMobileNormalize(b *testing.B) {
	for _, input := range []string{"already clean cafe 42", "Café Müller · München Hauptstraße 7"} {
		b.Run(input, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = normalized(input)
			}
		})
	}
}

// Import a region once and set OSMMINI_REAL_REGION to measure disk-backed
// prefix and multiword searches without including import or region validation.
func BenchmarkRealRegionSearch(b *testing.B) {
	region := os.Getenv("OSMMINI_REAL_REGION")
	if region == "" {
		b.Skip("set OSMMINI_REAL_REGION to a prepared offline region")
	}
	search, err := openSearch(filepath.Join(region, "search.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	defer search.close()
	for _, query := range []string{"Passau", "Haupt", "Hauptstrasse 12", "Hauptstrasse Passau", "Landshut Apotheke", "missing"} {
		b.Run(query, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := search.search(context.Background(), query, 20); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestRealRegionSearchResults(t *testing.T) {
	region := os.Getenv("OSMMINI_REAL_REGION")
	if region == "" {
		t.Skip("set OSMMINI_REAL_REGION to a prepared offline region")
	}
	search, err := openSearch(filepath.Join(region, "search.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer search.close()
	for _, query := range []string{"", "Passau", "Haupt", "Hauptstrasse 12", "Hauptstrasse Passau", "Landshut Apotheke", "Apotheke", "Tankstelle", "cafe", "strasse", "strasse 12", "Deggendorf", "Straubing", "Bad Griesbach", "missing"} {
		for _, limit := range []int{1, 20, 100} {
			results, err := search.search(t.Context(), query, limit)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(results)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%q/%d: %d results, %x", query, limit, len(results), sha256.Sum256(encoded))
		}
	}
}

// Prefixes can reach one record through many different terms. The second word
// occurs in JSON field names, but not in the searchable text, forcing decoding.
func BenchmarkMobileSearchDuplicatePrefixes(b *testing.B) {
	results := make([]SearchResult, 512)
	for i := range results {
		results[i] = SearchResult{ID: int64(i + 1), Kind: "node", Label: "cafe cafeteria cake camel camera camp canal candy"}
	}
	path := writeSearchCatalog(b, results)
	compact := compactSearchCatalog(b, path)
	for _, scenario := range []struct{ name, path string }{{"legacy", path}, {"compact", compact}, {"compressed", compressedSearchCatalog(b, compact)}} {
		b.Run(scenario.name, func(b *testing.B) {
			reader, err := openSearch(scenario.path)
			if err != nil {
				b.Fatal(err)
			}
			defer reader.close()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, err := reader.search(context.Background(), "ca x", 20)
				if err != nil || len(got) != 0 {
					b.Fatalf("results = %v, %v", got, err)
				}
			}
		})
	}
}
