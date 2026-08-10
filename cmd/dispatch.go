package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini"
)

// runDispatchCLI implements `osmmini dispatch assign` and
// `osmmini dispatch manifests`.
func runDispatchCLI(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: osmmini dispatch assign|manifests ...")
		os.Exit(2)
	}
	switch args[0] {
	case "assign":
		runDispatchAssign(args[1:])
	case "manifests":
		runDispatchManifests(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "osmmini dispatch: unknown subcommand %q (want assign or manifests)\n", args[0])
		os.Exit(2)
	}
}

// parcelRow is one input row: a required id/lat/lon plus the full original
// row (so manifest output can pass every input column through unchanged).
type parcelRow struct {
	id     string
	lat    float64
	lon    float64
	fields map[string]string
}

func readParcelsCSV(path string) ([]parcelRow, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[h] = i
	}
	for _, want := range []string{"parcel_id", "lat", "lon"} {
		if _, ok := idx[want]; !ok {
			return nil, nil, fmt.Errorf("input CSV is missing required column %q", want)
		}
	}

	var rows []parcelRow
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		lat, err := strconv.ParseFloat(rec[idx["lat"]], 64)
		if err != nil {
			return nil, nil, fmt.Errorf("row %d: invalid lat %q", len(rows)+2, rec[idx["lat"]])
		}
		lon, err := strconv.ParseFloat(rec[idx["lon"]], 64)
		if err != nil {
			return nil, nil, fmt.Errorf("row %d: invalid lon %q", len(rows)+2, rec[idx["lon"]])
		}
		fields := make(map[string]string, len(header))
		for i, h := range header {
			if i < len(rec) {
				fields[h] = rec[i]
			}
		}
		rows = append(rows, parcelRow{id: rec[idx["parcel_id"]], lat: lat, lon: lon, fields: fields})
	}
	return rows, header, nil
}

func runDispatchAssign(args []string) {
	fs := flag.NewFlagSet("dispatch assign", flag.ExitOnError)
	territoriesPath := fs.String("territories", "", "territory GeoJSON file")
	layer := fs.String("layer", "delivery", "territory layer name to assign against")
	input := fs.String("input", "", "input parcels CSV (parcel_id,lat,lon,...)")
	output := fs.String("output", "", "output assignments CSV")
	include := fs.String("include", "", "comma-separated territory properties to include (default: every property present in the layer)")
	onUnassigned := fs.String("on-unassigned", string(osmmini.PolicyUnassigned), "policy for unmatched destinations: error, unassigned, nearest, fallback:<territory-id>")
	fs.Parse(args)

	if *territoriesPath == "" || *input == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "usage: osmmini dispatch assign --territories FILE.geojson --input parcels.csv --output assignments.csv [--layer NAME] [--include fields] [--on-unassigned policy]")
		os.Exit(2)
	}

	store := osmmini.NewTerritoryStore()
	if err := store.LoadLayer(*layer, *territoriesPath); err != nil {
		log.Fatalf("dispatch assign: %v", err)
	}

	rows, _, err := readParcelsCSV(*input)
	if err != nil {
		log.Fatalf("dispatch assign: %v", err)
	}

	includeFields := parseIncludeFields(*include, store, *layer)

	points := make([]osmmini.Point, len(rows))
	for i, row := range rows {
		points[i] = osmmini.Point{ID: row.id, Lat: row.lat, Lon: row.lon}
	}
	assignments, err := store.AssignPointsWithOptions(points, osmmini.AssignOptions{
		Layer:        *layer,
		Include:      includeFields,
		OnUnassigned: osmmini.UnassignedPolicy(*onUnassigned),
	})
	if err != nil {
		log.Fatalf("dispatch assign: %v", err)
	}

	if err := writeAssignmentsCSV(*output, assignments, includeFields); err != nil {
		log.Fatalf("dispatch assign: %v", err)
	}
	fmt.Printf("assigned=%d output=%s\n", len(assignments), *output)
}

// parseIncludeFields resolves --include into a stable, deterministic column
// list: the explicit comma-separated list if given, otherwise every
// property that appears on any territory in the layer (sorted), so the
// output CSV has the same columns for every row regardless of which
// specific territory it matched.
func parseIncludeFields(flagVal string, store *osmmini.TerritoryStore, layer string) []string {
	if strings.TrimSpace(flagVal) != "" {
		parts := strings.Split(flagVal, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" && p != "territory_id" {
				out = append(out, p)
			}
		}
		return out
	}
	keys := map[string]bool{}
	for _, t := range store.Territories(layer) {
		for k := range t.Properties {
			keys[k] = true
		}
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		if k != "territory_id" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func writeAssignmentsCSV(path string, assignments []osmmini.TerritoryAssignment, includeFields []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	header := append([]string{"parcel_id", "territory_id"}, includeFields...)
	if err := w.Write(header); err != nil {
		return err
	}
	for _, a := range assignments {
		row := make([]string, len(header))
		row[0] = a.PointID
		row[1] = a.TerritoryID
		for i, field := range includeFields {
			if a.Properties != nil {
				row[2+i] = osmmini.PropertyString(a.Properties[field])
			}
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func runDispatchManifests(args []string) {
	fs := flag.NewFlagSet("dispatch manifests", flag.ExitOnError)
	territoriesPath := fs.String("territories", "", "territory GeoJSON file")
	layer := fs.String("layer", "delivery", "territory layer name to assign against")
	input := fs.String("input", "", "input parcels CSV (parcel_id,lat,lon,...)")
	groupBy := fs.String("group-by", "", "territory property to group manifests by (e.g. vehicle)")
	outputDir := fs.String("output", "manifests", "output directory, one CSV per group value")
	onUnassigned := fs.String("on-unassigned", string(osmmini.PolicyUnassigned), "policy for unmatched destinations: error, unassigned, nearest, fallback:<territory-id>")
	fs.Parse(args)

	if *territoriesPath == "" || *input == "" || *groupBy == "" {
		fmt.Fprintln(os.Stderr, "usage: osmmini dispatch manifests --territories FILE.geojson --input parcels.csv --group-by FIELD --output DIR [--layer NAME] [--on-unassigned policy]")
		os.Exit(2)
	}

	store := osmmini.NewTerritoryStore()
	if err := store.LoadLayer(*layer, *territoriesPath); err != nil {
		log.Fatalf("dispatch manifests: %v", err)
	}
	rows, header, err := readParcelsCSV(*input)
	if err != nil {
		log.Fatalf("dispatch manifests: %v", err)
	}

	points := make([]osmmini.Point, len(rows))
	for i, row := range rows {
		points[i] = osmmini.Point{ID: row.id, Lat: row.lat, Lon: row.lon}
	}
	assignments, err := store.AssignPointsWithOptions(points, osmmini.AssignOptions{
		Layer:        *layer,
		OnUnassigned: osmmini.UnassignedPolicy(*onUnassigned),
	})
	if err != nil {
		log.Fatalf("dispatch manifests: %v", err)
	}

	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		log.Fatalf("dispatch manifests: %v", err)
	}

	groups := map[string][]int{}
	var groupOrder []string
	for i, a := range assignments {
		group := "UNASSIGNED"
		if a.Matched {
			if v, ok := a.Properties[*groupBy]; ok {
				group = osmmini.PropertyString(v)
			}
		}
		if _, seen := groups[group]; !seen {
			groupOrder = append(groupOrder, group)
		}
		groups[group] = append(groups[group], i)
	}
	sort.Strings(groupOrder)

	outHeader := append(append([]string{}, header...), "territory_id")
	for _, group := range groupOrder {
		path := filepath.Join(*outputDir, sanitizeManifestName(group)+".csv")
		if err := writeManifestCSV(path, outHeader, header, rows, assignments, groups[group]); err != nil {
			log.Fatalf("dispatch manifests: %v", err)
		}
		fmt.Printf("%s: %d shipments\n", path, len(groups[group]))
	}
}

func writeManifestCSV(path string, outHeader, header []string, rows []parcelRow, assignments []osmmini.TerritoryAssignment, indices []int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if err := w.Write(outHeader); err != nil {
		return err
	}
	for _, i := range indices {
		rec := make([]string, len(outHeader))
		for j, h := range header {
			rec[j] = rows[i].fields[h]
		}
		rec[len(header)] = assignments[i].TerritoryID
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func sanitizeManifestName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "UNASSIGNED"
	}
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
