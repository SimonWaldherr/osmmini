package osmmini

import (
	"strconv"
	"testing"
)

func BenchmarkSearchAddressesTopK(b *testing.B) {
	entries := make([]AddressEntry, 50000)
	for i := range entries {
		entries[i] = AddressEntry{
			ID: int64(i + 1),
			Tags: Tags{
				"addr:street": "Nebenstraße " + strconv.Itoa(i),
				"addr:city":   "Musterstadt",
			},
		}
	}
	// Add a few strong candidates scattered through the source index.
	for _, i := range []int{17, 12_345, 36_789} {
		entries[i].Tags["addr:street"] = "Hauptstraße"
	}
	query := ParseAddressGuess("Hauptstraße Musterstadt")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SearchAddresses(entries, query, 10)
	}
}

func TestNormalizePreservesGermanSearchEquivalence(t *testing.T) {
	tests := map[string]string{
		"Hauptstraße 5": "hauptstrasse5",
		"KÖLN-NORD":     "koelnnord",
		"alreadyclean":  "alreadyclean",
	}
	for input, want := range tests {
		if got := normalize(input); got != want {
			t.Errorf("normalize(%q) = %q, want %q", input, got, want)
		}
	}
}
