package mobile

import (
	"strings"
	"unicode"
	"unicode/utf8"

	osm "simonwaldherr.de/go/osmmini/core"
)

// PlaceDetails contains source tags, not inferred addresses or interpreted
// opening hours. Missing fields remain absent in previously imported regions.
type PlaceDetails struct {
	Address      string            `json:"address,omitempty"`
	OpeningHours string            `json:"opening_hours,omitempty"`
	Phone        string            `json:"phone,omitempty"`
	Website      string            `json:"website,omitempty"`
	Emergency    *EmergencyDetails `json:"emergency,omitempty"`
}

// EmergencyDetails keeps source text, including any source-supplied units.
// It does not infer water availability, pressure or operational readiness.
type EmergencyDetails struct {
	Reference   string `json:"reference,omitempty"`
	HydrantType string `json:"hydrant_type,omitempty"`
	Diameter    string `json:"diameter,omitempty"`
	Pressure    string `json:"pressure,omitempty"`
	WaterSource string `json:"water_source,omitempty"`
	WaterVolume string `json:"water_volume,omitempty"`
}

func detailTag(key string) bool {
	switch key {
	case "opening_hours", "phone", "contact:phone", "website", "contact:website", "ref",
		"fire_hydrant:type", "fire_hydrant:diameter", "fire_hydrant:pressure",
		"fire_hydrant:water_source", "water_source", "water_tank:volume", "capacity":
		return true
	}
	return false
}

func boundedSourceText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return ""
	}
	return value
}

func detailValue(value string, limit int) string {
	value = boundedSourceText(value, limit)
	if strings.ContainsFunc(value, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
		return ""
	}
	return value
}

func cleanDetails(details *PlaceDetails) *PlaceDetails {
	if details == nil {
		return nil
	}
	value := PlaceDetails{
		Address: detailValue(details.Address, 1024), OpeningHours: detailValue(details.OpeningHours, 1024),
		Phone: detailValue(details.Phone, 128), Website: detailValue(details.Website, 2048),
		Emergency: cleanEmergencyDetails(details.Emergency),
	}
	if value == (PlaceDetails{}) {
		return nil
	}
	return &value
}

func cleanEmergencyDetails(details *EmergencyDetails) *EmergencyDetails {
	if details == nil {
		return nil
	}
	value := EmergencyDetails{
		Reference: detailValue(details.Reference, 256), HydrantType: detailValue(details.HydrantType, 256),
		Diameter: detailValue(details.Diameter, 256), Pressure: detailValue(details.Pressure, 256),
		WaterSource: detailValue(details.WaterSource, 256), WaterVolume: detailValue(details.WaterVolume, 256),
	}
	if value == (EmergencyDetails{}) {
		return nil
	}
	return &value
}

func emergencyDetailsFromTags(tags osm.Tags) *EmergencyDetails {
	category := emergencyCategory(tags)
	if category == "" {
		return nil
	}
	value := EmergencyDetails{Reference: tags["ref"]}
	switch category {
	case "fire_hydrant", "suction_point", "water_tank", "fire_water_pond":
		value.HydrantType = tags["fire_hydrant:type"]
		value.Diameter = tags["fire_hydrant:diameter"]
		value.Pressure = tags["fire_hydrant:pressure"]
		value.WaterSource = tags["water_source"]
		if detailValue(value.WaterSource, 256) == "" {
			value.WaterSource = tags["fire_hydrant:water_source"]
		}
		value.WaterVolume = tags["water_tank:volume"]
		if detailValue(value.WaterVolume, 256) == "" {
			value.WaterVolume = tags["capacity"]
		}
	}
	return cleanEmergencyDetails(&value)
}

func detailsFromTags(tags osm.Tags) *PlaceDetails {
	street := strings.TrimSpace(tags["addr:street"])
	if street == "" {
		street = strings.TrimSpace(tags["addr:place"])
	}
	street = strings.TrimSpace(street + " " + tags["addr:housenumber"])
	locality := strings.TrimSpace(tags["addr:postcode"] + " " + tags["addr:city"])
	address := street
	if locality != "" {
		if address != "" {
			address += ", "
		}
		address += locality
	}
	first := func(key, fallback string, limit int) string {
		if value := detailValue(tags[key], limit); value != "" {
			return value
		}
		return detailValue(tags[fallback], limit)
	}
	return cleanDetails(&PlaceDetails{
		Address: address, OpeningHours: tags["opening_hours"],
		Phone: first("phone", "contact:phone", 128), Website: first("website", "contact:website", 2048),
		Emergency: emergencyDetailsFromTags(tags),
	})
}
