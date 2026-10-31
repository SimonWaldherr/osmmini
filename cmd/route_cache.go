package main

import (
	"slices"
	"sync"
	"time"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

// ---- Route result cache ----

// routeCacheKey uniquely identifies a route request for caching purposes.
type routeCacheKey struct {
	fromNode int64
	toNode   int64
	// include routing-relevant options in the key
	engine              string
	objective           string
	profile             string
	pro                 bool
	emergency           bool
	leftTurn            float64
	rightTurn           float64
	uTurn               float64
	crossing            float64
	maxSpeed            float64
	heightM             float64
	weightT             float64
	noLeft              bool
	trafficLightPenalty float64
}

// routeCacheEntry holds a cached route response with its expiry time.
type routeCacheEntry struct {
	resp      RouteResponse
	expiresAt time.Time
}

// routeCacheDefaultMaxItems is the default capacity bound for the route cache.
const routeCacheDefaultMaxItems = 4096

// RouteCache is a bounded in-memory cache for route responses.
// It uses a simple RW-mutex protected map with TTL eviction.
// Maximum capacity is capped so it never grows unbounded.
type RouteCache struct {
	mu        sync.RWMutex
	entries   map[routeCacheKey]*routeCacheEntry
	ttl       time.Duration
	maxItems  int
	done      chan struct{}
	closeOnce sync.Once
}

func newRouteCache(ttl time.Duration, maxItems int) *RouteCache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if maxItems <= 0 {
		maxItems = routeCacheDefaultMaxItems
	}
	c := &RouteCache{
		entries:  make(map[routeCacheKey]*routeCacheEntry, 64),
		ttl:      ttl,
		maxItems: maxItems,
		done:     make(chan struct{}),
	}
	// Background eviction goroutine: purge expired entries every TTL/2.
	go func() {
		ticker := time.NewTicker(max(ttl/2, time.Nanosecond))
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.evict()
			case <-c.done:
				return
			}
		}
	}()
	return c
}

// Close stops background expiry maintenance. It is safe to call more than once.
func (c *RouteCache) Close() {
	c.closeOnce.Do(func() { close(c.done) })
}

func (c *RouteCache) cacheKey(fromNode, toNode int64, opt osmmini.RouteOptions) routeCacheKey {
	return routeCacheKey{
		fromNode:            fromNode,
		toNode:              toNode,
		engine:              string(opt.Engine),
		objective:           string(opt.Objective),
		profile:             string(opt.Profile),
		pro:                 opt.Pro,
		emergency:           opt.EmergencyMode,
		leftTurn:            opt.Weights.LeftTurn,
		rightTurn:           opt.Weights.RightTurn,
		uTurn:               opt.Weights.UTurn,
		crossing:            opt.Weights.Crossing,
		maxSpeed:            opt.Weights.MaxSpeedKph,
		heightM:             opt.Weights.VehicleHeightM,
		weightT:             opt.Weights.VehicleWeightT,
		noLeft:              opt.Weights.NoLeftTurn,
		trafficLightPenalty: opt.Weights.TrafficLightPenalty,
	}
}

func (c *RouteCache) get(key routeCacheKey) (RouteResponse, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return RouteResponse{}, false
	}
	return e.resp, true
}

func (c *RouteCache) set(key routeCacheKey, resp RouteResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// When at capacity, evict all entries that are already expired.
	// If still at capacity afterwards, remove entries with the earliest
	// expiry time to make room (approximate LRU via expiry ordering).
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxItems {
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		// If still full, remove the half with the soonest expiry.
		if len(c.entries) >= c.maxItems {
			// Only deadlines are needed to select the oldest half. Copying the
			// full routing-option key for sorting multiplies temporary storage.
			expiries := make([]time.Time, 0, len(c.entries))
			for _, e := range c.entries {
				expiries = append(expiries, e.expiresAt)
			}
			slices.SortFunc(expiries, time.Time.Compare)
			remaining := max(1, len(expiries)/2)
			cutoff := expiries[remaining-1]
			for k, e := range c.entries {
				if e.expiresAt.Before(cutoff) {
					delete(c.entries, k)
					remaining--
				}
			}
			// Equal deadlines can straddle the halfway point. Remove only the
			// required number, after all strictly older entries are gone.
			for k, e := range c.entries {
				if remaining == 0 {
					break
				}
				if e.expiresAt.Equal(cutoff) {
					delete(c.entries, k)
					remaining--
				}
			}
		}
	}
	c.entries[key] = &routeCacheEntry{resp: resp, expiresAt: time.Now().Add(c.ttl)}
}

func (c *RouteCache) evict() {
	now := time.Now()
	c.mu.Lock()
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

func (c *RouteCache) Invalidate() {
	c.mu.Lock()
	c.entries = make(map[routeCacheKey]*routeCacheEntry, 64)
	c.mu.Unlock()
}

// Size returns the current number of cached entries.
func (c *RouteCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
