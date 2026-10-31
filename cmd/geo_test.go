package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

func geoTestServer() *server {
	tagged := map[int64]osmmini.Node{
		1: {ID: 1, Lat: 48, Lon: 12, Tags: osmmini.Tags{"name": "Cafe A", "amenity": "cafe"}},
		2: {ID: 2, Lat: 48.01, Lon: 12, Tags: osmmini.Tags{"name": "Cafe B", "amenity": "cafe"}},
		3: {ID: 3, Lat: 48, Lon: 12.01, Tags: osmmini.Tags{"name": "Apotheke", "amenity": "pharmacy"}},
		4: {ID: 4, Lat: 0, Lon: -179.99, Tags: osmmini.Tags{"name": "Dateline"}},
		5: {ID: 5, Lat: 89.99, Lon: 150, Tags: osmmini.Tags{"name": "Polar"}},
	}
	return &server{poiGeo: buildPOIGeoIndex(nil, tagged, nil)}
}
func TestGeoPOIRadiusFiltersAndGeoJSON(t *testing.T) {
	s := geoTestServer()
	rec := httptest.NewRecorder()
	s.handleGeoPOIs(rec, httptest.NewRequest("GET", "/api/v1/geo/pois?lat=48&lon=12&radius_m=2000&category=cafe&limit=1", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/geo+json; charset=utf-8" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Type      string
		Features  []geoFeature
		Matched   int
		Truncated bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "FeatureCollection" || got.Matched != 2 || !got.Truncated || len(got.Features) != 1 || got.Features[0].ID != "node/1" || got.Features[0].Geometry.Coordinates != [2]float64{12, 48} {
		t.Fatalf("unexpected GeoJSON: %#v", got)
	}
	for _, q := range []string{"lat=0&lon=179.99&radius_m=3000", "lat=90&lon=0&radius_m=3000"} {
		rec := httptest.NewRecorder()
		s.handleGeoPOIs(rec, httptest.NewRequest("GET", "/?"+q, nil))
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Matched != 1 {
			t.Fatalf("wrapped/polar query %s: %s", q, rec.Body.String())
		}
	}
}
func TestGeoPOIBBoxAndInputValidation(t *testing.T) {
	s := geoTestServer()
	for _, q := range []string{"bbox=11.99,47.99,12.005,48.02&q=Cafe", "bbox=11.99,47.99,12.005,48.02"} {
		rec := httptest.NewRecorder()
		s.handleGeoPOIs(rec, httptest.NewRequest("GET", "/?"+q, nil))
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
	}
	for _, q := range []string{"", "lat=NaN&lon=12&radius_m=10", "lat=48&lon=12&radius_m=100001", "bbox=0,0,1,1&lat=0", "bbox=NaN,0,1,1", "bbox=1,1,0,0"} {
		rec := httptest.NewRecorder()
		s.handleGeoPOIs(rec, httptest.NewRequest("GET", "/?"+q, nil))
		if rec.Code != 400 {
			t.Fatalf("%q accepted", q)
		}
	}
	rec := httptest.NewRecorder()
	(&server{}).handleGeoPOIs(rec, httptest.NewRequest("GET", "/?bbox=0,0,1,1", nil))
	if rec.Code != 503 {
		t.Fatal("unloaded index returned success")
	}
}
func TestGeoMeasurementAndValidation(t *testing.T) {
	s := &server{}
	for _, body := range []string{`{"coordinates":[[0,0],[180,0]]}`, `{"coordinates":[[179,0],[-179,0]]}`} {
		rec := httptest.NewRecorder()
		s.handleGeoMeasure(rec, httptest.NewRequest("POST", "/", bytes.NewBufferString(body)))
		var data struct {
			Length float64 `json:"length_m"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		expected := math.Pi * 6371000
		if bytes.Contains([]byte(body), []byte("179")) {
			expected *= 2.0 / 180
		}
		if rec.Code != 200 || math.Abs(data.Length-expected) > 0.001 {
			t.Fatalf("measurement: %s", rec.Body.String())
		}
	}
	for _, body := range []string{`{"coordinates":[[0,0]]}`, `{"coordinates":[[0,0],[0,91]]}`, `{"coordinates":[[0,0],[1,2,3]]}`} {
		rec := httptest.NewRecorder()
		s.handleGeoMeasure(rec, httptest.NewRequest("POST", "/", bytes.NewBufferString(body)))
		if rec.Code != 400 {
			t.Fatalf("accepted %s", body)
		}
	}
}
func TestGeoIndexMatchesScanAndTextFallback(t *testing.T) {
	tagged := make(map[int64]osmmini.Node)
	for i := int64(0); i < 1000; i++ {
		tagged[i] = osmmini.Node{ID: i, Lat: 48 + float64(i%100)*0.01, Lon: 12 + float64(i/100)*0.01, Tags: osmmini.Tags{"name": fmt.Sprintf("Cafe %04d", i)}}
	}
	index := buildPOIGeoIndex(nil, tagged, nil)
	for _, box := range []osmmini.CoordWindow{{MinLat: 48.1, MaxLat: 48.3, MinLon: 12.01, MaxLon: 12.04}, {MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180}} {
		var got, want []int64
		if err := index.visit(context.Background(), box, func(p geoPOI) { got = append(got, p.ID) }); err != nil {
			t.Fatal(err)
		}
		for _, p := range index.entries {
			if box.Contains(p.Coord) {
				want = append(want, p.ID)
			}
		}
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		if !reflect.DeepEqual(got, want) {
			t.Fatal("indexed candidates differ from scan")
		}
	}
	s := &server{poiTaggedNodes: tagged}
	before := s.searchPOIMatches("Cafe", 6)
	s.poiGeo = index
	after := s.searchPOIMatches("Cafe", 6)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("indexed text search changed ranking")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := index.visit(ctx, osmmini.CoordWindow{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180}, func(geoPOI) {}); err == nil {
		t.Fatal("cancelled query accepted")
	}
}
func BenchmarkGeoViewport100k(b *testing.B) {
	tagged := make(map[int64]osmmini.Node, 100000)
	for i := int64(0); i < 100000; i++ {
		tagged[i] = osmmini.Node{ID: i, Lat: 48 + float64(i%1000)*0.001, Lon: 12 + float64(i/1000)*0.01}
	}
	index := buildPOIGeoIndex(nil, tagged, nil)
	box := osmmini.CoordWindow{MinLat: 48.1, MaxLat: 48.11, MinLon: 12.1, MaxLon: 12.11}
	for _, indexed := range []bool{false, true} {
		b.Run(fmt.Sprintf("indexed=%v", indexed), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				count := 0
				if indexed {
					_ = index.visit(context.Background(), box, func(geoPOI) { count++ })
				} else {
					for _, p := range index.entries {
						if box.Contains(p.Coord) {
							count++
						}
					}
				}
				if count == 0 {
					b.Fatal("no candidates")
				}
			}
		})
	}
}
