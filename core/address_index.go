package osmmini

import (
	"math/bits"
	"strings"
)

var addressSeparators = strings.NewReplacer(",", " ", ";", " ")

// AddressIndex stores normalized fields for repeated address/POI queries. It is
// safe for concurrent searches and does not use a global cache. Rebuild it after
// changing the catalog. Input Tags maps are retained for returning the original
// entries and must be treated as read-only while the index is in use.
//
// Only populated search fields are retained. Already-normalized values reuse
// their existing string storage rather than duplicating every tag in the catalog.
type AddressIndex struct {
	entries []AddressEntry
	offsets []int
	masks   []uint16
	values  []string
}

var addressSearchTags = [...]string{"addr:street", "addr:housenumber", "addr:postcode", "addr:city", "addr:place", "name", "brand", "operator", "shop", "office", "amenity", "tourism", "leisure"}

type normalizedAddressFields struct {
	street, house, postcode, city, place, name, brand, operator, shop, office, amenity, tourism, leisure string
}

func normalizedAddressFieldsFromTags(tags Tags, q normalizedAddressQuery) normalizedAddressFields {
	var f normalizedAddressFields
	if q.housenumber != "" {
		f.house = normalize(tags["addr:housenumber"])
	}
	if q.postcode != "" {
		f.postcode = normalize(tags["addr:postcode"])
	}
	if q.city != "" || q.raw != "" {
		f.city, f.place = normalize(tags["addr:city"]), normalize(tags["addr:place"])
	}
	if q.street != "" || q.raw != "" {
		f.street = normalize(tags["addr:street"])
		f.name, f.brand, f.operator = normalize(tags["name"]), normalize(tags["brand"]), normalize(tags["operator"])
		f.shop, f.office, f.amenity = normalize(tags["shop"]), normalize(tags["office"]), normalize(tags["amenity"])
	}
	if q.raw != "" {
		f.tourism, f.leisure = normalize(tags["tourism"]), normalize(tags["leisure"])
	}
	return f
}

func (f normalizedAddressFields) score(q normalizedAddressQuery) int {
	street := f.street
	house := f.house
	postcode := f.postcode
	city := f.city
	place := f.place
	name := f.name
	brand := f.brand
	operator := f.operator
	shop := f.shop
	office := f.office
	amenity := f.amenity
	tourism := f.tourism
	leisure := f.leisure
	equal := func(value, query string) bool { return query != "" && value == query }
	contains := func(value, query string) bool { return value != "" && query != "" && strings.Contains(value, query) }

	score := 0
	if equal(street, q.street) {
		score += 3
	}
	if equal(house, q.housenumber) {
		score += 4
	}
	if equal(postcode, q.postcode) {
		score += 2
	}
	if q.city != "" {
		if equal(city, q.city) || equal(place, q.city) {
			score += 1
		}
	}
	if q.raw != "" {
		switch {
		case equal(name, q.raw):
			score += 6
		case contains(name, q.raw):
			score += 3
		}
		switch {
		case equal(brand, q.raw), equal(operator, q.raw):
			score += 4
		case contains(brand, q.raw), contains(operator, q.raw):
			score += 2
		}
		switch {
		case equal(shop, q.raw), equal(office, q.raw), equal(amenity, q.raw), equal(tourism, q.raw), equal(leisure, q.raw):
			score += 3
		case contains(shop, q.raw), contains(office, q.raw), contains(amenity, q.raw), contains(tourism, q.raw), contains(leisure, q.raw):
			score += 1
		}
		if equal(street, q.raw) {
			score += 4
		} else if contains(street, q.raw) {
			score += 1
		}
		if equal(city, q.raw) || equal(place, q.raw) {
			score += 3
		}
	}
	// If street/address matching didn't produce a high score, also try matching
	// common POI/company tags so users can search by firm/shop names.
	if q.street != "" {
		// exact name match (high relevance)
		if equal(name, q.street) {
			score += 5
		} else if contains(name, q.street) {
			score += 2
		}
		// other common business tags
		if equal(brand, q.street) || equal(operator, q.street) {
			score += 3
		} else if contains(brand, q.street) || contains(operator, q.street) {
			score += 1
		}
		if equal(shop, q.street) || equal(office, q.street) || equal(amenity, q.street) {
			score += 2
		} else if contains(shop, q.street) || contains(office, q.street) || contains(amenity, q.street) {
			score += 1
		}
		// fallback: partial street match as before
		if score == 0 && contains(street, q.street) {
			score += 1
		}
	}
	return score
}

// NewAddressIndex snapshots entry metadata and prepares each nonempty search
// field once. Search result tags refer to the supplied catalog's read-only maps.
func NewAddressIndex(entries []AddressEntry) *AddressIndex {
	x := &AddressIndex{
		entries: append([]AddressEntry(nil), entries...),
		offsets: make([]int, len(entries)),
		masks:   make([]uint16, len(entries)),
	}
	count := 0
	for i, entry := range entries {
		for j, key := range addressSearchTags {
			if entry.Tags[key] != "" {
				x.masks[i] |= 1 << j
				count++
			}
		}
	}
	x.values = make([]string, 0, count)
	// Repeated cities/categories otherwise allocate the same lowercase string
	// for every address. Keep this construction-only cache bounded so unique
	// street names cannot turn it into a second regional catalog in memory.
	common := make(map[string]string)
	for i, entry := range entries {
		x.offsets[i] = len(x.values)
		for mask := x.masks[i]; mask != 0; mask &= mask - 1 {
			j := bits.TrailingZeros16(mask)
			raw := entry.Tags[addressSearchTags[j]]
			value, ok := common[raw]
			if !ok {
				value = normalize(raw)
				if j >= 2 && len(common) < 4096 {
					common[raw] = value
				}
			}
			x.values = append(x.values, value)
		}
	}
	return x
}

func (x *AddressIndex) fields(i int) normalizedAddressFields {
	var values [len(addressSearchTags)]string
	pos := x.offsets[i]
	for mask := x.masks[i]; mask != 0; mask &= mask - 1 {
		values[bits.TrailingZeros16(mask)] = x.values[pos]
		pos++
	}
	return normalizedAddressFields{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8], values[9], values[10], values[11], values[12]}
}

// Search ranks matches exactly like SearchAddresses, reusing preprocessed fields.
// A nonpositive limit returns every match.
func (x *AddressIndex) Search(q AddressQuery, limit int) []AddressEntry {
	if x == nil {
		return nil
	}
	return searchAddressEntries(x.entries, normalizeAddressQuery(q), limit, func(i int, q normalizedAddressQuery) int { return x.fields(i).score(q) })
}

// FindBest applies the same house-number safeguards as FindBestAddress.
func (x *AddressIndex) FindBest(q AddressQuery) (AddressEntry, bool) {
	if x == nil {
		return AddressEntry{}, false
	}
	query := normalizeAddressQuery(q)
	var best AddressEntry
	bestScore := -1
	for i, entry := range x.entries {
		score := x.fields(i).score(query)
		if score > bestScore {
			best, bestScore = entry, score
		}
	}
	if bestScore <= 0 || query.housenumber != "" && bestScore <= 4 {
		return AddressEntry{}, false
	}
	return best, true
}
