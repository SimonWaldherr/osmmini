package mobile

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func TestPOIPresetCategoryRulesDoNotTreatEmergencyVehicleAccessAsAnObject(t *testing.T) {
	for _, test := range []struct {
		tags     osm.Tags
		everyday string
		fire     string
		all      string
	}{
		{osm.Tags{"emergency": "fire_hydrant"}, "", "fire_hydrant", "fire_hydrant"},
		{osm.Tags{"emergency": "fire_hydrant", "amenity": "cafe"}, "cafe", "fire_hydrant", "cafe"},
		{osm.Tags{"amenity": "fire_station"}, "fire_station", "fire_station", "fire_station"},
		{osm.Tags{"amenity": "restaurant"}, "restaurant", "", "restaurant"},
		{osm.Tags{"shop": "supermarket"}, "supermarket", "", "supermarket"},
		{osm.Tags{"place": "village"}, "village", "village", "village"},
		{osm.Tags{"highway": "emergency_access_point"}, "", "access_point", "access_point"},
		{osm.Tags{"highway": "bus_stop"}, "", "", "bus_stop"},
		{osm.Tags{"healthcare": "hospital"}, "", "", ""},
		{osm.Tags{"emergency": "yes"}, "", "", ""},
		{osm.Tags{"emergency": "no"}, "", "", ""},
		{osm.Tags{"emergency": "designated", "amenity": "fuel"}, "fuel", "fuel", "fuel"},
	} {
		for _, preset := range []struct {
			value POIPreset
			want  string
		}{
			{POIPresetEveryday, test.everyday}, {POIPresetFirefighting, test.fire}, {POIPresetAll, test.all},
		} {
			if got := presetCategory(test.tags, preset.value); got != preset.want {
				t.Errorf("%s with %v: got %q, want %q", preset.value, test.tags, got, preset.want)
			}
		}
	}
}

// This independently encoded PBF exercises both node and way POIs next to one
// small routable street, rather than testing only the classification function.
func presetFixture(t *testing.T) string {
	t.Helper()
	uv := func(v uint64) []byte { return binary.AppendUvarint(nil, v) }
	field := func(n int, v uint64) []byte { return append(uv(uint64(n<<3)), uv(v)...) }
	message := func(n int, b []byte) []byte {
		out := uv(uint64(n<<3 | 2))
		out = append(out, uv(uint64(len(b)))...)
		return append(out, b...)
	}
	zig := func(v int64) uint64 { return uint64(v<<1) ^ uint64(v>>63) }
	type object struct {
		id   int64
		tags osm.Tags
		refs []int64
	}
	objects := []object{
		{1, nil, nil}, {2, nil, nil}, {3, nil, nil},
		{10, osm.Tags{"emergency": "fire_hydrant", "ref": "H123", "fire_hydrant:type": "underground", "fire_hydrant:diameter": "100", "fire_hydrant:pressure": "6 bar", "water_source": "main", "water_tank:volume": "20 m3"}, nil},
		{11, osm.Tags{"emergency": "fire_hydrant", "name": "Nordhydrant", "ref": "N234"}, nil},
		{12, osm.Tags{"amenity": "cafe", "name": "Café Fenster", "brand": "Kaffeemarke", "addr:street": "Hauptstraße", "addr:housenumber": "7"}, nil},
		{13, osm.Tags{"amenity": "cafe", "name": "Café OhneAdresse"}, nil},
		{14, osm.Tags{"amenity": "fire_station"}, nil},
		{15, osm.Tags{"place": "village", "name": "Testdorf"}, nil},
		{16, osm.Tags{"highway": "emergency_access_point", "ref": "R999"}, nil},
		{17, osm.Tags{"emergency": "yes", "name": "Keine Einsatzstelle"}, nil},
		{18, osm.Tags{"amenity": "cafe", "emergency": "fire_hydrant", "ref": "M123"}, nil},
		{21, osm.Tags{"emergency": "defibrillator", "ref": "A42"}, nil},
		{22, osm.Tags{"shop": "supermarket", "name": "Testladen"}, nil},
		{23, osm.Tags{"healthcare": "hospital", "name": "NurHealthcare"}, nil},
		{24, osm.Tags{"name": "Blaues Haus", "brand": "Adressmarke", "addr:street": "Hauptstraße", "addr:housenumber": "9"}, nil},
		{25, osm.Tags{"name": "Zufahrt Nord", "emergency": "yes", "addr:street": "Hauptstraße", "addr:housenumber": "10"}, nil},
		{26, osm.Tags{"shop": "florist", "name": "Blumenladen Hofberg", "addr:place": "Hofberg", "addr:housenumber": "2"}, nil},
		{27, osm.Tags{"name": "Grünes Haus", "addr:street": " ", "addr:place": "Hofberg", "addr:housenumber": "4"}, nil},
		{30, osm.Tags{"amenity": "drinking_water"}, nil},
		{31, osm.Tags{"amenity": "toilets"}, nil},
		{32, osm.Tags{"amenity": "shelter"}, nil},
		{33, osm.Tags{"tourism": "camp_site"}, nil},
		{34, osm.Tags{"tourism": "information", "information": "guidepost"}, nil},
		{35, osm.Tags{"natural": "peak", "name": "Testgipfel", "addr:street": "Bergstraße", "addr:housenumber": "1"}, nil},
		{36, osm.Tags{"natural": "saddle"}, nil},
		{37, osm.Tags{"natural": "spring"}, nil},
		{38, osm.Tags{"natural": "cave_entrance"}, nil},
		{39, osm.Tags{"leisure": "nature_reserve"}, nil},
		{40, osm.Tags{"amenity": "bicycle_repair_station"}, nil},
		{41, osm.Tags{"amenity": "bicycle_rental"}, nil},
		{42, osm.Tags{"amenity": "bicycle_parking"}, nil},
		{43, osm.Tags{"shop": "bicycle"}, nil},
		{44, osm.Tags{"tourism": "hotel"}, nil},
		{45, osm.Tags{"tourism": "attraction"}, nil},
		{46, osm.Tags{"tourism": "museum"}, nil},
		{47, osm.Tags{"railway": "station"}, nil},
		{48, osm.Tags{"railway": "halt"}, nil},
		{49, osm.Tags{"highway": "bus_stop", "public_transport": "platform"}, nil},
		{50, osm.Tags{"aeroway": "aerodrome"}, nil},
		{51, osm.Tags{"aeroway": "terminal"}, nil},
		{52, osm.Tags{"natural": "tree", "name": "Kein Baum-POI"}, nil},
		{53, osm.Tags{"railway": "signal", "name": "Kein Signal-POI"}, nil},
		{54, osm.Tags{"aeroway": "runway", "name": "Kein Startbahn-POI"}, nil},
		{55, osm.Tags{"public_transport": "stop_position", "name": "Keine Fahrzeugposition"}, nil},
		{56, osm.Tags{"information": "guidepost", "name": "Kein isolierter Informationstag"}, nil},
		{57, osm.Tags{"tourism": "caravan_site"}, nil},
		{58, osm.Tags{"tourism": "picnic_site"}, nil},
		{59, osm.Tags{"leisure": "park"}, nil},
		{60, osm.Tags{"amenity": "parking"}, nil},
		{61, osm.Tags{"amenity": "pharmacy"}, nil},
		{62, osm.Tags{"amenity": "ferry_terminal"}, nil},
		{100, osm.Tags{"highway": "residential", "name": "Teststraße"}, []int64{1, 2, 3}},
		{101, osm.Tags{"emergency": "fire_water_pond", "natural": "water", "water_tank:volume": "600 m3"}, []int64{1, 2, 3, 1}},
		{102, osm.Tags{"amenity": "cafe", "name": "FlächenCafé", "addr:street": "Hauptstraße", "addr:housenumber": "8"}, []int64{1, 2, 3, 1}},
		{103, osm.Tags{"tourism": "wilderness_hut"}, []int64{1, 2, 3, 1}},
		{104, osm.Tags{"public_transport": "platform"}, []int64{1, 2, 3}},
		{105, osm.Tags{"shop": "florist", "name": "Gärtnerei Hofberg", "addr:place": "Hofberg", "addr:housenumber": "3"}, []int64{1, 2, 3, 1}},
	}
	stringsTable := []string{""}
	indexes := map[string]uint64{"": 0}
	addString := func(value string) uint64 {
		if index, ok := indexes[value]; ok {
			return index
		}
		index := uint64(len(stringsTable))
		indexes[value] = index
		stringsTable = append(stringsTable, value)
		return index
	}
	var group []byte
	for _, object := range objects {
		var data []byte
		if object.refs == nil {
			data = field(1, zig(object.id))
		} else {
			data = field(1, uint64(object.id))
		}
		keys := make([]string, 0, len(object.tags))
		for key := range object.tags {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		var tags, values []byte
		for _, key := range keys {
			tags = append(tags, uv(addString(key))...)
			values = append(values, uv(addString(object.tags[key]))...)
		}
		if len(tags) > 0 {
			data = append(data, message(2, tags)...)
			data = append(data, message(3, values)...)
		}
		if object.refs == nil {
			lat, lon := 48.0003+float64((object.id-10)%10)*0.00024, 12.0006
			if object.id >= 20 {
				lon = 12.0018
			}
			switch object.id {
			case 1:
				lat, lon = 48, 12
			case 2:
				lat, lon = 48.003, 12
			case 3:
				lat, lon = 48.003, 12.003
			}
			data = append(data, field(8, zig(int64(math.Round(lat*1e7))))...)
			data = append(data, field(9, zig(int64(math.Round(lon*1e7))))...)
			group = append(group, message(1, data)...)
		} else {
			var refs []byte
			var previous int64
			for _, id := range object.refs {
				refs = append(refs, uv(zig(id-previous))...)
				previous = id
			}
			data = append(data, message(8, refs)...)
			group = append(group, message(3, data)...)
		}
	}
	var table []byte
	for _, value := range stringsTable {
		table = append(table, message(1, []byte(value))...)
	}
	block := append(message(1, table), message(2, group)...)
	blob := message(1, block)
	header := append(message(1, []byte("OSMData")), field(3, uint64(len(blob)))...)
	out := append(binary.BigEndian.AppendUint32(nil, uint32(len(header))), header...)
	out = append(out, blob...)
	path := filepath.Join(t.TempDir(), "presets.osm.pbf")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPOIPresetsSeparateImportedSearchAndMapPOIsWithoutChangingTheRoadGraph(t *testing.T) {
	pbf := presetFixture(t)
	var graphHash string
	for _, preset := range []POIPreset{POIPresetEveryday, POIPresetFirefighting, POIPresetAll} {
		t.Run(string(preset), func(t *testing.T) {
			options := ImportOptions{PBF: pbf, Destination: filepath.Join(t.TempDir(), "region"), POIPreset: preset, MinZoom: 14, MaxZoom: 14, TileMemoryBytes: 16 << 20}
			manifest, err := Import(t.Context(), options, nil)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.POIPreset != preset {
				t.Fatalf("manifest preset %q", manifest.POIPreset)
			}
			if graphHash == "" {
				graphHash = manifest.Checksums["graph.bin"]
			} else if manifest.Checksums["graph.bin"] != graphHash {
				t.Fatal("POI preset changed the routing graph")
			}
			engine, err := Open(t.Context(), options.Destination)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			search := func(query string, count int) []SearchResult {
				t.Helper()
				found, err := engine.Search(t.Context(), query, 100)
				if err != nil || len(found) != count {
					t.Fatalf("search %q: %+v, %v (want %d)", query, found, err, count)
				}
				return found
			}
			search("Teststraße", 1)
			search("Testdorf", 1)
			search("Feuerwehr", 1)
			if result := search("Blaues Haus", 1)[0]; result.Category != "" || result.Label != "Blaues Haus" {
				t.Fatalf("ordinary named address changed: %+v", result)
			}
			search("Adressmarke", 1)
			search("Zufahrt Nord", 1) // emergency=yes is access, not a filtered POI.
			search("Grünes Haus", 1)
			for _, query := range []string{"Hofberg 2", "Hofberg 3", "Hofberg 4"} {
				found := search(query, 1)[0]
				if found.Details == nil || found.Details.Address != query {
					t.Fatalf("place-based address %q: %+v", query, found)
				}
				if preset == POIPresetFirefighting && query != "Hofberg 4" && (found.Label != query || found.Category != "") {
					t.Fatalf("filtered POI kept its identity in a place-based address: %+v", found)
				}
			}
			hydrants := 2
			if preset == POIPresetEveryday {
				hydrants = 0
			}
			if preset == POIPresetFirefighting {
				hydrants = 3
			}
			search("Hydranten", hydrants)
			if preset != POIPresetEveryday {
				found := search("H123", 1)[0]
				if found.Label != "Hydrant H123" || found.Details == nil || found.Details.Emergency == nil || found.Details.Emergency.Diameter != "100" || found.Details.Emergency.Pressure != "6 bar" {
					t.Fatalf("hydrant source data: %+v", found)
				}
				search("N234", 1) // Named hydrants also retain their asset reference.
				search("Rettungspunkte", 1)
				search("Defibrillatoren", 1)
			} else {
				search("Rettungspunkte", 0)
				search("Defibrillatoren", 0)
			}
			address := search("Hauptstraße 7", 1)[0]
			wayAddress := search("Hauptstraße 8", 1)[0]
			if preset == POIPresetFirefighting {
				search("OhneAdresse", 0)
				search("Kaffeemarke", 0)
				search("FlächenCafé", 0)
				if address.Category != "" || address.Label != "Hauptstraße 7" || wayAddress.Category != "" || wayAddress.Kind != "way" {
					t.Fatalf("filtered POI leaked into address: %+v / %+v", address, wayAddress)
				}
			} else {
				search("OhneAdresse", 1)
				search("Kaffeemarke", 1)
				search("FlächenCafé", 1)
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
			if preset == POIPresetEveryday {
				if points["node/10"] != "" || points["node/16"] != "" || points["way/101"] != "" || points["node/12"] != "cafe" {
					t.Fatalf("everyday tile: %v", points)
				}
			} else if points["node/10"] != "fire_hydrant" || points["node/16"] != "access_point" || points["way/101"] != "fire_water_pond" {
				t.Fatalf("emergency tile: %v", points)
			}
			if preset == POIPresetFirefighting && (points["node/12"] != "" || points["way/102"] != "") {
				t.Fatal("address-only records became map POIs")
			}
		})
	}
}

func TestPOIPresetValidationAndPublishedOrPendingRevisionsNeverMix(t *testing.T) {
	pbf := presetFixture(t)
	options := ImportOptions{PBF: pbf, Destination: filepath.Join(t.TempDir(), "region"), POIPreset: "unknown", MinZoom: 14, MaxZoom: 14, TileMemoryBytes: 16 << 20}
	if _, err := Import(t.Context(), options, nil); err == nil || !strings.Contains(err.Error(), "poi_preset") {
		t.Fatalf("unknown preset accepted: %v", err)
	}
	if _, err := os.Stat(options.Destination + ".pending"); !os.IsNotExist(err) {
		t.Fatal("unknown preset wrote a workspace")
	}
	options.POIPreset = POIPresetFirefighting
	ctx, cancel := context.WithCancel(t.Context())
	_, err := Import(ctx, options, func(p Progress) {
		if p.Phase == "building_map" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("checkpoint cancellation: %v", err)
	}
	source := filepath.Join(options.Destination+".pending", "source")
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	options.POIPreset = POIPresetEveryday
	if _, err := Import(t.Context(), options, nil); err == nil || !strings.Contains(err.Error(), "another source/options") {
		t.Fatalf("mismatched checkpoint resumed: %v", err)
	}
	if after, err := os.ReadFile(source); err != nil || !bytes.Equal(before, after) {
		t.Fatal("mismatched checkpoint was modified")
	}
	options.POIPreset = POIPresetFirefighting
	for _, revision := range []string{"7", "9"} {
		fields := strings.Split(string(before), ":")
		if len(fields) != 6 || fields[4] != "8" {
			t.Fatalf("unexpected checkpoint signature: %q", before)
		}
		fields[4] = revision
		other := []byte(strings.Join(fields, ":"))
		if err := os.WriteFile(source, other, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Import(t.Context(), options, nil); err == nil || !strings.Contains(err.Error(), "another source/options/import version") {
			t.Fatalf("checkpoint revision %s resumed: %v", revision, err)
		}
		if after, err := os.ReadFile(source); err != nil || !bytes.Equal(other, after) {
			t.Fatal("incompatible checkpoint was modified")
		}
	}
	if err := os.WriteFile(source, before, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := Import(t.Context(), options, nil)
	if err != nil || manifest.POIPreset != options.POIPreset {
		t.Fatalf("same preset could not resume: %+v, %v", manifest, err)
	}
	if _, err := Import(t.Context(), options, nil); err != nil {
		t.Fatalf("same preset could not reuse: %v", err)
	}
	options.POIPreset = POIPresetEveryday
	if _, err := Import(t.Context(), options, nil); err == nil {
		t.Fatal("published firefighting region reused as everyday")
	}
	manifestPath := filepath.Join(options.Destination, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "poi_preset")
	data, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(t.Context(), options.Destination)
	if err != nil {
		t.Fatalf("legacy manifest stopped opening: %v", err)
	}
	opened.Close()
	options.POIPreset = POIPresetAll
	if _, err := Import(t.Context(), options, nil); err == nil {
		t.Fatal("legacy manifest implicitly reused as explicit all")
	}
}

func TestEmergencyDetailsKeepRawUnitsAndBoundEveryField(t *testing.T) {
	value := detailsFromTags(osm.Tags{"emergency": "fire_hydrant", "ref": "H123", "fire_hydrant:type": "underground", "fire_hydrant:diameter": "100", "fire_hydrant:pressure": "suction", "water_source": "pond", "fire_hydrant:water_source": "main", "water_tank:volume": "20 m3", "capacity": "1000"})
	if value == nil || value.Emergency == nil {
		t.Fatal("emergency data missing")
	}
	want := EmergencyDetails{Reference: "H123", HydrantType: "underground", Diameter: "100", Pressure: "suction", WaterSource: "pond", WaterVolume: "20 m3"}
	if *value.Emergency != want {
		t.Fatalf("raw source values changed: %+v", value.Emergency)
	}
	for _, input := range []string{strings.Repeat("a", 257), "a\nb", "a\u200bb", string([]byte{0xff})} {
		if cleanEmergencyDetails(&EmergencyDetails{Reference: input, HydrantType: input, Diameter: input, Pressure: input, WaterSource: input, WaterVolume: input}) != nil {
			t.Fatalf("invalid emergency data accepted: %q", input)
		}
	}
	limit := strings.Repeat("ä", 128)
	if cleanEmergencyDetails(&EmergencyDetails{Reference: limit, HydrantType: limit, Diameter: limit, Pressure: limit, WaterSource: limit, WaterVolume: limit}) == nil {
		t.Fatal("UTF8 byte boundary rejected")
	}
	if detailsFromTags(osm.Tags{"amenity": "cafe", "ref": "H123", "fire_hydrant:type": "underground"}) != nil {
		t.Fatal("technical details attached to a non-emergency object")
	}
	assembly := detailsFromTags(osm.Tags{"emergency": "assembly_point", "ref": "S1", "capacity": "100 persons"})
	if assembly == nil || assembly.Emergency == nil || assembly.Emergency.WaterVolume != "" {
		t.Fatal("assembly capacity misrepresented as water volume")
	}
}
