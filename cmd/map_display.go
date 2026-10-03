package main

import "fmt"

// MapDisplaySettings selects the browser renderer independently of tile sources.
// Strings keep defaults unambiguous in older settings files with no such fields.
type MapDisplaySettings struct {
	Renderer string           `json:"renderer"`
	MicroMap MicroMapSettings `json:"micromap"`
}

type MicroMapSettings struct {
	Quality     string `json:"quality"`
	View        string `json:"view"`
	Buildings   string `json:"buildings"`
	Sky         string `json:"sky"`
	Interaction string `json:"interaction"`
}

func normalizeMapDisplay(v *MapDisplaySettings) {
	defaults := []struct {
		value    *string
		fallback string
	}{
		{&v.Renderer, "micromap"}, {&v.MicroMap.Quality, "balanced"},
		{&v.MicroMap.View, "flat"}, {&v.MicroMap.Buildings, "auto"},
		{&v.MicroMap.Sky, "auto"}, {&v.MicroMap.Interaction, "full"},
	}
	for _, field := range defaults {
		if *field.value == "" {
			*field.value = field.fallback
		}
	}
}

func validateMapDisplay(v MapDisplaySettings) error {
	fields := []struct {
		name, value string
		allowed     []string
	}{
		{"renderer", v.Renderer, []string{"micromap", "maplibre"}},
		{"micromap.quality", v.MicroMap.Quality, []string{"economy", "balanced", "sharp"}},
		{"micromap.view", v.MicroMap.View, []string{"flat", "perspective", "steep"}},
		{"micromap.buildings", v.MicroMap.Buildings, []string{"auto", "canvas"}},
		{"micromap.sky", v.MicroMap.Sky, []string{"auto", "off"}},
		{"micromap.interaction", v.MicroMap.Interaction, []string{"full", "simple"}},
	}
	for _, field := range fields {
		valid := false
		for _, allowed := range field.allowed {
			if field.value == allowed {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("ungültige Kartenanzeige: %s = %q", field.name, field.value)
		}
	}
	return nil
}
