package osmmini

import "testing"

const samplePointFC = `{"type":"FeatureCollection","features":[
  {"type":"Feature","properties":{"name":"A"},"geometry":{"type":"Point","coordinates":[12.0,48.0]}},
  {"type":"Feature","properties":{"name":"B"},"geometry":{"type":"Point","coordinates":[12.1,48.1]}}
]}`

const sampleMixedFC = `{"type":"FeatureCollection","features":[
  {"type":"Feature","properties":{},"geometry":{"type":"Point","coordinates":[12.0,48.0]}},
  {"type":"Feature","properties":{},"geometry":{"type":"LineString","coordinates":[[12.0,48.0],[12.1,48.1]]}}
]}`

const samplePolygonFC = `{"type":"FeatureCollection","features":[
  {"type":"Feature","properties":{"id":"p1"},"geometry":{"type":"Polygon","coordinates":[[[12.0,48.0],[12.1,48.0],[12.1,48.1],[12.0,48.1],[12.0,48.0]]]}}
]}`

func TestParseImportedLayerAcceptsPointsAndLines(t *testing.T) {
	layer, err := ParseImportedLayer("addresses", []byte(samplePointFC))
	if err != nil {
		t.Fatalf("ParseImportedLayer: %v", err)
	}
	if layer.FeatureCount != 2 {
		t.Fatalf("FeatureCount = %d, want 2", layer.FeatureCount)
	}
	if len(layer.GeometryTypes) != 1 || layer.GeometryTypes[0] != "Point" {
		t.Fatalf("GeometryTypes = %v, want [Point]", layer.GeometryTypes)
	}
	if layer.IsPolygonOnly() {
		t.Fatal("a point layer must not be polygon-only")
	}

	mixed, err := ParseImportedLayer("mixed", []byte(sampleMixedFC))
	if err != nil {
		t.Fatalf("ParseImportedLayer(mixed): %v", err)
	}
	if len(mixed.GeometryTypes) != 2 {
		t.Fatalf("GeometryTypes = %v, want 2 distinct types", mixed.GeometryTypes)
	}
	if mixed.IsPolygonOnly() {
		t.Fatal("a mixed point/line layer must not be polygon-only")
	}
}

func TestParseImportedLayerDetectsPolygonOnly(t *testing.T) {
	layer, err := ParseImportedLayer("areas", []byte(samplePolygonFC))
	if err != nil {
		t.Fatalf("ParseImportedLayer: %v", err)
	}
	if !layer.IsPolygonOnly() {
		t.Fatal("a polygon-only layer must report IsPolygonOnly() == true")
	}
	// A polygon-only ImportedLayer's bytes must also parse as a Territory
	// layer unchanged -- this is the contract the caller relies on to
	// additionally register it with TerritoryStore.
	territories, err := ParseTerritoriesGeoJSON(layer.GeoJSON)
	if err != nil {
		t.Fatalf("ParseTerritoriesGeoJSON on a polygon-only imported layer: %v", err)
	}
	if len(territories) != 1 {
		t.Fatalf("got %d territories, want 1", len(territories))
	}
}

func TestParseImportedLayerRejectsMalformedInput(t *testing.T) {
	cases := map[string]string{
		"not json":               `not json`,
		"wrong top-level type":   `{"type":"Polygon","coordinates":[]}`,
		"empty collection":       `{"type":"FeatureCollection","features":[]}`,
		"feature missing geom":   `{"type":"Feature","properties":{}}`,
		"malformed feature list": `{"type":"FeatureCollection","features":"nope"}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseImportedLayer("x", []byte(data)); err == nil {
				t.Fatalf("expected an error for input %q", data)
			}
		})
	}
}

func TestImportedLayerStoreLoadReplaceAndRemove(t *testing.T) {
	store := NewImportedLayerStore()
	if got := store.Layers(); len(got) != 0 {
		t.Fatalf("new store has %d layers, want 0", len(got))
	}

	if _, err := store.LoadLayer("addresses", []byte(samplePointFC)); err != nil {
		t.Fatalf("LoadLayer: %v", err)
	}
	if got := store.Layer("addresses"); got == nil || got.FeatureCount != 2 {
		t.Fatalf("Layer(addresses) = %+v, want a 2-feature layer", got)
	}

	// Loading the same name again replaces it, not merges.
	if _, err := store.LoadLayer("addresses", []byte(samplePolygonFC)); err != nil {
		t.Fatalf("LoadLayer (replace): %v", err)
	}
	if got := store.Layer("addresses"); got == nil || !got.IsPolygonOnly() {
		t.Fatalf("Layer(addresses) after replace = %+v, want the polygon layer", got)
	}
	if got := store.Layers(); len(got) != 1 {
		t.Fatalf("Layers() = %d, want 1 (replace must not add a second entry)", len(got))
	}

	// An invalid replacement must not clobber the existing good layer.
	if _, err := store.LoadLayer("addresses", []byte(`not json`)); err == nil {
		t.Fatal("expected an error loading malformed GeoJSON")
	}
	if got := store.Layer("addresses"); got == nil || !got.IsPolygonOnly() {
		t.Fatal("a failed LoadLayer must not replace the previously loaded layer")
	}

	store.Remove("addresses")
	if got := store.Layer("addresses"); got != nil {
		t.Fatalf("Layer(addresses) after Remove = %+v, want nil", got)
	}
	if got := store.Layers(); len(got) != 0 {
		t.Fatalf("Layers() after Remove = %d, want 0", len(got))
	}
}
