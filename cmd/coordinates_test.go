package main

import (
	"strings"
	"testing"
)

func TestParseLatLonRejectsNonfiniteAndOutOfRangeCoordinates(t *testing.T) {
	for _, input := range []string{"NaN,12", "48,NaN", "NaN NaN", "+Inf,12", "48,-Inf", "90.01,12", "48,-180.01", "48", "48,12,1"} {
		if coordinate, ok := parseLatLon(input); ok {
			t.Errorf("accepted %q: %+v", input, coordinate)
		}
	}
	for _, test := range []struct {
		input    string
		lat, lon float64
	}{
		{"48.5,12.25", 48.5, 12.25}, {"  -90 , 180  ", -90, 180}, {"90 -180", 90, -180}, {"0,0", 0, 0},
	} {
		got, ok := parseLatLon(test.input)
		if !ok || got.Lat != test.lat || got.Lon != test.lon {
			t.Errorf("%q: %+v, %v", test.input, got, ok)
		}
	}
}

func TestCLIRouteRejectsInvalidCoordinatesAndDependenciesBeforeImport(t *testing.T) {
	for _, scenario := range []struct {
		name, from, to string
		events         bool
		message        string
	}{
		{"nonfinite start", "NaN,12", "48,12", false, "invalid --from"},
		{"nonfinite target", "48,12", "48,NaN", false, "invalid --to"},
		{"missing territory layer", "48,12", "49,13", true, "requires --territory-layer"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			args := []string{"route", "-pbf", "missing.osm.pbf", "-from", scenario.from, "-to", scenario.to}
			if scenario.events {
				args = append(args, "-territory-events")
			}
			_, stderr, err := runCLI(t, t.TempDir(), args...)
			if err == nil || !strings.Contains(stderr, scenario.message) {
				t.Fatalf("validation: %v, %s", err, stderr)
			}
		})
	}
}
