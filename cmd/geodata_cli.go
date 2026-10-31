package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

// runGeodataCLI implements `osmmini geodata import`, a standalone (no
// running server needed) counterpart to POST /api/v1/geodata/import --
// mirrors runTerritoriesCLI's shape (cmd/territories.go).
func runGeodataCLI(args []string) {
	if len(args) == 0 || args[0] != "import" {
		fmt.Fprintln(os.Stderr, "usage: osmmini geodata import --layer NAME --input FILE [--format auto|geojson|kml|kmz|shp] [--imported-layers-dir DIR] [--territories-dir DIR]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("geodata import", flag.ExitOnError)
	layer := fs.String("layer", "", "layer name (letters, digits, - or _)")
	input := fs.String("input", "", "source GIS file (GeoJSON, KML, KMZ, or a Shapefile ZIP)")
	format := fs.String("format", "auto", "auto|geojson|kml|kmz|shp")
	importedLayersDir := fs.String("imported-layers-dir", "local/imported-layers", "Directory imported layers are written to")
	territoriesDir := fs.String("territories-dir", "local/territories", "Directory polygon-only imported layers are mirrored into (for dispatch/routing-cost lookups)")
	fs.Parse(args[1:])

	if *layer == "" || *input == "" {
		fmt.Fprintln(os.Stderr, "geodata import: --layer and --input are required")
		os.Exit(2)
	}
	if !geodataLayerNamePattern.MatchString(*layer) {
		log.Fatalf("geodata import: --layer must be 1-64 letters, digits, - or _")
	}

	resolvedFormat := *format
	if resolvedFormat == "" || resolvedFormat == "auto" {
		resolvedFormat = vectorFormatFromFilename(*input)
	}
	if resolvedFormat == "" {
		log.Fatalf("geodata import: could not infer --format from %q; pass --format explicitly", *input)
	}

	f, err := os.Open(*input)
	if err != nil {
		log.Fatalf("geodata import: %v", err)
	}
	defer f.Close()

	geojson, err := importVectorToGeoJSON(context.Background(), resolvedFormat, f)
	if err != nil {
		log.Fatalf("geodata import: %v", err)
	}
	imported, err := osmmini.ParseImportedLayer(*layer, geojson)
	if err != nil {
		log.Fatalf("geodata import: converted data is not valid GeoJSON: %v", err)
	}

	if err := os.MkdirAll(*importedLayersDir, 0o755); err != nil {
		log.Fatalf("geodata import: %v", err)
	}
	outPath := filepath.Join(*importedLayersDir, *layer+".geojson")
	if err := writeFileAtomically(outPath, imported.GeoJSON); err != nil {
		log.Fatalf("geodata import: %v", err)
	}

	territoryNote := ""
	if imported.IsPolygonOnly() {
		if err := os.MkdirAll(*territoriesDir, 0o755); err != nil {
			log.Fatalf("geodata import: %v", err)
		}
		mirrorPath := filepath.Join(*territoriesDir, *layer+".geojson")
		if err := writeFileAtomically(mirrorPath, imported.GeoJSON); err != nil {
			log.Fatalf("geodata import: %v", err)
		}
		territoryNote = fmt.Sprintf(" (mirrored into %s -- also usable as a territory layer)", mirrorPath)
	}

	fmt.Printf("layer=%s format=%s features=%d geometry_types=%v output=%s%s\n",
		imported.Name, resolvedFormat, imported.FeatureCount, imported.GeometryTypes, outPath, territoryNote)
}
