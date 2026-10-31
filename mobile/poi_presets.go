package mobile

import (
	"fmt"
	"strings"

	osm "simonwaldherr.de/go/osmmini/core"
)

// POIPreset selects the searchable points of interest prepared for a region.
// Roads, addresses, settlements and the basemap are independent of this choice.
type POIPreset string

const (
	POIPresetEveryday     POIPreset = "everyday"
	POIPresetFirefighting POIPreset = "firefighting"
	POIPresetOutdoor      POIPreset = "outdoor"
	POIPresetCycling      POIPreset = "cycling"
	POIPresetTravel       POIPreset = "travel"
	POIPresetAll          POIPreset = "all"
)

func (p POIPreset) normalized() (POIPreset, error) {
	if p == "" {
		return POIPresetAll, nil // Older SDKs supplied no preset.
	}
	switch p {
	case POIPresetEveryday, POIPresetFirefighting, POIPresetOutdoor, POIPresetCycling, POIPresetTravel, POIPresetAll:
		return p, nil
	default:
		return "", fmt.Errorf("unknown poi_preset %q (want everyday, firefighting, outdoor, cycling, travel or all)", p)
	}
}

func searchPOIKey(key string) bool {
	switch key {
	case "amenity", "shop", "tourism", "leisure", "office", "emergency", "place", "highway", "natural", "railway", "public_transport", "aeroway", "information":
		return true
	}
	return false
}

// emergency=yes/no on a road describes vehicle access, not an emergency object.
func emergencyCategory(tags osm.Tags) string {
	if tags["highway"] == "emergency_access_point" {
		return "access_point"
	}
	value := strings.TrimSpace(tags["emergency"])
	switch value {
	case "", "yes", "no", "designated", "destination", "permissive", "private":
		return ""
	}
	return value
}

func presetCategory(tags osm.Tags, preset POIPreset) string {
	if preset == POIPresetFirefighting {
		if emergency := emergencyCategory(tags); emergency != "" {
			return emergency
		}
		switch tags["amenity"] {
		case "fire_station", "police", "hospital", "clinic", "ambulance_station", "fuel", "charging_station", "drinking_water":
			return tags["amenity"]
		}
		return strings.TrimSpace(tags["place"])
	}
	if preset == POIPresetOutdoor || preset == POIPresetCycling || preset == POIPresetTravel {
		if category := activityCategory(tags, preset); category != "" {
			return category
		}
		return strings.TrimSpace(tags["place"])
	}
	for _, key := range []string{"amenity", "shop", "tourism", "leisure", "office"} {
		if value := strings.TrimSpace(tags[key]); value != "" {
			if preset == POIPresetAll {
				return poiCategory(key, value, tags)
			}
			return value
		}
	}
	if preset == POIPresetAll {
		if emergency := emergencyCategory(tags); emergency != "" {
			return emergency
		}
		if natural := naturalPOICategory(tags); natural != "" {
			return natural
		}
		if transit := transitCategory(tags); transit != "" {
			return transit
		}
	}
	return strings.TrimSpace(tags["place"])
}

// These presets deliberately select object values rather than whole OSM keys:
// a summit is useful outdoors, while every mapped tree would overwhelm the map.
func activityCategory(tags osm.Tags, preset POIPreset) string {
	switch preset {
	case POIPresetOutdoor:
		if emergency := oneOf(emergencyCategory(tags), "defibrillator", "access_point"); emergency != "" {
			return emergency
		}
		if amenity := oneOf(tags["amenity"], "drinking_water", "toilets", "shelter", "bench", "bbq", "picnic_table"); amenity != "" {
			return amenity
		}
		if tourism := oneOf(tags["tourism"], "camp_site", "caravan_site", "alpine_hut", "wilderness_hut", "viewpoint", "picnic_site", "information"); tourism != "" {
			return poiCategory("tourism", tourism, tags)
		}
		if natural := naturalPOICategory(tags); natural != "" {
			return natural
		}
		return oneOf(tags["leisure"], "park", "nature_reserve")
	case POIPresetCycling:
		if amenity := oneOf(tags["amenity"], "bicycle_repair_station", "bicycle_rental", "bicycle_parking", "drinking_water", "toilets", "shelter", "bench", "cafe", "restaurant", "fast_food", "picnic_table"); amenity != "" {
			return amenity
		}
		if shop := oneOf(tags["shop"], "bicycle", "supermarket", "convenience", "bakery"); shop != "" {
			return poiCategory("shop", shop, tags)
		}
		if tourism := oneOf(tags["tourism"], "camp_site", "caravan_site", "picnic_site"); tourism != "" {
			return tourism
		}
		return oneOf(tags["leisure"], "park")
	case POIPresetTravel:
		if tourism := oneOf(tags["tourism"], "hotel", "hostel", "guest_house", "motel", "camp_site", "caravan_site", "museum", "attraction", "viewpoint", "information"); tourism != "" {
			return poiCategory("tourism", tourism, tags)
		}
		if amenity := oneOf(tags["amenity"], "fuel", "charging_station", "parking", "parking_entrance", "toilets", "restaurant", "cafe", "fast_food", "bank", "atm", "pharmacy", "hospital", "ferry_terminal"); amenity != "" {
			return amenity
		}
		return transitCategory(tags)
	}
	return ""
}

func oneOf(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return ""
}

func poiCategory(key, value string, tags osm.Tags) string {
	if key == "shop" && value == "bicycle" {
		return "bicycle_shop"
	}
	if key == "tourism" && value == "information" && tags["information"] == "guidepost" {
		return "guidepost"
	}
	return value
}

func naturalPOICategory(tags osm.Tags) string {
	return oneOf(tags["natural"], "peak", "saddle", "spring", "cave_entrance")
}

func transitCategory(tags osm.Tags) string {
	switch tags["railway"] {
	case "station", "halt":
		return "station"
	case "tram_stop":
		return "tram_stop"
	}
	if tags["highway"] == "bus_stop" {
		return "bus_stop"
	}
	switch tags["public_transport"] {
	case "station":
		return "station"
	case "platform":
		return "public_transport_stop"
	}
	switch tags["aeroway"] {
	case "aerodrome":
		return "aerodrome"
	case "terminal":
		return "airport_terminal"
	}
	return ""
}

func poiLabel(category string) string {
	switch category {
	case "fire_hydrant":
		return "Hydrant"
	case "suction_point":
		return "Saugstelle"
	case "water_tank":
		return "Löschwassertank"
	case "fire_water_pond":
		return "Löschteich"
	case "defibrillator":
		return "Defibrillator"
	case "access_point":
		return "Rettungspunkt"
	case "assembly_point":
		return "Sammelstelle"
	case "fire_service_inlet":
		return "Löschwassereinspeisung"
	case "fire_extinguisher":
		return "Feuerlöscher"
	case "fire_hose":
		return "Löschschlauch"
	case "phone":
		return "Notruftelefon"
	case "siren":
		return "Sirene"
	case "ambulance_station":
		return "Rettungswache"
	case "first_aid":
		return "Erste Hilfe"
	case "fire_station":
		return "Feuerwache"
	case "police":
		return "Polizei"
	case "hospital":
		return "Krankenhaus"
	case "clinic":
		return "Klinik"
	case "fuel":
		return "Tankstelle"
	case "charging_station":
		return "Ladestation"
	case "drinking_water":
		return "Trinkwasserstelle"
	case "toilets":
		return "Toilette"
	case "shelter":
		return "Unterstand"
	case "bench":
		return "Sitzbank"
	case "bbq":
		return "Grillplatz"
	case "picnic_table":
		return "Picknicktisch"
	case "camp_site":
		return "Campingplatz"
	case "caravan_site":
		return "Wohnmobilstellplatz"
	case "alpine_hut":
		return "Berghütte"
	case "wilderness_hut":
		return "Wildnishütte"
	case "viewpoint":
		return "Aussichtspunkt"
	case "picnic_site":
		return "Picknickplatz"
	case "information":
		return "Information"
	case "guidepost":
		return "Wegweiser"
	case "peak":
		return "Gipfel"
	case "saddle":
		return "Bergsattel"
	case "spring":
		return "Quelle"
	case "cave_entrance":
		return "Höhleneingang"
	case "park":
		return "Park"
	case "nature_reserve":
		return "Naturschutzgebiet"
	case "bicycle_shop", "bicycle":
		return "Fahrradgeschäft"
	case "bicycle_repair_station":
		return "Fahrrad-Reparaturstation"
	case "bicycle_rental":
		return "Fahrradverleih"
	case "bicycle_parking":
		return "Fahrradparkplatz"
	case "supermarket":
		return "Supermarkt"
	case "convenience":
		return "Lebensmittelgeschäft"
	case "bakery":
		return "Bäckerei"
	case "cafe":
		return "Café"
	case "restaurant":
		return "Restaurant"
	case "fast_food":
		return "Imbiss"
	case "hotel":
		return "Hotel"
	case "hostel":
		return "Hostel"
	case "guest_house":
		return "Pension"
	case "motel":
		return "Motel"
	case "museum":
		return "Museum"
	case "attraction":
		return "Sehenswürdigkeit"
	case "parking":
		return "Parkplatz"
	case "parking_entrance":
		return "Parkhauseinfahrt"
	case "bank":
		return "Bank"
	case "atm":
		return "Geldautomat"
	case "pharmacy":
		return "Apotheke"
	case "station":
		return "Bahnhof"
	case "tram_stop":
		return "Straßenbahnhaltestelle"
	case "bus_stop":
		return "Bushaltestelle"
	case "public_transport_stop":
		return "Haltestelle"
	case "ferry_terminal":
		return "Fähranleger"
	case "aerodrome":
		return "Flugplatz"
	case "airport_terminal":
		return "Flughafenterminal"
	default:
		return strings.ReplaceAll(category, "_", " ")
	}
}

func poiSearchAliases(category string) string {
	switch category {
	case "fire_hydrant":
		return "Hydrant Hydranten Feuerhydrant Feuerhydranten Löschwasser"
	case "suction_point":
		return "Saugstelle Saugstellen Löschwasserentnahmestelle Löschwasser"
	case "water_tank":
		return "Löschwassertank Löschwassertanks Wassertank Wassertanks Löschwasser"
	case "fire_water_pond":
		return "Löschteich Löschteiche Löschwasserteich Löschwasser"
	case "defibrillator":
		return "Defibrillator Defibrillatoren AED"
	case "access_point":
		return "Rettungspunkt Rettungspunkte"
	case "assembly_point":
		return "Sammelstelle Sammelstellen Sammelpunkt Sammelpunkte"
	case "fire_station":
		return "Feuerwehr Feuerwache Feuerwachen Feuerwehrhaus"
	case "police":
		return "Polizei"
	case "hospital":
		return "Krankenhaus Krankenhäuser"
	case "clinic":
		return "Klinik Kliniken"
	case "ambulance_station":
		return "Rettungswache Rettungswachen"
	case "fuel":
		return "Tankstelle Tankstellen"
	case "charging_station":
		return "Ladesäule Ladesäulen Ladestation Ladestationen"
	case "drinking_water":
		return "Trinkwasser Trinkwasserstelle Trinkwasserstellen"
	case "toilets":
		return "Toilette Toiletten WC"
	case "shelter", "wilderness_hut":
		return "Schutzhütte Schutzhütten Unterstand Unterstände Rastplatz Rastplätze"
	case "bench":
		return "Sitzbank Sitzbänke Bank Bänke"
	case "bbq":
		return "Grillplatz Grillplätze Grillstelle Grillstellen"
	case "picnic_table":
		return "Picknicktisch Picknicktische Rastplatz Rastplätze"
	case "camp_site":
		return "Camping Campingplatz Campingplätze Zeltplatz Zeltplätze"
	case "caravan_site":
		return "Camping Wohnmobil Wohnmobile Wohnmobilstellplatz Wohnmobilstellplätze Stellplatz Stellplätze"
	case "alpine_hut":
		return "Berghütte Berghütten Schutzhütte Schutzhütten Alpenvereinshütte"
	case "viewpoint":
		return "Aussicht Aussichtspunkt Aussichtspunkte"
	case "picnic_site":
		return "Picknick Picknickplatz Picknickplätze Rastplatz Rastplätze"
	case "information":
		return "Information Informationen Touristinformation Wanderkarte"
	case "guidepost":
		return "Information Informationen Wegweiser Wanderwegweiser"
	case "peak":
		return "Gipfel Berggipfel Berg Berge"
	case "saddle":
		return "Bergsattel Sattel Pass Bergpass"
	case "spring":
		return "Quelle Quellen"
	case "cave_entrance":
		return "Höhle Höhlen Höhleneingang Höhleneingänge"
	case "park":
		return "Park Parks Grünanlage Grünanlagen"
	case "nature_reserve":
		return "Naturschutzgebiet Naturschutzgebiete Schutzgebiet Schutzgebiete"
	case "bicycle_shop", "bicycle":
		return "Fahrrad Fahrräder Fahrradgeschäft Fahrradgeschäfte Fahrradladen Fahrradläden"
	case "bicycle_repair_station":
		return "Fahrradwerkstatt Fahrradwerkstätten Fahrradreparatur Fahrradreparaturstation Reparaturstation"
	case "bicycle_rental":
		return "Fahrradverleih Fahrradvermietung Leihrad Leihräder"
	case "bicycle_parking":
		return "Fahrradparkplatz Fahrradparkplätze Fahrradständer Radständer"
	case "supermarket":
		return "Supermarkt Supermärkte Lebensmittel Einkauf Einkaufen"
	case "convenience":
		return "Lebensmittel Lebensmittelgeschäft Lebensmittelgeschäfte Einkauf Einkaufen"
	case "bakery":
		return "Bäcker Bäckerei Bäckereien Backwaren"
	case "cafe":
		return "Café Cafés Kaffee"
	case "restaurant":
		return "Restaurant Restaurants Gastronomie Essen"
	case "fast_food":
		return "Imbiss Schnellrestaurant Essen"
	case "hotel":
		return "Hotel Hotels Unterkunft Unterkünfte Übernachtung"
	case "hostel":
		return "Hostel Hostels Jugendherberge Jugendherbergen Unterkunft Übernachtung"
	case "guest_house":
		return "Pension Pensionen Gästehaus Gästehäuser Unterkunft Übernachtung"
	case "motel":
		return "Motel Motels Unterkunft Übernachtung"
	case "museum":
		return "Museum Museen Sehenswürdigkeit Sehenswürdigkeiten"
	case "attraction":
		return "Sehenswürdigkeit Sehenswürdigkeiten Attraktion Attraktionen"
	case "parking", "parking_entrance":
		return "Parkplatz Parkplätze Parkhaus Parkhäuser Parken"
	case "bank":
		return "Bank Banken Geld"
	case "atm":
		return "Geldautomat Geldautomaten Geld Bargeld"
	case "pharmacy":
		return "Apotheke Apotheken"
	case "station":
		return "Bahnhof Bahnhöfe Haltepunkt Haltepunkte Bahn Zug"
	case "tram_stop":
		return "Straßenbahn Straßenbahnhaltestelle Straßenbahnhaltestellen Tram Haltestelle Haltestellen"
	case "bus_stop":
		return "Bus Bushaltestelle Bushaltestellen Haltestelle Haltestellen"
	case "public_transport_stop":
		return "Haltestelle Haltestellen ÖPNV Öffentlicher Nahverkehr"
	case "ferry_terminal":
		return "Fähre Fähren Fähranleger Fährterminal"
	case "aerodrome":
		return "Flughafen Flughäfen Flugplatz Flugplätze"
	case "airport_terminal":
		return "Flughafen Flughäfen Flughafenterminal Terminal"
	}
	return ""
}
