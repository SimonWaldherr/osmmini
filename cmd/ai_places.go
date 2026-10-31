package main

import (
	"fmt"
	osmmini "simonwaldherr.de/go/osmmini/core"
	"slices"
	"strings"
)

// A place suffix supplies context, not another part of the POI's name.
// For example OSM calls the cinema in Dingolfing "cinema filmpalais".
func (s *server) aiScopedPOITargets(query string) ([]apiSearchResult, bool) {
	norm := normalizeCategoryAlias(query)
	var centers []osmmini.Coord
	placeName := ""
	s.poiMu.RLock()
	defer s.poiMu.RUnlock()
	for _, node := range s.poiTaggedNodes {
		name := normalizeCategoryAlias(node.Tags["name"])
		if placeLabelRank(node.Tags["place"]) < 60 || name == "" || !strings.HasSuffix(norm, " "+name) {
			continue
		}
		if len(name) > len(placeName) {
			placeName = name
			centers = nil
		}
		if name == placeName {
			centers = append(centers, osmmini.Coord{Lat: node.Lat, Lon: node.Lon})
		}
	}
	if len(centers) == 0 {
		return nil, false
	}
	terms := strings.Fields(strings.TrimSpace(strings.TrimSuffix(norm, placeName)))
	if len(terms) == 0 {
		return nil, false
	}
	categoryTerms := make([]string, 0, len(terms))
	for _, term := range terms {
		if term != "in" && term != "bei" {
			categoryTerms = append(categoryTerms, term)
		}
	}
	category := lookupPOICategory(strings.Join(categoryTerms, " "))
	var results []apiSearchResult
	seen := map[string]bool{}
	consider := func(id int64, coord osmmini.Coord, tags osmmini.Tags) {
		if s.enforceWindow && s.window != nil && !s.window.Contains(coord) {
			return
		}
		nearby := false
		for _, center := range centers {
			if haversineMeters(center.Lat, center.Lon, coord.Lat, coord.Lon) <= 8000 {
				nearby = true
				break
			}
		}
		if !nearby {
			return
		}
		words := strings.Fields(normalizeCategoryAlias(tags["name"] + " " + tags["brand"]))
		hasPOITerm := false
		for _, term := range terms {
			if category != nil {
				hasPOITerm = category.matches(tags)
				break
			}
			if term == "in" || term == "bei" {
				continue
			}
			hasPOITerm = true
			matched := false
			if category := lookupPOICategory(term); category != nil {
				matched = category.matches(tags)
			}
			for _, word := range words {
				if word == term || (len(term) >= 4 && strings.HasPrefix(word, term)) {
					matched = true
				}
			}
			if !matched {
				return
			}
		}
		if !hasPOITerm || (category == nil && tags["name"] == "" && tags["brand"] == "") {
			return
		}
		result := buildSearchResult("poi", id, coord, tags, query)
		key := result.Label + "|" + fmt.Sprintf("%.5f,%.5f", coord.Lat, coord.Lon)
		if seen[key] {
			return
		}
		seen[key] = true
		results = append(results, result)
	}
	for id, node := range s.poiTaggedNodes {
		consider(id, osmmini.Coord{Lat: node.Lat, Lon: node.Lon}, node.Tags)
	}
	for id, way := range s.poiWays {
		if category == nil && way.Tags["name"] == "" && way.Tags["brand"] == "" {
			continue
		}
		var coord osmmini.Coord
		count := 0
		for _, id := range way.NodeIDs {
			if c, ok := s.poiNodes[id]; ok {
				coord.Lat += c.Lat
				coord.Lon += c.Lon
				count++
			}
		}
		if count > 0 {
			coord.Lat /= float64(count)
			coord.Lon /= float64(count)
			consider(id, coord, way.Tags)
		}
	}
	slices.SortFunc(results, func(a, b apiSearchResult) int { return strings.Compare(a.Label, b.Label) })
	if len(results) > 6 {
		results = results[:6]
	}
	return results, true
}
