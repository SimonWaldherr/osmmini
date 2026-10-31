package main

import osmmini "simonwaldherr.de/go/osmmini/core"

// The prepared index is immutable and shared across requests. Keeping the
// fallback supports small/ad-hoc server instances without an extra RAM cache.
func (s *server) searchAddresses(query osmmini.AddressQuery, limit int) []osmmini.AddressEntry {
	if s.addressIndex != nil {
		return s.addressIndex.Search(query, limit)
	}
	return osmmini.SearchAddresses(s.addrs, query, limit)
}

func (s *server) findBestAddress(query osmmini.AddressQuery) (osmmini.AddressEntry, bool) {
	if s.addressIndex != nil {
		return s.addressIndex.FindBest(query)
	}
	return osmmini.FindBestAddress(s.addrs, query)
}
