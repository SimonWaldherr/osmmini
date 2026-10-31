package main

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

func legacyPOINormalize(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "'", "", "’", "", "‘", "", "-", " ", ",", " ", ".", " ").Replace(s)
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			out = append(out, r)
		}
	}
	return strings.TrimSpace(string(out))
}

func TestPOINormalizationMatchesLegacy(t *testing.T) {
	random := rand.New(rand.NewSource(42))
	alphabet := []rune("abc XYZÄÖÜäöüßẞéİΣς中😀123-'’‘,. /\t\n")
	for i := 0; i < 1000; i++ {
		value := make([]rune, random.Intn(80))
		for j := range value {
			value[j] = alphabet[random.Intn(len(alphabet))]
		}
		input := string(value)
		if got, want := normalizeForCompare(input), legacyPOINormalize(input); got != want {
			t.Fatalf("%q: got %q, want %q", input, got, want)
		}
	}
}

func TestPOISearchGeometryAndRanking(t *testing.T) {
	s := &server{poiNodes: map[int64]osmmini.Coord{1: {Lat: 48, Lon: 12}, 2: {Lat: 50, Lon: 14}},
		poiWays: map[int64]osmmini.Way{
			10: {ID: 10, NodeIDs: []int64{1, 2, 99}, Tags: osmmini.Tags{"name": "Müller Café"}},
			11: {ID: 11, NodeIDs: []int64{99}, Tags: osmmini.Tags{"name": "Müller Café"}},
		}, poiTaggedNodes: map[int64]osmmini.Node{12: {ID: 12, Lat: 48, Lon: 12, Tags: osmmini.Tags{"name": "Müller Café Nord"}}}}
	got := s.searchPOIMatches("Mueller", 1)
	if len(got) != 1 || got[0].ID != 10 || got[0].Lat != 49 || got[0].Lon != 13 {
		t.Fatalf("unexpected result: %#v", got)
	}
	if got := s.searchPOIMatches("...", 10); len(got) != 0 {
		t.Fatalf("punctuation matched: %#v", got)
	}
}

func TestStreamingPOICacheRoundTripAndAtomicFailure(t *testing.T) {
	s := &server{}
	path := filepath.Join(t.TempDir(), "poi.json")
	nodes := map[int64]osmmini.Coord{1: {Lat: 48, Lon: 12}}
	tagged := map[int64]osmmini.Node{1: {ID: 1, Lat: 48, Lon: 12, Tags: osmmini.Tags{"name": "Müller & Söhne"}}}
	ways := map[int64]osmmini.Way{2: {ID: 2, NodeIDs: []int64{1}, Tags: osmmini.Tags{"amenity": "cafe"}}}
	rels := map[int64]osmmini.Relation{3: {ID: 3, Members: []osmmini.Member{{ID: 2, Type: osmmini.MemberWay}}}}
	if err := s.savePOICache(path, nodes, tagged, ways, rels); err != nil {
		t.Fatal(err)
	}
	got, err := readPOICache(path)
	if err != nil {
		t.Fatal(err)
	}
	want := poiCachePayload{Version: poiCacheVersion, Nodes: nodes, TaggedNodes: tagged, Ways: ways, Rels: rels}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
	before, _ := os.ReadFile(path)
	if !json.Valid(before) {
		t.Fatal("invalid JSON cache")
	}
	if err := s.savePOICache(path, map[int64]osmmini.Coord{1: {Lat: math.NaN()}}, nil, nil, nil); err == nil {
		t.Fatal("expected encoding failure")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("failed save replaced valid cache")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
	if err := os.WriteFile(path, append(before, []byte("{}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPOICache(path); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func BenchmarkPOINormalization(b *testing.B) {
	for _, variant := range []struct {
		name string
		fn   func(string) string
	}{{"before", legacyPOINormalize}, {"after", normalizeForCompare}} {
		b.Run(variant.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = variant.fn("Müller Café — Hauptstraße 5, München")
			}
		})
	}
}

func BenchmarkPOISearch5000(b *testing.B) {
	s := &server{poiTaggedNodes: make(map[int64]osmmini.Node)}
	for i := int64(0); i < 5000; i++ {
		s.poiTaggedNodes[i] = osmmini.Node{ID: i, Lat: 48, Lon: 12, Tags: osmmini.Tags{"name": "Waldgebiet", "landuse": "forest"}}
	}
	s.poiTaggedNodes[5000] = osmmini.Node{ID: 5000, Lat: 48, Lon: 12, Tags: osmmini.Tags{"name": "Müller Café", "amenity": "cafe", "addr:street": "Hauptstraße"}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(s.searchPOIMatches("Mueller", 6)) != 1 {
			b.Fatal("missing POI")
		}
	}
}

func TestStreamingPOICacheReadsLegacyMetadataAndRejectsTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poi.json")
	valid := `{"extra":{"values":[1,{"nested":true},null]},"version":3,"nodes":{"1":{"lat":48,"lon":12}},"tagged_nodes":null,"ways":{},"rels":{}}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readPOICache(path)
	if err != nil || got.Nodes[1].Lat != 48 {
		t.Fatalf("legacy cache: %#v, %v", got, err)
	}
	for _, invalid := range []string{valid[:len(valid)-1], `{"version":2}`, `{"version":3,"nodes":{"not-an-id":{}}}`} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readPOICache(path); err == nil {
			t.Fatalf("accepted invalid cache: %s", invalid)
		}
	}
}
