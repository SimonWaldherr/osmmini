package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	osmmini "simonwaldherr.de/go/osmmini"
)

// Route export formats. JSON is the regular API response; GPX and GeoJSON
// are download formats for navigation devices, GIS tools and other map apps.
const (
	routeFormatJSON    = "json"
	routeFormatGPX     = "gpx"
	routeFormatGeoJSON = "geojson"
)

// routeExport is the format-neutral view of a single route or a solved trip.
type routeExport struct {
	Name      string
	DistanceM float64
	DurationS float64
	Engine    string
	Profile   string
	Path      []osmmini.Coord
	Waypoints []routeExportWaypoint
	Steps     []osmmini.Maneuver
}

type routeExportWaypoint struct {
	Role  string // start, stop, end
	Name  string
	Coord osmmini.Coord
}

// parseRouteFormat accepts the optional ?format= query parameter.
func parseRouteFormat(r *http.Request) (string, error) {
	switch f := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))); f {
	case "", routeFormatJSON:
		return routeFormatJSON, nil
	case routeFormatGPX, routeFormatGeoJSON:
		return f, nil
	default:
		return "", fmt.Errorf("unsupported format %q (want json, gpx or geojson)", f)
	}
}

func routeExportFromRoute(resp RouteResponse) routeExport {
	from := firstNonEmpty(resp.From.Label, resp.From.Input, "Start")
	to := firstNonEmpty(resp.To.Label, resp.To.Input, "Ziel")
	return routeExport{
		Name:      from + " → " + to,
		DistanceM: resp.DistanceM,
		DurationS: resp.DurationS,
		Engine:    resp.Engine,
		Profile:   resp.Profile,
		Path:      resp.Path,
		Waypoints: []routeExportWaypoint{
			{Role: "start", Name: from, Coord: osmmini.Coord{Lat: resp.From.Lat, Lon: resp.From.Lon}},
			{Role: "end", Name: to, Coord: osmmini.Coord{Lat: resp.To.Lat, Lon: resp.To.Lon}},
		},
		Steps: resp.Steps,
	}
}

func routeExportFromTrip(resp TripSolveResponse) routeExport {
	exp := routeExport{
		Name:      "OSMmini Tour",
		DistanceM: resp.DistanceM,
		DurationS: resp.DurationS,
		Engine:    resp.Engine,
		Path:      resp.Path,
	}
	legPoint := func(role string, p RoutePoint, fallback string) routeExportWaypoint {
		return routeExportWaypoint{Role: role, Name: firstNonEmpty(p.Label, p.Input, fallback), Coord: osmmini.Coord{Lat: p.Lat, Lon: p.Lon}}
	}
	if len(resp.Legs) > 0 {
		exp.Waypoints = append(exp.Waypoints, legPoint("start", resp.Legs[0].From, "Start"))
	}
	for i, st := range resp.Stops {
		exp.Waypoints = append(exp.Waypoints, routeExportWaypoint{
			Role:  "stop",
			Name:  fmt.Sprintf("%d. %s", i+1, firstNonEmpty(st.Label, st.ID)),
			Coord: osmmini.Coord{Lat: st.Lat, Lon: st.Lon},
		})
	}
	if len(resp.Legs) > 0 {
		exp.Waypoints = append(exp.Waypoints, legPoint("end", resp.Legs[len(resp.Legs)-1].To, "Ziel"))
	}
	for _, leg := range resp.Legs {
		exp.Steps = append(exp.Steps, leg.Steps...)
	}
	return exp
}

// writeRouteExport writes exp as a downloadable GPX or GeoJSON file.
func writeRouteExport(w http.ResponseWriter, format string, exp routeExport) {
	filename := "osmmini-route-" + time.Now().UTC().Format("20060102-150405")
	switch format {
	case routeFormatGPX:
		w.Header().Set("Content-Type", "application/gpx+xml; charset=utf-8")
		filename += ".gpx"
	default:
		w.Header().Set("Content-Type", "application/geo+json; charset=utf-8")
		filename += ".geojson"
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if format == routeFormatGPX {
		_ = encodeRouteGPX(w, exp)
		return
	}
	_ = encodeRouteGeoJSON(w, exp)
}

type gpxDoc struct {
	XMLName  xml.Name    `xml:"gpx"`
	Version  string      `xml:"version,attr"`
	Creator  string      `xml:"creator,attr"`
	Xmlns    string      `xml:"xmlns,attr"`
	Metadata gpxMetadata `xml:"metadata"`
	Wpts     []gpxPoint  `xml:"wpt"`
	Rte      *gpxRoute   `xml:"rte,omitempty"`
	Trk      gpxTrack    `xml:"trk"`
}

type gpxMetadata struct {
	Name string `xml:"name"`
	Desc string `xml:"desc,omitempty"`
	Time string `xml:"time"`
}

type gpxPoint struct {
	Lat  string `xml:"lat,attr"`
	Lon  string `xml:"lon,attr"`
	Name string `xml:"name,omitempty"`
	Desc string `xml:"desc,omitempty"`
	Type string `xml:"type,omitempty"`
}

type gpxRoute struct {
	Name   string     `xml:"name"`
	Points []gpxPoint `xml:"rtept"`
}

type gpxTrack struct {
	Name string     `xml:"name"`
	Desc string     `xml:"desc,omitempty"`
	Seg  gpxSegment `xml:"trkseg"`
}

type gpxSegment struct {
	Points []gpxPoint `xml:"trkpt"`
}

func gpxCoord(c osmmini.Coord) (string, string) {
	return strconv.FormatFloat(c.Lat, 'f', 7, 64), strconv.FormatFloat(c.Lon, 'f', 7, 64)
}

func routeSummary(exp routeExport) string {
	return fmt.Sprintf("%.1f km, ca. %d min", exp.DistanceM/1000, int(exp.DurationS/60+0.5))
}

// encodeRouteGPX writes GPX 1.1: waypoints for start/stops/end, a <rte> with
// the turn-by-turn maneuvers (shown as instructions by most devices) and a
// <trk> carrying the exact road geometry.
func encodeRouteGPX(w io.Writer, exp routeExport) error {
	doc := gpxDoc{
		Version:  "1.1",
		Creator:  "osmmini",
		Xmlns:    "http://www.topografix.com/GPX/1/1",
		Metadata: gpxMetadata{Name: exp.Name, Desc: routeSummary(exp), Time: time.Now().UTC().Format(time.RFC3339)},
		Trk:      gpxTrack{Name: exp.Name, Desc: routeSummary(exp)},
	}
	for _, wp := range exp.Waypoints {
		lat, lon := gpxCoord(wp.Coord)
		doc.Wpts = append(doc.Wpts, gpxPoint{Lat: lat, Lon: lon, Name: wp.Name, Type: wp.Role})
	}
	if len(exp.Steps) > 0 {
		rte := &gpxRoute{Name: exp.Name}
		for _, st := range exp.Steps {
			lat, lon := gpxCoord(osmmini.Coord{Lat: st.Lat, Lon: st.Lon})
			rte.Points = append(rte.Points, gpxPoint{Lat: lat, Lon: lon, Name: st.Instruction, Type: st.Type})
		}
		doc.Rte = rte
	}
	doc.Trk.Seg.Points = make([]gpxPoint, 0, len(exp.Path))
	for _, c := range exp.Path {
		lat, lon := gpxCoord(c)
		doc.Trk.Seg.Points = append(doc.Trk.Seg.Points, gpxPoint{Lat: lat, Lon: lon})
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// encodeRouteGeoJSON writes a FeatureCollection with the route LineString
// followed by one Point feature per waypoint. Coordinates are [lon, lat].
func encodeRouteGeoJSON(w io.Writer, exp routeExport) error {
	line := make([][2]float64, 0, len(exp.Path))
	for _, c := range exp.Path {
		line = append(line, [2]float64{c.Lon, c.Lat})
	}
	routeProps := map[string]any{
		"name":       exp.Name,
		"kind":       "route",
		"distance_m": exp.DistanceM,
		"duration_s": exp.DurationS,
		"engine":     exp.Engine,
	}
	if exp.Profile != "" {
		routeProps["profile"] = exp.Profile
	}
	if len(exp.Steps) > 0 {
		routeProps["steps"] = exp.Steps
	}
	features := []map[string]any{{
		"type":       "Feature",
		"geometry":   map[string]any{"type": "LineString", "coordinates": line},
		"properties": routeProps,
	}}
	for i, wp := range exp.Waypoints {
		features = append(features, map[string]any{
			"type":       "Feature",
			"geometry":   map[string]any{"type": "Point", "coordinates": [2]float64{wp.Coord.Lon, wp.Coord.Lat}},
			"properties": map[string]any{"name": wp.Name, "kind": wp.Role, "order": i},
		})
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{"type": "FeatureCollection", "features": features})
}
