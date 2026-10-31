package osmmini

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

func TestFileGraphMatchesMemoryGraph(t *testing.T) {
	r, from, to := buildGridRouter(12, 12)
	r.streets = map[string]streetEntry{"teststrasse": {Display: "Teststraße", NodeIDs: []int64{from, to}}}
	path := filepath.Join(t.TempDir(), "graph.bin")
	if err := r.WriteGraph(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if disk.NodeCount() != r.NodeCount() || disk.EdgeCount() != r.EdgeCount() {
		t.Fatal("counts differ")
	}
	for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
		opt := RouteOptions{Engine: engine, Objective: ObjectiveDuration}
		want, err := r.RouteWithOptions(t.Context(), from, to, opt)
		if err != nil {
			t.Fatal(err)
		}
		got, err := disk.RouteWithOptions(t.Context(), from, to, opt)
		if err != nil || got.Cost != want.Cost || got.DistanceM != want.DistanceM {
			t.Fatalf("%s: got %v %v; want %v", engine, got, err, want)
		}
	}
	c, _ := r.Coord(from)
	id, _, ok := disk.NearestNode(c.Lat, c.Lon)
	if !ok || id != from {
		t.Fatalf("snap %d %v", id, ok)
	}
	if matches := disk.SearchStreets("Teststraße", 3); len(matches) != 1 {
		t.Fatalf("street names lost: %v", matches)
	}
	if _, err := disk.RouteWithOptions(context.Background(), to, from, RouteOptions{}); err != nil {
		t.Fatal(err)
	}
}
func TestGraphRejectsTruncationAndCancellation(t *testing.T) {
	r, _, _ := buildGridRouter(3, 3)
	path := filepath.Join(t.TempDir(), "graph")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.WriteGraph(ctx, path); err == nil {
		t.Fatal("cancelled graph was written")
	}
	_ = os.Remove(path)
	if err := r.WriteGraph(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGraph(path); err == nil {
		t.Fatal("accepted truncated graph")
	}
}

// Observing the output guarantees cancellation occurs after OpenFile succeeds,
// so this exercises cleanup rather than the initial context check.
type cancelWhenGraphCreated struct {
	context.Context
	path   string
	cancel context.CancelFunc
}

func (c cancelWhenGraphCreated) Err() error {
	if _, err := os.Stat(c.path); err == nil {
		c.cancel()
	}
	return c.Context.Err()
}

func TestGraphWriteCancellationAllowsRetry(t *testing.T) {
	memory, _, _ := buildGridRouter(3, 3)
	source := filepath.Join(t.TempDir(), "source.bin")
	if err := memory.WriteGraph(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(source)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	for name, router := range map[string]*Router{"memory": memory, "disk": disk} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "graph.bin")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := router.WriteGraph(cancelWhenGraphCreated{ctx, path, cancel}, path); !errors.Is(err, context.Canceled) {
				t.Fatalf("write after output creation: %v; want cancellation", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled write left an output file: %v", err)
			}
			if err := router.WriteGraph(t.Context(), path); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			reopened, err := OpenGraph(path)
			if err != nil {
				t.Fatalf("retry produced an invalid graph: %v", err)
			}
			defer reopened.Close()
			if reopened.NodeCount() != router.NodeCount() || reopened.EdgeCount() != router.EdgeCount() {
				t.Fatal("retry changed graph counts")
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := router.WriteGraph(t.Context(), path); !errors.Is(err, os.ErrExist) {
				t.Fatalf("existing output: %v; want file-exists error", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed exclusive create changed the existing graph: %v", err)
			}
		})
	}
}

func TestFileGraphSequentialScanAndExport(t *testing.T) {
	r, _, terminal := buildGridRouter(5, 4)
	delete(r.g.adj, terminal) // A one-way sink still has a coordinate record.
	path := filepath.Join(t.TempDir(), "graph.bin")
	if err := r.WriteGraph(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	seen := make(map[int64]Coord)
	disk.g.eachNode(func(id int64, c Coord) { seen[id] = c })
	if len(seen) != len(r.g.coords) {
		t.Fatalf("scan has %d nodes, want %d", len(seen), len(r.g.coords))
	}
	for id, want := range r.g.coords {
		if got, ok := seen[id]; !ok || got != want {
			t.Fatalf("scanned coordinate %d: %v, %v; want %v", id, got, ok, want)
		}
	}
	count := 0
	disk.g.eachRoutableNode(func(id int64, _ Coord) {
		count++
		if id == terminal {
			t.Error("routable scan returned a node with no outgoing edges")
		}
	})
	if count != r.NodeCount()-1 {
		t.Fatalf("routable scan has %d nodes", count)
	}
	coords, adj := disk.ExportGraph()
	for id, want := range r.g.coords {
		if coords[id] != want || !slices.Equal(adj[id], r.g.adj[id]) {
			t.Fatalf("export differs at node %d", id)
		}
	}
	// Exported maps and edge storage remain owned by the caller after Close.
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	for id, want := range r.g.coords {
		if coords[id] != want || !slices.Equal(adj[id], r.g.adj[id]) {
			t.Fatalf("export changed after closing mmap at node %d", id)
		}
	}
	// Append to one node must not overwrite the next node's adjacency.
	for id, edges := range adj {
		adj[id] = append(edges, Edge{To: terminal})
		for other, want := range r.g.adj {
			if other != id && !slices.Equal(adj[other], want) {
				t.Fatalf("append at node %d overwrote node %d", id, other)
			}
		}
		break
	}
}

func TestFileGraphCellIterator(t *testing.T) {
	r, _, _ := buildGridRouter(25, 25)
	path := filepath.Join(t.TempDir(), "graph.bin")
	if err := r.WriteGraph(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	cells := make(map[int64][]int64)
	for id, c := range r.g.coords {
		x, y := r.idx.cell(c.Lat, c.Lon)
		key := cellKey(x, y)
		cells[key] = append(cells[key], id)
	}
	for _, key := range []int64{cellKey(-1, -1), cellKey(99, 99)} {
		cells[key] = nil
	}
	for key, want := range cells {
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		var got []int64
		it := disk.g.store.cellNodesIter(key)
		for it.next() {
			got = append(got, it.nodeID())
		}
		if it.next() || !slices.Equal(got, want) {
			t.Fatalf("cell %d: got %v, want %v", key, got, want)
		}
	}
	key := cellKey(0, 0)
	allocs := testing.AllocsPerRun(100, func() {
		var sum int64
		for it := disk.g.store.cellNodesIter(key); it.next(); {
			sum += it.nodeID()
		}
		graphStoreBenchmarkSum = sum
	})
	if allocs != 0 {
		t.Fatalf("cell scan allocated %g times", allocs)
	}
}

func TestFileGraphRewritePreservesFormatAndCancellation(t *testing.T) {
	r, _, _ := buildGridRouter(8, 8)
	dir := t.TempDir()
	source, target := filepath.Join(dir, "source.bin"), filepath.Join(dir, "copy.bin")
	if err := r.WriteGraph(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(source)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := disk.WriteGraph(ctx, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled rewrite: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled rewrite left an output file: %v", err)
	}
	if err := disk.WriteGraph(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("rewrite changed the portable format: %v", err)
	}
	if err := disk.WriteGraph(t.Context(), target); err == nil {
		t.Fatal("rewrite overwrote an existing graph")
	}
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	copyRouter, err := OpenGraph(target)
	if err != nil {
		t.Fatal(err)
	}
	defer copyRouter.Close()
	if copyRouter.NodeCount() != r.NodeCount() || copyRouter.EdgeCount() != r.EdgeCount() {
		t.Fatal("rewritten graph counts differ")
	}
}
