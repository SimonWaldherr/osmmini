package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func TestActivityPresetsSelectObjectsRatherThanWholeTagKeys(t *testing.T) {
	for _, test := range []struct {
		tags    osm.Tags
		outdoor string
		cycling string
		travel  string
		all     string
	}{
		{osm.Tags{"natural": "peak"}, "peak", "", "", "peak"},
		{osm.Tags{"natural": "saddle"}, "saddle", "", "", "saddle"},
		{osm.Tags{"natural": "spring"}, "spring", "", "", "spring"},
		{osm.Tags{"natural": "cave_entrance"}, "cave_entrance", "", "", "cave_entrance"},
		{osm.Tags{"natural": "tree"}, "", "", "", ""},
		{osm.Tags{"natural": "water"}, "", "", "", ""},
		{osm.Tags{"tourism": "information", "information": "guidepost"}, "guidepost", "", "guidepost", "guidepost"},
		{osm.Tags{"tourism": "information", "information": "map"}, "information", "", "information", "information"},
		{osm.Tags{"information": "guidepost"}, "", "", "", ""},
		{osm.Tags{"shop": "bicycle"}, "", "bicycle_shop", "", "bicycle_shop"},
		{osm.Tags{"amenity": "bicycle_repair_station"}, "", "bicycle_repair_station", "", "bicycle_repair_station"},
		{osm.Tags{"shop": "supermarket"}, "", "supermarket", "", "supermarket"},
		{osm.Tags{"amenity": "compressed_air"}, "", "", "", "compressed_air"},
		{osm.Tags{"emergency": "defibrillator"}, "defibrillator", "", "", "defibrillator"},
		{osm.Tags{"emergency": "fire_hydrant"}, "", "", "", "fire_hydrant"},
		{osm.Tags{"highway": "emergency_access_point"}, "access_point", "", "", "access_point"},
		{osm.Tags{"railway": "station"}, "", "", "station", "station"},
		{osm.Tags{"railway": "halt"}, "", "", "station", "station"},
		{osm.Tags{"railway": "tram_stop"}, "", "", "tram_stop", "tram_stop"},
		{osm.Tags{"railway": "signal"}, "", "", "", ""},
		{osm.Tags{"highway": "bus_stop", "public_transport": "platform"}, "", "", "bus_stop", "bus_stop"},
		{osm.Tags{"public_transport": "station"}, "", "", "station", "station"},
		{osm.Tags{"public_transport": "platform"}, "", "", "public_transport_stop", "public_transport_stop"},
		{osm.Tags{"public_transport": "stop_position"}, "", "", "", ""},
		{osm.Tags{"aeroway": "aerodrome"}, "", "", "aerodrome", "aerodrome"},
		{osm.Tags{"aeroway": "terminal"}, "", "", "airport_terminal", "airport_terminal"},
		{osm.Tags{"aeroway": "runway"}, "", "", "", ""},
		{osm.Tags{"place": "village"}, "village", "village", "village", "village"},
	} {
		for _, preset := range []struct {
			value POIPreset
			want  string
		}{
			{POIPresetOutdoor, test.outdoor}, {POIPresetCycling, test.cycling}, {POIPresetTravel, test.travel}, {POIPresetAll, test.all},
		} {
			if got := presetCategory(test.tags, preset.value); got != preset.want {
				t.Errorf("%s with %v: got %q, want %q", preset.value, test.tags, got, preset.want)
			}
		}
	}
	// Existing Everyday imports retain their category vocabulary.
	if presetCategory(osm.Tags{"shop": "bicycle"}, POIPresetEveryday) != "bicycle" {
		t.Fatal("existing everyday category changed")
	}
}

func TestActivityPOIFallbackLabelsMatchTheirObjectTypes(t *testing.T) {
	for category, want := range map[string]string{
		"shelter":          "Unterstand",
		"wilderness_hut":   "Wildnishütte",
		"aerodrome":        "Flugplatz",
		"parking":          "Parkplatz",
		"parking_entrance": "Parkhauseinfahrt",
	} {
		if got := poiLabel(category); got != want {
			t.Errorf("%s: got %q, want %q", category, got, want)
		}
	}
}

func TestActivityPresetsImportSearchAliasesAndTilePOIsWithoutChangingRouting(t *testing.T) {
	pbf := presetFixture(t)
	var graphHash string
	for _, test := range []struct {
		preset POIPreset
		points map[string]string
		search map[string]int
	}{
		{POIPresetOutdoor, map[string]string{
			"node/16": "access_point", "node/21": "defibrillator", "node/30": "drinking_water", "node/34": "guidepost", "node/35": "peak", "node/36": "saddle", "node/37": "spring", "node/38": "cave_entrance", "node/39": "nature_reserve", "way/103": "wilderness_hut",
		}, map[string]int{"Trinkwasser": 1, "Gipfel": 1, "Schutzhütten": 2, "Camping": 2, "Fahrradwerkstatt": 0, "Hotels": 0, "Hydranten": 0}},
		{POIPresetCycling, map[string]string{
			"node/22": "supermarket", "node/30": "drinking_water", "node/40": "bicycle_repair_station", "node/41": "bicycle_rental", "node/42": "bicycle_parking", "node/43": "bicycle_shop", "way/102": "cafe",
		}, map[string]int{"Fahrradwerkstatt": 1, "Reparaturstation": 1, "Trinkwasser": 1, "Fahrradverleih": 1, "Rastplätze": 2, "Gipfel": 0, "Hotels": 0, "Hydranten": 0}},
		{POIPresetTravel, map[string]string{
			"node/44": "hotel", "node/45": "attraction", "node/46": "museum", "node/47": "station", "node/48": "station", "node/49": "bus_stop", "node/50": "aerodrome", "node/51": "airport_terminal", "node/60": "parking", "node/61": "pharmacy", "node/62": "ferry_terminal", "way/104": "public_transport_stop",
		}, map[string]int{"Hotels": 1, "Sehenswürdigkeiten": 2, "Bahnhof": 2, "Parkplatz": 1, "Fahrradwerkstatt": 0, "Gipfel": 0, "Hydranten": 0}},
		{POIPresetAll, map[string]string{
			"node/10": "fire_hydrant", "node/34": "guidepost", "node/35": "peak", "node/40": "bicycle_repair_station", "node/43": "bicycle_shop", "node/44": "hotel", "node/48": "station", "node/49": "bus_stop", "node/51": "airport_terminal", "way/103": "wilderness_hut", "way/104": "public_transport_stop",
		}, map[string]int{"Trinkwasser": 1, "Gipfel": 1, "Fahrradwerkstatt": 1, "Hotels": 1}},
	} {
		t.Run(string(test.preset), func(t *testing.T) {
			options := ImportOptions{PBF: pbf, Destination: filepath.Join(t.TempDir(), "region"), POIPreset: test.preset, MinZoom: 14, MaxZoom: 14, TileMemoryBytes: 16 << 20}
			manifest, err := Import(t.Context(), options, nil)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.ImportRevision != 8 || manifest.POIPreset != test.preset {
				t.Fatalf("import metadata: %+v", manifest)
			}
			if graphHash == "" {
				graphHash = manifest.Checksums["graph.bin"]
			} else if manifest.Checksums["graph.bin"] != graphHash {
				t.Fatal("activity preset changed the routing graph")
			}
			engine, err := Open(t.Context(), options.Destination)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			for query, count := range test.search {
				found, err := engine.Search(t.Context(), query, 100)
				if err != nil || len(found) != count {
					t.Errorf("search %q: %+v, %v (want %d)", query, found, err, count)
				}
			}
			for _, query := range []string{"Teststraße", "Testdorf", "Blaues Haus", "Adressmarke", "Zufahrt Nord", "Hauptstraße 7", "Hauptstraße 8", "Bergstraße 1", "Hofberg 2", "Hofberg 3", "Hofberg 4", "Grünes Haus"} {
				found, err := engine.Search(t.Context(), query, 100)
				if err != nil || len(found) != 1 {
					t.Errorf("shared address/street/place %q: %+v, %v", query, found, err)
				}
			}
			if test.preset != POIPresetAll {
				for _, query := range []string{"Hofberg 2", "Hofberg 3"} {
					found, err := engine.Search(t.Context(), query, 100)
					if err != nil || len(found) != 1 || found[0].Category != "" || found[0].Label != query {
						t.Fatalf("filtered place-based address %q: %+v, %v", query, found, err)
					}
				}
			}
			if test.preset == POIPresetCycling || test.preset == POIPresetTravel {
				found, err := engine.Search(t.Context(), "Testgipfel", 100)
				if err != nil || len(found) != 0 {
					t.Fatalf("excluded natural POI retained its name: %+v, %v", found, err)
				}
			}
			if test.preset == POIPresetOutdoor {
				found, err := engine.Search(t.Context(), "Hauptstraße 7", 100)
				if err != nil || len(found) != 1 || found[0].Category != "" || found[0].Label != "Hauptstraße 7" {
					t.Fatalf("excluded cafe lost its address or retained its POI identity: %+v, %v", found, err)
				}
			} else if test.preset != POIPresetAll {
				found, err := engine.Search(t.Context(), "cafe", 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, result := range found {
					if result.Details != nil && result.Details.Emergency != nil {
						t.Fatalf("cafe retained unselected emergency details: %+v", result)
					}
				}
			}
			x, y := poiTile(SearchResult{Coordinate: osm.Coord{Lat: 48.0005, Lon: 12.0005}}, 14)
			tile, err := engine.Tile(t.Context(), 14, x, y)
			if err != nil {
				t.Fatal(err)
			}
			points := map[string]string{}
			for _, feature := range tile.Features {
				if feature.Layer == "poi" {
					points[feature.SourceIdentity] = feature.Class
				}
			}
			for identity, category := range test.points {
				if points[identity] != category {
					t.Errorf("tile %s: got %q, want %q", identity, points[identity], category)
				}
			}
			for _, identity := range []string{"node/52", "node/53", "node/54", "node/55", "node/56", "node/24", "node/25"} {
				if points[identity] != "" {
					t.Errorf("tile contains non-POI %s: %q", identity, points[identity])
				}
			}
			for _, identity := range []string{"node/35", "node/40", "node/44", "way/103", "way/104"} {
				if test.points[identity] == "" && points[identity] != "" {
					t.Errorf("tile contains excluded POI %s: %q", identity, points[identity])
				}
			}
		})
	}
}

func TestOlderImportRevisionsRemainReadableButCannotBeReused(t *testing.T) {
	options := ImportOptions{PBF: presetFixture(t), Destination: filepath.Join(t.TempDir(), "region"), POIPreset: POIPresetAll, MinZoom: 14, MaxZoom: 14, TileMemoryBytes: 16 << 20}
	if _, err := Import(t.Context(), options, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(options.Destination, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["import_revision"] != float64(8) {
		t.Fatalf("missing published revision: %v", manifest["import_revision"])
	}
	for _, revision := range []int{0, 7, 9} {
		if revision == 0 {
			delete(manifest, "import_revision")
		} else {
			manifest["import_revision"] = revision
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		engine, err := Open(t.Context(), options.Destination)
		if err != nil {
			t.Fatalf("compatible format with revision %d stopped opening: %v", revision, err)
		}
		engine.Close()
		if _, err := Import(t.Context(), options, nil); err == nil || !strings.Contains(err.Error(), "another region revision") {
			t.Fatalf("revision %d reused: %v", revision, err)
		}
	}
}
