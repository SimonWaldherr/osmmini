package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini"
)

// publishPostalTerritories converts tinyTiles' real five-digit postal-boundary
// sidecar into one territory per requested prefix (PLZ1 through PLZ5). The
// component polygons are retained exactly; grouping never invents a convex
// hull or otherwise guesses a boundary from address points.
func (s *server) publishPostalTerritories(postcodesPath string, prefixLength int) (string, int, error) {
	if prefixLength < 1 || prefixLength > 5 {
		return "", 0, fmt.Errorf("postal prefix length %d is outside 1..5", prefixLength)
	}
	if strings.TrimSpace(postcodesPath) == "" {
		return "", 0, fmt.Errorf("no OSM postal-code boundaries were found in the loaded PBF")
	}
	postcodes, err := osmmini.LoadTerritoriesGeoJSON(postcodesPath)
	if err != nil {
		return "", 0, err
	}
	grouped, err := groupPostalTerritories(postcodes, prefixLength)
	if err != nil {
		return "", 0, err
	}
	layer := fmt.Sprintf("plz%d", prefixLength)
	data, err := marshalTerritoryFeatureCollection(grouped)
	if err != nil {
		return "", 0, err
	}
	output := filepath.Join(s.territoriesDir, layer+".geojson")
	if err := writeFileAtomically(output, data); err != nil {
		return "", 0, err
	}
	// Loading from disk validates the saved GeoJSON exactly as it will be
	// served, and makes the new layer immediately available without restart.
	s.loadTerritories(s.territoriesDir)
	return layer, len(grouped), nil
}

func groupPostalTerritories(postcodes []*osmmini.Territory, prefixLength int) ([]*osmmini.Territory, error) {
	groups := make(map[string]*osmmini.Territory)
	for _, postcode := range postcodes {
		code, _ := postcode.Properties["postcode"].(string)
		code = strings.TrimSpace(code)
		if len(code) != 5 || !allASCIIDigits(code) {
			continue
		}
		prefix := code[:prefixLength]
		group := groups[prefix]
		if group == nil {
			group = &osmmini.Territory{
				ID:   prefix,
				Name: "PLZ " + prefix,
				Properties: map[string]any{
					"territory_id":    prefix,
					"postcode_prefix": prefix,
					"source":          "OpenStreetMap boundary=postal_code",
				},
			}
			groups[prefix] = group
		}
		group.Geometry.Polygons = append(group.Geometry.Polygons, postcode.Geometry.Polygons...)
	}

	keys := make([]string, 0, len(groups))
	for prefix := range groups {
		keys = append(keys, prefix)
	}
	sort.Strings(keys)
	result := make([]*osmmini.Territory, 0, len(keys))
	for _, prefix := range keys {
		result = append(result, groups[prefix])
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no usable five-digit OSM postal-code boundaries found")
	}
	return result, nil
}

func allASCIIDigits(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func marshalTerritoryFeatureCollection(territories []*osmmini.Territory) ([]byte, error) {
	type feature struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
		Geometry   any            `json:"geometry"`
	}
	features := make([]feature, 0, len(territories))
	for _, territory := range territories {
		geometry, err := territoryGeometryGeoJSON(territory.Geometry)
		if err != nil {
			return nil, fmt.Errorf("territory %q: %w", territory.ID, err)
		}
		properties := make(map[string]any, len(territory.Properties)+1)
		for key, value := range territory.Properties {
			properties[key] = value
		}
		properties["territory_id"] = territory.ID
		features = append(features, feature{Type: "Feature", Properties: properties, Geometry: geometry})
	}
	return json.Marshal(struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}{Type: "FeatureCollection", Features: features})
}

func territoryGeometryGeoJSON(geometry osmmini.Geometry) (any, error) {
	if len(geometry.Polygons) == 0 {
		return nil, fmt.Errorf("has no polygons")
	}
	polygons := make([][][][2]float64, 0, len(geometry.Polygons))
	for _, polygon := range geometry.Polygons {
		if len(polygon.Outer) < 4 {
			return nil, fmt.Errorf("has an invalid outer ring")
		}
		rings := make([][][2]float64, 0, len(polygon.Holes)+1)
		rings = append(rings, ringCoordinates(polygon.Outer))
		for _, hole := range polygon.Holes {
			if len(hole) < 4 {
				return nil, fmt.Errorf("has an invalid inner ring")
			}
			rings = append(rings, ringCoordinates(hole))
		}
		polygons = append(polygons, rings)
	}
	return map[string]any{"type": "MultiPolygon", "coordinates": polygons}, nil
}

func ringCoordinates(ring osmmini.Ring) [][2]float64 {
	coordinates := make([][2]float64, len(ring))
	for i, point := range ring {
		coordinates[i] = [2]float64{point.Lon, point.Lat}
	}
	return coordinates
}

func writeFileAtomically(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".osmmini-territory-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
