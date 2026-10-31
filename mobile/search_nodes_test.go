package mobile

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func TestSearchNodeSortMergesDuplicateReferencesInBoundedRuns(t *testing.T) {
	root := t.TempDir()
	builder := searchNodeBuilder{ctx: t.Context(), parent: root}
	defer builder.close()
	extremes := []int64{math.MaxInt64, -1, math.MinInt64, 0, -2, math.MaxInt64}
	if err := builder.add(extremes); err != nil {
		t.Fatal(err)
	}
	const unique = 100_003
	ids := make([]int64, searchNodeSortIDs)
	for run := range searchMergeRuns + 1 {
		for i := range ids {
			ids[i] = int64((run*7919 + len(ids) - i) % unique)
		}
		if err := builder.add(ids); err != nil {
			t.Fatal(err)
		}
		if len(builder.ids) > searchNodeSortIDs || cap(builder.ids) > searchNodeSortIDs {
			t.Fatal("reference sort retained an unbounded chunk")
		}
	}
	table, err := builder.finish()
	if err != nil {
		t.Fatal(err)
	}
	defer table.close()
	want := []int64{math.MinInt64, -2, -1}
	for i := range unique {
		want = append(want, int64(i))
	}
	want = append(want, math.MaxInt64)
	if table.count != len(want) {
		t.Fatalf("deduplicated %d nodes, want %d", table.count, len(want))
	}
	for i, id := range want {
		got, err := table.id(i)
		if err != nil || got != id {
			t.Fatalf("sorted row %d differs: %v", i, err)
		}
	}
	if err := table.finishCoordinates(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := table.coordinate(42); err != nil || found {
		t.Fatalf("unseen source node gained coordinates: %v, %v", found, err)
	}
	table.close()
	builder.close()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("reference scratch remains: %v, %v", entries, err)
	}
}

func TestSearchNodeCoordinatesPreserveUnsortedDuplicatesAndMissingNodes(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name := "mapped"
		if fallback {
			name = "file window"
		}
		t.Run(name, func(t *testing.T) {
			builder := searchNodeBuilder{ctx: t.Context(), parent: t.TempDir()}
			defer builder.close()
			ids := []int64{math.MinInt64, -10, -1, 0, 1, 2, 3, 404, 10_000_000_000, math.MaxInt64}
			if err := builder.add(ids); err != nil {
				t.Fatal(err)
			}
			table, err := builder.finish()
			if err != nil {
				t.Fatal(err)
			}
			defer table.close()
			if fallback && table.unmap != nil {
				if err := table.unmap(); err != nil {
					t.Fatal(err)
				}
				table.data, table.unmap = nil, nil
			}
			input := []struct {
				id int64
				c  osm.Coord
			}{
				{math.MinInt64, osm.Coord{Lat: math.Nextafter(1, 2), Lon: math.Copysign(0, -1)}},
				{-10, osm.Coord{Lat: -48.123456789, Lon: 12.987654321}},
				{0, osm.Coord{}},
				{3, osm.Coord{Lat: 3, Lon: 30}},
				{10_000_000_000, osm.Coord{Lat: 48.5, Lon: 12.5}},
				{math.MaxInt64, osm.Coord{Lat: 90, Lon: 180}},
				{1, osm.Coord{Lat: 1, Lon: 10}},
				{1, osm.Coord{Lat: 1.5, Lon: 15}},
				{-1, osm.Coord{Lat: -1, Lon: -10}},
				{123, osm.Coord{Lat: 99, Lon: 99}},
			}
			want := map[int64]osm.Coord{}
			for _, node := range input {
				if err := table.record(node.id, node.c.Lat, node.c.Lon); err != nil {
					t.Fatal(err)
				}
				if slices.Contains(ids, node.id) {
					want[node.id] = node.c
				}
			}
			if err := table.finishCoordinates(); err != nil {
				t.Fatal(err)
			}
			for _, id := range append(ids, 123, 999) {
				got, found, err := table.coordinate(id)
				expected, present := want[id]
				if err != nil || found != present || math.Float64bits(got.Lat) != math.Float64bits(expected.Lat) || math.Float64bits(got.Lon) != math.Float64bits(expected.Lon) {
					t.Fatalf("node %d differs: %+v/%v, want %+v/%v; %v", id, got, found, expected, present, err)
				}
			}
			if fallback && cap(table.idCache)+cap(table.coordinateCache) > searchNodeCacheRows*searchNodeRowBytes {
				t.Fatal("fallback retained the full coordinate table")
			}
		})
	}
}

func TestSearchNodeSortCancellationAndBrokenRunsRemoveScratch(t *testing.T) {
	for _, mode := range []string{"cancelled", "broken run"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			builder := searchNodeBuilder{ctx: ctx, parent: root}
			defer builder.close()
			ids := make([]int64, searchNodeSortIDs)
			for i := range ids {
				ids[i] = int64(len(ids) - i)
			}
			if err := builder.add(ids); err != nil {
				t.Fatal(err)
			}
			if mode == "cancelled" {
				builder.ctx = &cancelSearchBuildContext{Context: ctx, cancel: cancel, after: 3}
			} else if err := os.WriteFile(builder.runs[0], []byte{1}, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := builder.finish(); err == nil || mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("incomplete node sort succeeded: %v", err)
			}
			builder.close()
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed sort left scratch: %v, %v", entries, err)
			}
		})
	}
	root := t.TempDir()
	stale := filepath.Join(root, ".search-nodes-interrupted")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "coordinates"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanSearchScratch(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("interrupted reference table remains: %v", err)
	}
}

func TestSearchNodeFileWindowsPreserveSparseCoordinatesAcrossBoundaries(t *testing.T) {
	builder := searchNodeBuilder{ctx: t.Context(), parent: t.TempDir()}
	defer builder.close()
	ids := make([]int64, 3*searchNodeCacheRows+17)
	for i := range ids {
		ids[i] = int64(i)*7 - 1000
	}
	if err := builder.add(ids); err != nil {
		t.Fatal(err)
	}
	table, err := builder.finish()
	if err != nil {
		t.Fatal(err)
	}
	defer table.close()
	if table.unmap != nil {
		if err := table.unmap(); err != nil {
			t.Fatal(err)
		}
		table.data, table.unmap = nil, nil
	}
	for i, id := range ids {
		if i%17 != 0 {
			c := benchmarkSearchNodeCoordinate(id)
			if err := table.record(id, c.Lat, c.Lon); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := table.finishCoordinates(); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 4095, 4096, 8191, 8192, len(ids) - 1, 17, 1} {
		got, found, err := table.coordinate(ids[i])
		want := benchmarkSearchNodeCoordinate(ids[i])
		if err != nil || found != (i%17 != 0) || found && got != want {
			t.Fatalf("file window node %d differs: %+v/%v, %v", i, got, found, err)
		}
	}
	if cap(table.idCache)+cap(table.coordinateCache) > searchNodeCacheRows*searchNodeRowBytes {
		t.Fatal("file windows retained a regional table")
	}
}
