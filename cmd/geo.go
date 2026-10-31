package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

const poiGeoCellSize = 0.05

type geoPOI struct {
	ID    int64
	Kind  string
	Coord osmmini.Coord
	Tags  osmmini.Tags
}
type poiGeoIndex struct {
	entries []geoPOI
	cells   map[[2]int][]int
}

func geoCell(c osmmini.Coord) [2]int {
	return [2]int{int(math.Floor(c.Lon / poiGeoCellSize)), int(math.Floor(c.Lat / poiGeoCellSize))}
}
func validGeoPoint(c osmmini.Coord) bool {
	return !math.IsNaN(c.Lat) && !math.IsNaN(c.Lon) && c.Lat >= -90 && c.Lat <= 90 && c.Lon >= -180 && c.Lon <= 180
}

func buildPOIGeoIndex(nodes map[int64]osmmini.Coord, tagged map[int64]osmmini.Node, ways map[int64]osmmini.Way) *poiGeoIndex {
	index := &poiGeoIndex{entries: make([]geoPOI, 0, len(tagged)+len(ways)), cells: make(map[[2]int][]int)}
	add := func(p geoPOI) {
		if !validGeoPoint(p.Coord) {
			return
		}
		key := geoCell(p.Coord)
		index.cells[key] = append(index.cells[key], len(index.entries))
		index.entries = append(index.entries, p)
	}
	for _, n := range tagged {
		add(geoPOI{n.ID, "node", osmmini.Coord{Lat: n.Lat, Lon: n.Lon}, n.Tags})
	}
	for _, w := range ways {
		var c osmmini.Coord
		count := 0
		for _, id := range w.NodeIDs {
			if v, ok := nodes[id]; ok {
				c.Lat += v.Lat
				c.Lon += v.Lon
				count++
			}
		}
		if count > 0 {
			c.Lat /= float64(count)
			c.Lon /= float64(count)
			add(geoPOI{w.ID, "poi", c, w.Tags})
		}
	}
	return index
}

// Broad/global windows scan entries instead of iterating millions of empty
// cells. Small windows touch only their occupied spatial buckets.
func (index *poiGeoIndex) visit(ctx context.Context, box osmmini.CoordWindow, fn func(geoPOI)) error {
	lo := geoCell(osmmini.Coord{Lat: box.MinLat, Lon: box.MinLon})
	hi := geoCell(osmmini.Coord{Lat: box.MaxLat, Lon: box.MaxLon})
	count := int64(hi[0]-lo[0]+1) * int64(hi[1]-lo[1]+1)
	if count > int64(len(index.cells))*2 {
		for i, p := range index.entries {
			if i%256 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if box.Contains(p.Coord) {
				fn(p)
			}
		}
	} else {
		for x := lo[0]; x <= hi[0]; x++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			for y := lo[1]; y <= hi[1]; y++ {
				for _, i := range index.cells[[2]int{x, y}] {
					p := index.entries[i]
					if box.Contains(p.Coord) {
						fn(p)
					}
				}
			}
		}
	}
	return ctx.Err()
}

type geoFeature struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Geometry struct {
		Type        string     `json:"type"`
		Coordinates [2]float64 `json:"coordinates"`
	} `json:"geometry"`
	Properties map[string]any `json:"properties"`
}
type geoHit struct {
	poi      geoPOI
	distance float64
}

// radiusWindows handles poles and the antimeridian; final inclusion uses
// great-circle distance, not the bounding boxes alone.
func radiusWindows(c osmmini.Coord, radius float64) []osmmini.CoordWindow {
	delta := radius / 6371000
	lat := c.Lat * math.Pi / 180
	box := osmmini.CoordWindow{MinLat: math.Max(-90, c.Lat-delta*180/math.Pi), MaxLat: math.Min(90, c.Lat+delta*180/math.Pi), MinLon: -180, MaxLon: 180}
	if lat-delta <= -math.Pi/2 || lat+delta >= math.Pi/2 {
		return []osmmini.CoordWindow{box}
	}
	span := math.Asin(math.Min(1, math.Sin(delta)/math.Cos(lat))) * 180 / math.Pi
	box.MinLon = c.Lon - span
	box.MaxLon = c.Lon + span
	if box.MinLon < -180 {
		other := box
		other.MinLon = box.MinLon + 360
		other.MaxLon = 180
		box.MinLon = -180
		return []osmmini.CoordWindow{box, other}
	}
	if box.MaxLon > 180 {
		other := box
		other.MinLon = -180
		other.MaxLon = box.MaxLon - 360
		box.MaxLon = 180
		return []osmmini.CoordWindow{box, other}
	}
	return []osmmini.CoordWindow{box}
}

func (s *server) handleGeoPOIs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, 405, "method not allowed")
		return
	}
	query := r.URL.Query()
	var center *osmmini.Coord
	var radius float64
	var boxes []osmmini.CoordWindow
	if query.Get("bbox") != "" {
		if query.Has("lat") || query.Has("lon") || query.Has("radius_m") {
			writeJSONError(w, 400, "use bbox or lat/lon/radius_m, not both")
			return
		}
		box, ok := offlineLabelsWindow(query.Get("bbox"))
		if !ok {
			writeJSONError(w, 400, "invalid bbox")
			return
		}
		boxes = []osmmini.CoordWindow{box}
	} else {
		lat, e1 := strconv.ParseFloat(query.Get("lat"), 64)
		lon, e2 := strconv.ParseFloat(query.Get("lon"), 64)
		var e3 error
		radius, e3 = strconv.ParseFloat(query.Get("radius_m"), 64)
		c := osmmini.Coord{Lat: lat, Lon: lon}
		if e1 != nil || e2 != nil || e3 != nil || !validGeoPoint(c) || math.IsNaN(radius) || radius <= 0 || radius > 100000 {
			writeJSONError(w, 400, "lat/lon and radius_m in (0,100000] are required")
			return
		}
		center = &c
		boxes = radiusWindows(c, radius)
	}
	limit := parseLimit(query.Get("limit"), 100, 1, 1000)
	normalized := normalizeForCompare(query.Get("q"))
	tokens := strings.Fields(normalized)
	category := strings.TrimSpace(query.Get("category"))
	knownCategory := lookupPOICategory(category)
	s.poiMu.RLock()
	index := s.poiGeo
	s.poiMu.RUnlock()
	if index == nil {
		writeJSONError(w, 503, "POI index is not ready")
		return
	}
	hits := make([]geoHit, 0, limit)
	matches := 0
	before := func(a, b geoHit) bool {
		if a.distance != b.distance {
			return a.distance < b.distance
		}
		if a.poi.Kind != b.poi.Kind {
			return a.poi.Kind < b.poi.Kind
		}
		return a.poi.ID < b.poi.ID
	}
	consider := func(p geoPOI) {
		if s.window != nil && !s.window.Contains(p.Coord) {
			return
		}
		if knownCategory != nil {
			if !knownCategory.matches(p.Tags) {
				return
			}
		} else if category != "" && !matchesPOICategory(p.Tags, category) {
			return
		}

		d := 0.0
		if center != nil {
			d = haversineMeters(center.Lat, center.Lon, p.Coord.Lat, p.Coord.Lon)
			if d > radius {
				return
			}
		}
		if normalized != "" && scorePOIResult(p.Tags, normalized, tokens) <= 0 {
			return
		}
		matches++
		hit := geoHit{p, d}
		if len(hits) == limit && !before(hit, hits[len(hits)-1]) {
			return
		}
		pos := sort.Search(len(hits), func(i int) bool { return before(hit, hits[i]) })
		if len(hits) < limit {
			hits = append(hits, geoHit{})
		}
		copy(hits[pos+1:], hits[pos:len(hits)-1])
		hits[pos] = hit
	}
	for _, box := range boxes {
		if err := index.visit(r.Context(), box, consider); err != nil {
			return
		}
	}
	features := make([]geoFeature, 0, len(hits))
	for _, hit := range hits {
		p := hit.poi
		f := geoFeature{Type: "Feature", ID: p.Kind + "/" + strconv.FormatInt(p.ID, 10), Properties: map[string]any{"osm_id": p.ID, "kind": p.Kind, "label": formatSearchResultLabel(p.Kind, p.Tags), "category": primarySearchCategory(p.Tags)}}
		f.Geometry.Type = "Point"
		f.Geometry.Coordinates = [2]float64{p.Coord.Lon, p.Coord.Lat}
		if center != nil {
			f.Properties["distance_m"] = hit.distance
		}
		features = append(features, f)
	}
	writeGeoJSON(w, map[string]any{"type": "FeatureCollection", "features": features, "matched": matches, "truncated": matches > len(features)})
}

func writeGeoJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/geo+json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func (s *server) handleGeoMeasure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, 405, "method not allowed")
		return
	}
	var request struct {
		Coordinates [][]float64 `json:"coordinates"`
	}
	if err := readJSON(w, r, &request, 1<<20); err != nil {
		writeJSONError(w, 400, "invalid measurement JSON")
		return
	}
	if len(request.Coordinates) < 2 || len(request.Coordinates) > 10000 {
		writeJSONError(w, 400, "provide 2 to 10000 [longitude,latitude] positions")
		return
	}
	points := make([]osmmini.Coord, len(request.Coordinates))
	for i, c := range request.Coordinates {
		if len(c) != 2 {
			writeJSONError(w, 400, "each position must contain longitude and latitude")
			return
		}
		points[i] = osmmini.Coord{Lat: c[1], Lon: c[0]}
		if !validGeoPoint(points[i]) {
			writeJSONError(w, 400, "invalid coordinate")
			return
		}
	}
	total := 0.0
	segments := make([]float64, len(points)-1)
	for i := 1; i < len(points); i++ {
		if i%256 == 0 && r.Context().Err() != nil {
			return
		}
		a, b := points[i-1], points[i]
		segments[i-1] = haversineMeters(a.Lat, a.Lon, b.Lat, b.Lon)
		total += segments[i-1]
	}
	writeJSON(w, 200, map[string]any{"length_m": total, "segments_m": segments, "model": "sphere", "earth_radius_m": 6371000})
}
