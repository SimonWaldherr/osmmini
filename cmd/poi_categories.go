package main

import (
	osmmini "simonwaldherr.de/go/osmmini/core"
	"strings"
	"unicode"
)

// Filters are OR alternatives; entries within a filter are AND conditions.
// Keep aliases here so AI, geographic filters and search use the same semantics.
type poiCategory struct {
	ID      string
	Key     string
	Value   string
	Aliases []string
	Filters []osmmini.Tags
}

var poiCategories = []poiCategory{
	{ID: "fuel", Key: "amenity", Value: "fuel", Aliases: []string{"tankstelle", "tankstellen", "benzin", "diesel", "gas station", "fuel"}, Filters: []osmmini.Tags{{"amenity": "fuel"}}},
	{ID: "station", Key: "railway", Value: "station", Aliases: []string{"bahnhof", "bahnhöfe", "train station", "railway station"}, Filters: []osmmini.Tags{{"railway": "station"}}},
	{ID: "bus_stop", Key: "highway", Value: "bus_stop", Aliases: []string{"haltestelle", "bushaltestelle"}, Filters: []osmmini.Tags{{"highway": "bus_stop"}}},
	{ID: "pharmacy", Key: "amenity", Value: "pharmacy", Aliases: []string{"apotheke", "pharmacy", "apotheken", "pharmacies"}, Filters: []osmmini.Tags{{"amenity": "pharmacy"}, {"healthcare": "pharmacy"}}},
	{ID: "supermarket", Key: "shop", Value: "supermarket", Aliases: []string{"supermarkt", "supermarket", "supermärkte", "supermarkets"}, Filters: []osmmini.Tags{{"shop": "supermarket"}}},
	{ID: "bakery", Key: "shop", Value: "bakery", Aliases: []string{"bäckerei", "bakery", "bäckereien", "bakeries"}, Filters: []osmmini.Tags{{"shop": "bakery"}}},
	{ID: "parking", Key: "amenity", Value: "parking", Aliases: []string{"parkplatz", "parking", "parkplätze", "car park"}, Filters: []osmmini.Tags{{"amenity": "parking"}}},
	{ID: "bank", Key: "amenity", Value: "bank", Aliases: []string{"bank"}, Filters: []osmmini.Tags{{"amenity": "bank"}}},
	{ID: "atm", Key: "amenity", Value: "atm", Aliases: []string{"geldautomat", "atm", "geldautomaten"}, Filters: []osmmini.Tags{{"amenity": "atm"}}},
	{ID: "hospital", Key: "amenity", Value: "hospital", Aliases: []string{"krankenhaus", "hospital", "krankenhäuser", "hospitals"}, Filters: []osmmini.Tags{{"amenity": "hospital"}, {"healthcare": "hospital"}}},
	{ID: "school", Key: "amenity", Value: "school", Aliases: []string{"schule", "school"}, Filters: []osmmini.Tags{{"amenity": "school"}}},
	{ID: "museum", Key: "tourism", Value: "museum", Aliases: []string{"museum", "museen", "museums"}, Filters: []osmmini.Tags{{"tourism": "museum"}}},
	{ID: "hotel", Key: "tourism", Value: "hotel", Aliases: []string{"hotel", "hotels"}, Filters: []osmmini.Tags{{"tourism": "hotel"}}},
	{ID: "restaurant", Key: "amenity", Value: "restaurant", Aliases: []string{"gasthaus", "restaurant", "gaststätte", "restaurants", "gaststätten"}, Filters: []osmmini.Tags{{"amenity": "restaurant"}}},
	{ID: "cafe", Key: "amenity", Value: "cafe", Aliases: []string{"café", "cafe", "kaffee", "cafés", "cafes", "coffee shop"}, Filters: []osmmini.Tags{{"amenity": "cafe"}}},
	{ID: "fast_food", Key: "amenity", Value: "fast_food", Aliases: []string{"fastfood", "fast food", "imbiss"}, Filters: []osmmini.Tags{{"amenity": "fast_food"}}},
	{ID: "doctors", Key: "amenity", Value: "doctors", Aliases: []string{"arzt", "ärzte", "arztpraxis", "doctor"}, Filters: []osmmini.Tags{{"amenity": "doctors"}, {"healthcare": "doctor"}}},
	{ID: "dentist", Key: "amenity", Value: "dentist", Aliases: []string{"zahnarzt", "zahnärzte", "zahnarztpraxis", "dentist"}, Filters: []osmmini.Tags{{"amenity": "dentist"}, {"healthcare": "dentist"}}},
	{ID: "post_office", Key: "amenity", Value: "post_office", Aliases: []string{"post", "postamt"}, Filters: []osmmini.Tags{{"amenity": "post_office"}}},
	{ID: "hairdresser", Key: "shop", Value: "hairdresser", Aliases: []string{"friseur"}, Filters: []osmmini.Tags{{"shop": "hairdresser"}}},
	{ID: "playground", Key: "leisure", Value: "playground", Aliases: []string{"spielplatz"}, Filters: []osmmini.Tags{{"leisure": "playground"}}},
	{ID: "swimming_pool", Key: "leisure", Value: "swimming_pool", Aliases: []string{"schwimmbad", "schwimmbaeder", "schwimmbäder", "hallenbad", "freibad", "swimming pool", "swimming pools"}, Filters: []osmmini.Tags{{"leisure": "swimming_pool"}, {"leisure": "sports_centre", "sport": "swimming"}}},
	{ID: "pitch", Key: "leisure", Value: "pitch", Aliases: []string{"sportplatz"}, Filters: []osmmini.Tags{{"leisure": "pitch"}}},
	{ID: "park", Key: "leisure", Value: "park", Aliases: []string{"park", "parks"}, Filters: []osmmini.Tags{{"leisure": "park"}}},
	{ID: "spa", Key: "leisure", Value: "spa", Aliases: []string{"therme"}, Filters: []osmmini.Tags{{"leisure": "spa"}}},
	{ID: "sauna", Key: "leisure", Value: "sauna", Aliases: []string{"sauna"}, Filters: []osmmini.Tags{{"leisure": "sauna"}}},
	{ID: "forest", Key: "landuse", Value: "forest", Aliases: []string{"wald", "waelder", "wälder", "wäldchen", "forest", "forests", "wood", "woods"}, Filters: []osmmini.Tags{{"landuse": "forest"}, {"natural": "wood"}}},
	{ID: "river", Key: "waterway", Value: "river", Aliases: []string{"fluss", "river"}, Filters: []osmmini.Tags{{"waterway": "river"}}},
	{ID: "place_of_worship", Key: "amenity", Value: "place_of_worship", Aliases: []string{"kirche", "church"}, Filters: []osmmini.Tags{{"amenity": "place_of_worship"}}},
	{ID: "townhall", Key: "amenity", Value: "townhall", Aliases: []string{"rathaus"}, Filters: []osmmini.Tags{{"amenity": "townhall"}}},
	{ID: "library", Key: "amenity", Value: "library", Aliases: []string{"bibliothek", "library"}, Filters: []osmmini.Tags{{"amenity": "library"}}},
	{ID: "zoo", Key: "tourism", Value: "zoo", Aliases: []string{"zoo", "tierpark"}, Filters: []osmmini.Tags{{"tourism": "zoo"}}},
	{ID: "cinema", Key: "amenity", Value: "cinema", Aliases: []string{"kino", "cinema", "kinos", "cinemas"}, Filters: []osmmini.Tags{{"amenity": "cinema"}}},
	{ID: "fire_station", Key: "amenity", Value: "fire_station", Aliases: []string{"feuerwehr", "feuerwehrhaus", "feuerwehrhäuser", "fire station"}, Filters: []osmmini.Tags{{"amenity": "fire_station"}}},
	{ID: "police", Key: "amenity", Value: "police", Aliases: []string{"polizei", "police"}, Filters: []osmmini.Tags{{"amenity": "police"}}},
	{ID: "lake", Key: "water", Value: "lake", Aliases: []string{"see", "seen", "lake", "lakes"}, Filters: []osmmini.Tags{{"natural": "water", "water": "lake"}}},
	{ID: "charging_station", Key: "amenity", Value: "charging_station", Aliases: []string{"ladesäule", "ladesäulen", "ladestation", "ladestationen", "charging station", "ev charging"}, Filters: []osmmini.Tags{{"amenity": "charging_station"}}},
	{ID: "toilets", Key: "amenity", Value: "toilets", Aliases: []string{"toilette", "toiletten", "wc", "toilets"}, Filters: []osmmini.Tags{{"amenity": "toilets"}}},
	{ID: "bicycle_parking", Key: "amenity", Value: "bicycle_parking", Aliases: []string{"fahrradparkplatz", "fahrradparkplätze", "bicycle parking"}, Filters: []osmmini.Tags{{"amenity": "bicycle_parking"}}},
	{ID: "camp_site", Key: "tourism", Value: "camp_site", Aliases: []string{"campingplatz", "campingplätze", "campsite"}, Filters: []osmmini.Tags{{"tourism": "camp_site"}}},
	{ID: "physiotherapist", Key: "healthcare", Value: "physiotherapist", Aliases: []string{"physiotherapie", "physiotherapist"}, Filters: []osmmini.Tags{{"healthcare": "physiotherapist"}}},
}

var poiCategoryKeys = []string{"amenity", "shop", "tourism", "leisure", "office", "place", "emergency", "healthcare", "railway", "public_transport", "aeroway", "highway", "natural", "landuse", "water", "waterway", "craft", "historic", "sport"}

var categoryTransliteration = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "é", "e", "è", "e")

func normalizeCategoryAlias(value string) string {
	value = categoryTransliteration.Replace(strings.ToLower(value))
	value = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		if unicode.IsMark(r) {
			return -1
		}
		return ' '
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

var poiCategoryAliases, poiCategoryPairs = buildPOICategoryIndexes()

func buildPOICategoryIndexes() (map[string]*poiCategory, map[string]*poiCategory) {
	aliases, pairs := map[string]*poiCategory{}, map[string]*poiCategory{}
	for i := range poiCategories {
		category := &poiCategories[i]
		pairs[category.Key+"="+category.Value] = category
		for _, alias := range append([]string{category.ID}, category.Aliases...) {
			key := normalizeCategoryAlias(alias)
			if previous := aliases[key]; previous != nil && previous != category {
				panic("ambiguous POI alias: " + alias)
			}
			aliases[key] = category
		}
	}
	return aliases, pairs
}
func lookupPOICategory(query string) *poiCategory {
	if category := poiCategoryAliases[query]; category != nil {
		return category
	}
	return poiCategoryAliases[normalizeCategoryAlias(query)]
}
func categoryInPrompt(prompt string) (string, *poiCategory) {
	words := strings.Fields(normalizeCategoryAlias(prompt))
	// Longest phrase first, then earliest occurrence. Map iteration never determines priority.
	for length := min(len(words), 4); length > 0; length-- {
		for start := 0; start+length <= len(words); start++ {
			phrase := strings.Join(words[start:start+length], " ")
			if category := poiCategoryAliases[phrase]; category != nil {
				return phrase, category
			}
		}
	}
	return "", nil
}
func tagHasValue(tags osmmini.Tags, key, value string) bool {
	for _, actual := range strings.Split(tags[key], ";") {
		if actual = strings.TrimSpace(actual); actual != "" && (value == "" || strings.EqualFold(actual, value)) {
			return true
		}
	}
	return false
}
func (category *poiCategory) matches(tags osmmini.Tags) bool {
	for _, filter := range category.Filters {
		matched := true
		for key, value := range filter {
			if !tagHasValue(tags, key, value) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
func matchesKnownPOICategory(tags osmmini.Tags, query string) bool {
	category := lookupPOICategory(query)
	return category != nil && category.matches(tags)
}
func matchesPOITag(tags osmmini.Tags, key, value string) bool {
	if category := poiCategoryPairs[key+"="+value]; category != nil {
		return category.matches(tags)
	}
	return tagHasValue(tags, key, value)
}
func matchesPOICategory(tags osmmini.Tags, query string) bool {
	query = strings.TrimSpace(query)
	// Qualified filters are exact OSM filters, not semantic category expansion.
	if key, value, ok := strings.Cut(query, "="); ok {
		for _, allowed := range poiCategoryKeys {
			if strings.TrimSpace(key) == allowed && strings.TrimSpace(value) != "" {
				return tagHasValue(tags, allowed, strings.TrimSpace(value))
			}
		}
		return false
	}
	if category := lookupPOICategory(query); category != nil {
		return category.matches(tags)
	}
	// Keep custom tag values accepted by the existing Geo API.
	for _, key := range poiCategoryKeys {
		if tagHasValue(tags, key, query) {
			return true
		}
	}
	return false
}
