package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

// regionBounds uses osmium's conventional left,bottom,right,top ordering.
// It stays separate from CoordWindow so command-line input cannot accidentally
// swap latitude and longitude.
type regionBounds struct {
	minLon float64
	minLat float64
	maxLon float64
	maxLat float64
}

const regionalPBFManifestVersion = 1

type regionalPBFManifest struct {
	Version           int    `json:"version"`
	SourceSize        int64  `json:"source_size"`
	SourceModTimeNano int64  `json:"source_mod_time_nano"`
	Bounds            string `json:"bounds"`
	OutputSize        int64  `json:"output_size"`
}

func regionalPBFManifestPath(output string) string { return output + ".meta" }

func regionalPBFIsReusable(source, output string, bounds regionBounds) bool {
	sourceInfo, sourceErr := os.Stat(source)
	outputInfo, outputErr := os.Stat(output)
	data, manifestErr := os.ReadFile(regionalPBFManifestPath(output))
	if sourceErr != nil || outputErr != nil || outputInfo.IsDir() || manifestErr != nil {
		return false
	}
	var manifest regionalPBFManifest
	if json.Unmarshal(data, &manifest) != nil {
		return false
	}
	return manifest.Version == regionalPBFManifestVersion &&
		manifest.SourceSize == sourceInfo.Size() &&
		manifest.SourceModTimeNano == sourceInfo.ModTime().UnixNano() &&
		manifest.Bounds == bounds.String() && manifest.OutputSize == outputInfo.Size()
}

func saveRegionalPBFManifest(source, output string, bounds regionBounds) error {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	outputInfo, err := os.Stat(output)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(regionalPBFManifest{
		Version:           regionalPBFManifestVersion,
		SourceSize:        sourceInfo.Size(),
		SourceModTimeNano: sourceInfo.ModTime().UnixNano(),
		Bounds:            bounds.String(),
		OutputSize:        outputInfo.Size(),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(regionalPBFManifestPath(output), payload, 0o600)
}

func parseRegionBounds(value string) (regionBounds, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 4 {
		return regionBounds{}, errors.New("bbox must be minLon,minLat,maxLon,maxLat")
	}
	values := [4]float64{}
	for i, part := range parts {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return regionBounds{}, fmt.Errorf("bbox value %d: %w", i+1, err)
		}
		values[i] = parsed
	}
	bounds := regionBounds{minLon: values[0], minLat: values[1], maxLon: values[2], maxLat: values[3]}
	if !(osmmini.CoordWindow{MinLat: bounds.minLat, MaxLat: bounds.maxLat, MinLon: bounds.minLon, MaxLon: bounds.maxLon}).Valid() {
		return regionBounds{}, errors.New("bbox is outside WGS84 or has no area")
	}
	return bounds, nil
}

func (b regionBounds) String() string {
	return strconv.FormatFloat(b.minLon, 'f', -1, 64) + "," +
		strconv.FormatFloat(b.minLat, 'f', -1, 64) + "," +
		strconv.FormatFloat(b.maxLon, 'f', -1, 64) + "," +
		strconv.FormatFloat(b.maxLat, 'f', -1, 64)
}

// runRegionExtractCLI creates a self-contained PBF for one geographic window.
// osmium's complete_ways strategy includes the referenced nodes of intersecting
// ways, so the resulting PBF can be used unchanged by routing, search and
// tinyTiles. Its process boundary keeps the GPL-licensed osmium tool separate
// from osmmini's Go binary.
func runRegionExtractCLI(args []string) {
	flags := flag.NewFlagSet("region-extract", flag.ExitOnError)
	pbfPath := flags.String("pbf", "local/region.osm.pbf", "Path to the large OSM PBF")
	bbox := flags.String("bbox", "", "Region as minLon,minLat,maxLon,maxLat (required)")
	output := flags.String("output", "", "Output regional .osm.pbf path (required)")
	buildIndex := flags.Bool("index", true, "Build or refresh the source PBF spatial index before extracting")
	reuse := flags.Bool("reuse", true, "Reuse a current regional PBF for the same source and bounding box")
	_ = flags.Parse(args)

	if strings.TrimSpace(*bbox) == "" || strings.TrimSpace(*output) == "" {
		flags.Usage()
		return
	}
	if _, err := os.Stat(*pbfPath); err != nil {
		log.Fatalf("region-extract: PBF file not found: %v", err)
	}
	region, err := parseRegionBounds(*bbox)
	if err != nil {
		log.Fatalf("region-extract: %v", err)
	}
	if filepath.Clean(*output) == filepath.Clean(*pbfPath) {
		log.Fatal("region-extract: output must not replace the source PBF")
	}

	ctx := context.Background()
	if *buildIndex {
		indexPath := osmmini.PBFSpatialIndexPath(*pbfPath)
		if existing, loadErr := osmmini.LoadPBFSpatialIndex(indexPath); loadErr == nil && existing.FreshFor(*pbfPath) {
			log.Printf("region-extract: source index %s is current", indexPath)
		} else {
			log.Printf("region-extract: indexing source PBF once before extraction…")
			if _, err := osmmini.BuildPBFSpatialIndex(ctx, *pbfPath, indexPath, nil); err != nil {
				log.Fatalf("region-extract: build source index: %v", err)
			}
		}
	}
	if *reuse && regionalPBFIsReusable(*pbfPath, *output, region) {
		info, _ := os.Stat(*output)
		fmt.Printf("region=%s output=%s size=%d reused=true\n", region, *output, info.Size())
		return
	}

	if err := extractRegionPBF(ctx, *pbfPath, *output, region); err != nil {
		log.Fatalf("region-extract: %v", err)
	}
	info, err := os.Stat(*output)
	if err != nil {
		log.Fatalf("region-extract: inspect output: %v", err)
	}
	if err := saveRegionalPBFManifest(*pbfPath, *output, region); err != nil {
		log.Printf("region-extract: could not save cache manifest: %v", err)
	}
	fmt.Printf("region=%s output=%s size=%d\n", region, *output, info.Size())
}

func extractRegionPBF(ctx context.Context, source, output string, bounds regionBounds) error {
	osmium, err := exec.LookPath("osmium")
	if err != nil {
		return errors.New("osmium is required for region-extract; install osmium-tool or use a pre-extracted regional PBF")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".osmmini-region-*.osm.pbf")
	if err != nil {
		return fmt.Errorf("create temporary regional PBF: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close temporary regional PBF: %w", err)
	}
	defer os.Remove(temporaryPath)

	command := exec.CommandContext(ctx, osmium,
		"extract", "--strategy", "complete_ways", "--set-bounds",
		"--bbox", bounds.String(), "--output", temporaryPath, "--overwrite", source,
	)
	command.Stderr = os.Stderr
	command.Stdout = os.Stdout
	if err := command.Run(); err != nil {
		return fmt.Errorf("stream geographic extract: %w", err)
	}
	if err := os.Rename(temporaryPath, output); err != nil {
		return fmt.Errorf("publish regional PBF: %w", err)
	}
	return nil
}
