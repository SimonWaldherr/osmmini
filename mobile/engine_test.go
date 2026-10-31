package mobile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	osm "simonwaldherr.de/go/osmmini/core"
	"strings"
	"testing"
	"time"
)

func TestImportRejectsLegacyCheckpointWithoutModifyingIt(t *testing.T) {
	pbf := fixture(t)
	destination := filepath.Join(t.TempDir(), "region")
	stage := destination + ".pending"
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	hash, err := fileHash(t.Context(), pbf)
	if err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf("%s:14:14:%d", hash, FormatVersion)
	source := filepath.Join(stage, "source")
	if err := os.WriteFile(source, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(t.Context(), ImportOptions{PBF: pbf, Destination: destination, MinZoom: 14, MaxZoom: 14}, nil); err == nil || !strings.Contains(err.Error(), "import version") {
		t.Fatalf("legacy checkpoint was reused: %v", err)
	}
	unchanged, err := os.ReadFile(source)
	if err != nil || string(unchanged) != legacy {
		t.Fatalf("old workspace changed: %q, %v", unchanged, err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("published legacy workspace: %v", err)
	}
}

func TestImportReleasesDestinationAfterCancellation(t *testing.T) {
	o := ImportOptions{PBF: fixture(t), Destination: filepath.Join(t.TempDir(), "region")}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := Import(ctx, o, func(p Progress) {
			if p.Phase == "checking" {
				close(entered)
				<-ctx.Done()
			}
		})
		done <- err
	}()
	<-entered
	if _, err := Import(t.Context(), o, nil); err == nil || !strings.Contains(err.Error(), "already being imported") {
		t.Errorf("parallel import should be rejected: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A retry reaches its context check, rather than a stale destination lock.
	if _, err := Import(ctx, o, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("destination was not released: %v", err)
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	return fixtureWithStreetName(t, "Teststraße")
}

func fixtureWithStreetName(t *testing.T, streetName string) string {
	return fixtureWithRoadClass(t, streetName, "residential")
}

func fixtureWithRoadClass(t *testing.T, streetName, roadClass string) string {
	t.Helper()
	uv := func(v uint64) []byte { return binary.AppendUvarint(nil, v) }
	field := func(n int, v uint64) []byte { return append(uv(uint64(n<<3)), uv(v)...) }
	message := func(n int, b []byte) []byte {
		out := uv(uint64(n<<3 | 2))
		out = append(out, uv(uint64(len(b)))...)
		return append(out, b...)
	}
	zig := func(v int64) uint64 { return uint64(v<<1) ^ uint64(v>>63) }
	strings := []string{"", "highway", roadClass, "name", streetName, "amenity", "cafe", "Café Test", "addr:street", "addr:housenumber", "7"}
	var table, group []byte
	for _, s := range strings {
		table = append(table, message(1, []byte(s))...)
	}
	for i := int64(1); i <= 3; i++ {
		node := field(1, zig(i))
		node = append(node, field(8, zig(480000000+i*10000))...)
		node = append(node, field(9, zig(120000000+i*10000))...)
		if i == 2 {
			node = append(node, message(2, []byte{3, 5, 8, 9})...)
			node = append(node, message(3, []byte{7, 6, 4, 10})...)
		}
		group = append(group, message(1, node)...)
	}
	way := field(1, 100)
	way = append(way, message(2, []byte{1, 3})...)
	way = append(way, message(3, []byte{2, 4})...)
	way = append(way, message(8, []byte{2, 2, 2})...)
	group = append(group, message(3, way)...)
	block := append(message(1, table), message(2, group)...)
	blob := message(1, block)
	header := append(message(1, []byte("OSMData")), field(3, uint64(len(blob)))...)
	out := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	out = append(out, header...)
	out = append(out, blob...)
	path := filepath.Join(t.TempDir(), "fixture.osm.pbf")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestRegionSurvivesWithoutSourceAndHasLocalMapSearchRoute(t *testing.T) {
	pbf := fixture(t)
	dest := filepath.Join(t.TempDir(), "region")
	o := ImportOptions{PBF: pbf, Destination: dest, Name: "Test", MinZoom: 14, MaxZoom: 14, TileMemoryBytes: 64 << 20}
	m, err := Import(t.Context(), o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Nodes != 3 || m.Edges != 4 {
		t.Fatalf("manifest: %+v", m)
	}
	notice, err := os.ReadFile(filepath.Join(dest, "NOTICE.txt"))
	if err != nil || !strings.Contains(string(notice), "OpenStreetMap contributors") || !strings.Contains(string(notice), "https://opendatacommons.org/licenses/odbl/1-0/") {
		t.Fatalf("portable region lost its attribution/data licence: %q, %v", notice, err)
	}
	if err = os.Remove(pbf); err != nil {
		t.Fatal(err)
	}
	e, err := Open(t.Context(), dest)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	results, err := e.Search(t.Context(), "cafe", 10)
	if err != nil || len(results) == 0 {
		t.Fatalf("search: %v %v", results, err)
	}
	r, err := e.Route(t.Context(), RouteRequest{From: osm.Coord{Lat: 48.001, Lon: 12.001}, To: osm.Coord{Lat: 48.003, Lon: 12.003}})
	if err != nil || len(r.Path) < 2 || r.DistanceM <= 0 {
		t.Fatalf("route: %+v %v", r, err)
	}
	z := 14
	x := int((12.002 + 180) / 360 * math.Exp2(float64(z)))
	lat := 48.002 * math.Pi / 180
	y := int((1 - math.Asinh(math.Tan(lat))/math.Pi) / 2 * math.Exp2(float64(z)))
	tile, err := e.Tile(t.Context(), z, x, y)
	if err != nil || len(tile.Features) == 0 {
		t.Fatalf("local tile: %v %v", tile, err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Search(t.Context(), "test", 1); err == nil {
		t.Fatal("query on closed engine succeeded")
	}
	if err = os.WriteFile(filepath.Join(dest, "search.jsonl"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(t.Context(), dest); err == nil {
		t.Fatal("accepted corrupt region")
	}
}
func TestCancelledAndBoundedImport(t *testing.T) {
	pbf := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	o := ImportOptions{PBF: pbf, Destination: filepath.Join(t.TempDir(), "region"), MinZoom: 14, MaxZoom: 14}
	if _, err := Import(ctx, o, nil); err == nil {
		t.Fatal("cancelled import succeeded")
	}
	o.MaxImportNodes = 1
	if _, err := Import(t.Context(), o, nil); err == nil {
		t.Fatal("node limit not enforced")
	}
	o.MaxImportNodes = 100
	if _, err := Import(t.Context(), o, nil); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
}

func TestMapImportFitsLargeTilesWithinTheConfiguredBudget(t *testing.T) {
	// A high-entropy name produces a large compressed tile. A 1,000-row
	// batch exceeds 16 MiB even though the tile itself easily fits the budget.
	random := rand.New(rand.NewPCG(1, 2))
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	name := make([]byte, 48<<10)
	for i := range name {
		name[i] = alphabet[random.IntN(len(alphabet))]
	}
	destination := filepath.Join(t.TempDir(), "region")
	var phases []Progress
	m, err := Import(t.Context(), ImportOptions{
		PBF: fixtureWithStreetName(t, string(name)), Destination: destination,
		MinZoom: 14, MaxZoom: 14, TileMemoryBytes: 16 << 20,
	}, func(p Progress) { phases = append(phases, p) })
	if err != nil {
		t.Fatal(err)
	}
	if m.Nodes != 3 || m.Edges != 4 {
		t.Fatalf("graph changed: %+v", m)
	}
	previous := 0.0
	published := false
	for _, p := range phases {
		if p.Fraction < previous || p.Fraction > 1 {
			t.Fatalf("nonmonotonic progress: %+v after %f", p, previous)
		}
		previous = p.Fraction
		if p.Phase == "map_published" {
			published = true
			if p.Fraction != 0.85 {
				t.Fatalf("publication progress: %+v", p)
			}
		}
	}
	if !published || previous != 1 {
		t.Fatalf("import never completed: %+v", phases)
	}
	engine, err := Open(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	streets := engine.router.AllStreets()
	if len(streets) != 1 || streets[0].Name != string(name) {
		t.Fatal("large street name was lost during import")
	}
}
func TestVectorDecoderRejectsMalformedData(t *testing.T) {
	for _, b := range [][]byte{{0xff}, {0x1a, 0xff}, {0x1f, 0x8b, 0}} {
		if _, err := DecodeVectorTile(b); err == nil {
			t.Fatalf("accepted %x", b)
		}
	}
}

func TestImportResumesCompletedGraphAndIsIdempotent(t *testing.T) {
	pbf := fixture(t)
	dest := filepath.Join(t.TempDir(), "region")
	o := ImportOptions{PBF: pbf, Destination: dest, MinZoom: 14, MaxZoom: 14}
	ctx, cancel := context.WithCancel(t.Context())
	_, err := Import(ctx, o, func(p Progress) {
		if p.Phase == "indexing_search" {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("cancelled import completed")
	}
	graph := dest + ".pending/graph.bin"
	before, err := os.Stat(graph)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Import(t.Context(), o, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(dest, "graph.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("completed graph was unnecessarily rebuilt")
	}
	if _, err := Import(t.Context(), o, nil); err != nil {
		t.Fatalf("retry after publication: %v", err)
	}
}

func TestImportResumesCompletedGraphAndSearchBeforeMapBuild(t *testing.T) {
	pbf := fixture(t)
	destination := filepath.Join(t.TempDir(), "region")
	stage := destination + ".pending"
	options := ImportOptions{PBF: pbf, Destination: destination, Name: "Resumed region", MinZoom: 14, MaxZoom: 14}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reachedMap := false
	_, err := Import(ctx, options, func(p Progress) {
		if p.Phase == "building_map" {
			reachedMap = true
			cancel()
		}
	})
	if !reachedMap || !errors.Is(err, context.Canceled) {
		t.Fatalf("map-phase cancellation: reached=%v, error=%v", reachedMap, err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("cancelled import published a region: %v", err)
	}

	type savedFile struct {
		info os.FileInfo
		data []byte
	}
	saved := make(map[string]savedFile)
	for i, name := range []string{"graph.bin", "graph.ready", "search.jsonl", "search.jsonl.idx", "search.ready"} {
		path := filepath.Join(stage, name)
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			t.Fatalf("completed checkpoint %s missing: %v", name, err)
		}
		// An old, explicit timestamp detects rewrites even on filesystems
		// whose modification-time resolution is coarser than a fixture import.
		stamp := time.Unix(1_000_000+int64(i), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		saved[name] = savedFile{info: info, data: data}
	}

	phases := make(map[string]bool)
	manifest, err := Import(t.Context(), options, func(p Progress) { phases[p.Phase] = true })
	if err != nil {
		t.Fatalf("resume after map-phase cancellation: %v", err)
	}
	if phases["building_graph"] || !phases["loading_graph"] || !phases["building_map"] || !phases["ready"] {
		t.Fatalf("resume skipped or repeated the wrong phases: %+v", phases)
	}
	if manifest.Name != options.Name || manifest.Nodes != 3 || manifest.Edges != 4 {
		t.Fatalf("graph metadata lost after closing the preparation phase: %+v", manifest)
	}
	for name, before := range saved {
		path := filepath.Join(destination, name)
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, before.data) || !after.ModTime().Equal(before.info.ModTime()) || !os.SameFile(before.info, after) {
			t.Fatalf("completed %s was rebuilt instead of resumed: %v", name, err)
		}
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("published import left its checkpoint directory: %v", err)
	}
	if err := os.Remove(pbf); err != nil {
		t.Fatal(err)
	}
	engine, err := Open(t.Context(), destination)
	if err != nil {
		t.Fatalf("resumed region cannot open without its source: %v", err)
	}
	defer engine.Close()
	results, err := engine.Search(t.Context(), "cafe", 10)
	if err != nil || len(results) != 1 || results[0].ID != 2 || results[0].Label != "Café Test" {
		t.Fatalf("resumed offline search: %+v, %v", results, err)
	}
	route, err := engine.Route(t.Context(), RouteRequest{From: osm.Coord{Lat: 48.001, Lon: 12.001}, To: osm.Coord{Lat: 48.003, Lon: 12.003}})
	if err != nil || len(route.Path) != 3 || route.DistanceM <= 0 || route.DurationS <= 0 {
		t.Fatalf("resumed offline route: %+v, %v", route, err)
	}
	z := options.MaxZoom
	x := int((12.002 + 180) / 360 * math.Exp2(float64(z)))
	latitude := 48.002 * math.Pi / 180
	y := int((1 - math.Asinh(math.Tan(latitude))/math.Pi) / 2 * math.Exp2(float64(z)))
	if tile, err := engine.Tile(t.Context(), z, x, y); err != nil || len(tile.Features) == 0 {
		t.Fatalf("resumed offline map: %+v, %v", tile, err)
	}
}
