package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	osmmini "simonwaldherr.de/go/osmmini"
)

func runPBFIndexCLI(args []string) {
	flags := flag.NewFlagSet("pbf-index", flag.ExitOnError)
	pbfPath := flags.String("pbf", "region.osm.pbf", "Path to OSM PBF")
	indexPath := flags.String("output", "", "Output index path (default: <pbf base>.idx)")
	_ = flags.Parse(args)

	if _, err := os.Stat(*pbfPath); err != nil {
		log.Fatalf("pbf-index: PBF file not found: %v", err)
	}
	output := strings.TrimSpace(*indexPath)
	if output == "" {
		output = osmmini.PBFSpatialIndexPath(*pbfPath)
	}

	started := time.Now()
	lastLog := time.Time{}
	index, err := osmmini.BuildPBFSpatialIndex(context.Background(), *pbfPath, output, func(progress osmmini.PBFSpatialIndexProgress) {
		if lastLog.IsZero() || time.Since(lastLog) >= 2*time.Second || progress.BytesRead == progress.SourceBytes {
			lastLog = time.Now()
			percent := 0.0
			if progress.SourceBytes > 0 {
				percent = 100 * float64(progress.BytesRead) / float64(progress.SourceBytes)
			}
			log.Printf("pbf-index: %.1f%%, PBF-Blöcke=%d, räumliche Blöcke=%d", percent, progress.BlocksRead, progress.IndexedBlocks)
		}
	})
	if err != nil {
		log.Fatalf("pbf-index: %v", err)
	}
	fmt.Printf("index=%s blocks=%d source_sha256=%s duration=%s\n", output, len(index.Blocks), index.Source.SHA256, time.Since(started).Round(time.Second))
}
