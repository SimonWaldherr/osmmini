package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

func TestRouteCacheCapacityAndReplacement(t *testing.T) {
	for _, capacity := range []int{1, 2, 5} {
		c := newRouteCache(time.Hour, capacity)
		defer c.Close()
		for i := range 20 {
			key := routeCacheKey{fromNode: int64(i)}
			c.set(key, RouteResponse{DistanceM: float64(i)})
			if c.Size() > capacity {
				t.Fatalf("capacity %d: size = %d", capacity, c.Size())
			}
			before := c.Size()
			c.set(key, RouteResponse{DistanceM: 42})
			if c.Size() != before {
				t.Fatal("replacing an existing entry evicted other routes")
			}
			if got, ok := c.get(key); !ok || got.DistanceM != 42 {
				t.Fatal("replacement missing")
			}
		}
	}
}

func TestRouteCacheExpiryAndInvalidation(t *testing.T) {
	c := newRouteCache(time.Hour, 3)
	defer c.Close()
	expired, live := routeCacheKey{fromNode: 1}, routeCacheKey{fromNode: 2}
	c.set(expired, RouteResponse{})
	c.set(live, RouteResponse{})
	c.entries[expired].expiresAt = time.Now().Add(-time.Second)
	if _, ok := c.get(expired); ok {
		t.Fatal("expired entry was served")
	}
	c.evict()
	if c.Size() != 1 {
		t.Fatalf("size after expiry = %d", c.Size())
	}
	c.Invalidate()
	if _, ok := c.get(live); ok {
		t.Fatal("invalidated entry was served")
	}
	c.Close() // Also exercise idempotent cleanup with the deferred Close.
}

func TestRouteCacheEvictionPreservesLaterDeadlinesAndBoundsTies(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		offsets []int
		want    map[int]int
	}{
		{name: "one entry", offsets: []int{3}, want: map[int]int{}},
		{name: "ordered", offsets: []int{6, 2, 5, 1, 4, 3}, want: map[int]int{4: 1, 5: 1, 6: 1}},
		{name: "odd capacity", offsets: []int{4, 1, 5, 2, 3}, want: map[int]int{3: 1, 4: 1, 5: 1}},
		{name: "equal deadlines", offsets: []int{3, 3, 3, 3, 3, 3}, want: map[int]int{3: 3}},
		{name: "boundary ties", offsets: []int{1, 2, 2, 2, 3, 3}, want: map[int]int{2: 1, 3: 2}},
		{name: "expired first", offsets: []int{-1, -1, 2, 3, 4, 5}, want: map[int]int{2: 1, 3: 1, 4: 1, 5: 1}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cache := newRouteCache(time.Hour, len(scenario.offsets))
			defer cache.Close()
			now := time.Now()
			for i, offset := range scenario.offsets {
				cache.entries[routeCacheKey{fromNode: int64(i)}] = &routeCacheEntry{
					expiresAt: now.Add(time.Duration(offset) * time.Minute),
				}
			}
			newKey := routeCacheKey{fromNode: int64(len(scenario.offsets))}
			cache.set(newKey, RouteResponse{DistanceM: 42})
			if response, ok := cache.get(newKey); !ok || response.DistanceM != 42 {
				t.Fatal("new route missing after eviction")
			}
			got := map[int]int{}
			for key := range cache.entries {
				if key != newKey {
					got[scenario.offsets[int(key.fromNode)]]++
				}
			}
			if len(got) != len(scenario.want) {
				t.Fatalf("remaining expiry groups = %v, want %v", got, scenario.want)
			}
			for offset, count := range scenario.want {
				if got[offset] != count {
					t.Fatalf("remaining expiry groups = %v, want %v", got, scenario.want)
				}
			}
		})
	}
}

func TestRouteCacheConcurrentAccess(t *testing.T) {
	c := newRouteCache(time.Hour, 8)
	defer c.Close()
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Go(func() {
			for i := range 100 {
				key := routeCacheKey{fromNode: int64(worker*100 + i)}
				c.set(key, RouteResponse{})
				c.get(key)
				c.Size()
				if i%25 == 0 {
					c.Invalidate()
				}
			}
		})
	}
	wg.Wait()
	if c.Size() > 8 {
		t.Fatal("concurrent writes exceeded capacity")
	}
}

func TestRouteCacheHitUsesCurrentCoordinatesInMapLinks(t *testing.T) {
	coords := map[int64]osmmini.Coord{1: {Lat: 48, Lon: 12}, 2: {Lat: 48.01, Lon: 12.01}}
	router := osmmini.NewRouterFromGraph(coords, map[int64][]osmmini.Edge{
		1: {{To: 2, DistM: 1300, SpeedKph: 50, HwyType: "residential"}},
		2: {{To: 1, DistM: 1300, SpeedKph: 50, HwyType: "residential"}},
	})
	c := newRouteCache(time.Hour, 4)
	defer c.Close()
	s := &server{router: router, routeCache: c, settings: NewSettingsStore("", DefaultSettings("", ""))}
	request := func(from, to osmmini.Coord) RouteResponse {
		t.Helper()
		body, err := json.Marshal(map[string]any{"from": from, "to": to})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		s.handleRoute(rec, httptest.NewRequest(http.MethodPost, "/api/v1/route", bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var response RouteResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := request(coords[1], coords[2])
	from, to := osmmini.Coord{Lat: 48.0001, Lon: 12.0001}, osmmini.Coord{Lat: 48.0101, Lon: 12.0101}
	second := request(from, to)
	if first.Cached || !second.Cached {
		t.Fatalf("cache flags = %v, %v", first.Cached, second.Cached)
	}
	if second.From.Lat != from.Lat || second.To.Lon != to.Lon {
		t.Fatal("stale route endpoints")
	}
	points := []osmmini.Coord{from, to}
	if second.GoogleMapsURL != buildGoogleMapsURL(points, 0) || second.AppleMapsURL != buildAppleMapsURL(points) {
		t.Fatal("cache hit returned stale map links")
	}
}

func BenchmarkRouteCacheEviction(b *testing.B) {
	c := newRouteCache(time.Hour, routeCacheDefaultMaxItems)
	defer c.Close()
	expiry := time.Now().Add(time.Hour)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		clear(c.entries)
		for j := range routeCacheDefaultMaxItems {
			c.entries[routeCacheKey{fromNode: int64(j)}] = &routeCacheEntry{expiresAt: expiry.Add(time.Duration(j))}
		}
		b.StartTimer()
		c.set(routeCacheKey{fromNode: int64(routeCacheDefaultMaxItems + i)}, RouteResponse{})
	}
}
