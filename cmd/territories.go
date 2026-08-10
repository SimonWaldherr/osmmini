package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	osmmini "simonwaldherr.de/go/osmmini"
)

// defaultTerritoryStatePath is where `territories load` records which
// source file backs each layer, so a later, separate `territory lookup`
// invocation (a fresh process -- this CLI has no daemon) can find it again
// without repeating --input.
const defaultTerritoryStatePath = ".osmmini/territories.json"

type territoryManifest struct {
	Layers map[string]string `json:"layers"`
}

func loadTerritoryManifest(path string) (territoryManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return territoryManifest{Layers: map[string]string{}}, nil
		}
		return territoryManifest{}, err
	}
	var m territoryManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return territoryManifest{}, fmt.Errorf("parse state file %s: %w", path, err)
	}
	if m.Layers == nil {
		m.Layers = map[string]string{}
	}
	return m, nil
}

func saveTerritoryManifest(path string, m territoryManifest) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// runTerritoriesCLI implements `osmmini territories load`.
func runTerritoriesCLI(args []string) {
	if len(args) == 0 || args[0] != "load" {
		fmt.Fprintln(os.Stderr, "usage: osmmini territories load --layer NAME --input FILE.geojson [--state FILE]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("territories load", flag.ExitOnError)
	layer := fs.String("layer", "", "territory layer name (e.g. sales, delivery)")
	input := fs.String("input", "", "GeoJSON territory file")
	statePath := fs.String("state", defaultTerritoryStatePath, "state file recording layer -> source path")
	fs.Parse(args[1:])

	if *layer == "" || *input == "" {
		fmt.Fprintln(os.Stderr, "territories load: --layer and --input are required")
		os.Exit(2)
	}

	abs, err := filepath.Abs(*input)
	if err != nil {
		log.Fatalf("territories load: %v", err)
	}

	territories, err := osmmini.LoadTerritoriesGeoJSON(abs)
	if err != nil {
		log.Fatalf("territories load: %v", err)
	}

	// Validate it actually builds into a usable layer (duplicate IDs, etc.)
	// before persisting anything -- a bad file should never be recorded as
	// loaded.
	store := osmmini.NewTerritoryStore()
	if err := store.LoadLayerTerritories(*layer, territories); err != nil {
		log.Fatalf("territories load: %v", err)
	}

	manifest, err := loadTerritoryManifest(*statePath)
	if err != nil {
		log.Fatalf("territories load: %v", err)
	}
	manifest.Layers[*layer] = abs
	if err := saveTerritoryManifest(*statePath, manifest); err != nil {
		log.Fatalf("territories load: %v", err)
	}

	bbox := unionBBox(territories)
	fmt.Printf("layer=%s territories=%d bbox=[%.6f,%.6f,%.6f,%.6f] state=%s\n",
		*layer, len(territories), bbox.MinLon, bbox.MinLat, bbox.MaxLon, bbox.MaxLat, *statePath)
}

// runTerritoryCLI implements `osmmini territory lookup`.
func runTerritoryCLI(args []string) {
	if len(args) == 0 || args[0] != "lookup" {
		fmt.Fprintln(os.Stderr, "usage: osmmini territory lookup --layer NAME --lat LAT --lon LON [--all] [--territories FILE.geojson] [--state FILE]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("territory lookup", flag.ExitOnError)
	layer := fs.String("layer", "", "territory layer name")
	lat := fs.Float64("lat", 0, "query latitude")
	lon := fs.Float64("lon", 0, "query longitude")
	all := fs.Bool("all", false, "return every matching territory instead of just the best match")
	territoriesPath := fs.String("territories", "", "GeoJSON territory file (overrides the state file written by `territories load`)")
	statePath := fs.String("state", defaultTerritoryStatePath, "state file to resolve --layer's source path from")
	fs.Parse(args[1:])

	if *layer == "" {
		fmt.Fprintln(os.Stderr, "territory lookup: --layer is required")
		os.Exit(2)
	}

	path := *territoriesPath
	if path == "" {
		manifest, err := loadTerritoryManifest(*statePath)
		if err != nil {
			log.Fatalf("territory lookup: %v", err)
		}
		p, ok := manifest.Layers[*layer]
		if !ok {
			log.Fatalf("territory lookup: layer %q not found in %s; pass --territories or run `territories load` first", *layer, *statePath)
		}
		path = p
	}

	store := osmmini.NewTerritoryStore()
	if err := store.LoadLayer(*layer, path); err != nil {
		log.Fatalf("territory lookup: %v", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	if *all {
		matches := store.FindTerritories(*layer, *lat, *lon)
		out := make([]map[string]any, len(matches))
		for i, t := range matches {
			out[i] = flattenTerritory(t)
		}
		if err := enc.Encode(out); err != nil {
			log.Fatalf("territory lookup: %v", err)
		}
		return
	}

	t := store.FindTerritory(*layer, *lat, *lon)
	if t == nil {
		if err := enc.Encode(map[string]any{"matched": false}); err != nil {
			log.Fatalf("territory lookup: %v", err)
		}
		return
	}
	if err := enc.Encode(flattenTerritory(t)); err != nil {
		log.Fatalf("territory lookup: %v", err)
	}
}

// flattenTerritory renders a territory as the flattened lookup result
// shape: territory_id plus every property, all at the top level.
func flattenTerritory(t *osmmini.Territory) map[string]any {
	out := make(map[string]any, len(t.Properties)+1)
	for k, v := range t.Properties {
		out[k] = v
	}
	out["territory_id"] = t.ID
	return out
}

func unionBBox(ts []*osmmini.Territory) osmmini.CoordWindow {
	w := osmmini.CoordWindow{}
	first := true
	for _, t := range ts {
		b := t.Geometry.BBox()
		if first {
			w = b
			first = false
			continue
		}
		if b.MinLat < w.MinLat {
			w.MinLat = b.MinLat
		}
		if b.MinLon < w.MinLon {
			w.MinLon = b.MinLon
		}
		if b.MaxLat > w.MaxLat {
			w.MaxLat = b.MaxLat
		}
		if b.MaxLon > w.MaxLon {
			w.MaxLon = b.MaxLon
		}
	}
	return w
}
