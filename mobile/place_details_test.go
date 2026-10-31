package mobile

import (
	"strings"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func TestPlaceDetailsKeepSourceTextAndContactFallbacks(t *testing.T) {
	details := detailsFromTags(osm.Tags{
		"addr:street": " Hauptstraße ", "addr:housenumber": "7", "addr:postcode": "94405", "addr:city": "Landau",
		"opening_hours": " Mo-Fr 09:00-18:00; PH off ", "contact:phone": "+49 123 456", "contact:website": "https://example.org/a?b=c",
	})
	want := PlaceDetails{Address: "Hauptstraße 7, 94405 Landau", OpeningHours: "Mo-Fr 09:00-18:00; PH off", Phone: "+49 123 456", Website: "https://example.org/a?b=c"}
	if details == nil || *details != want {
		t.Fatalf("source details: %+v", details)
	}
	if detailsFromTags(nil) != nil || cleanDetails(nil) != nil {
		t.Fatal("empty metadata must remain absent")
	}
	placeAddress := detailsFromTags(osm.Tags{"addr:place": "Oberdorf", "addr:housenumber": "3"})
	if placeAddress == nil || placeAddress.Address != "Oberdorf 3" {
		t.Fatalf("place address: %+v", placeAddress)
	}
}

func TestPlaceDetailsEnforceUTF8ByteLimitsAndRejectControls(t *testing.T) {
	value := &PlaceDetails{
		Address: strings.Repeat("ä", 512), OpeningHours: strings.Repeat("a", 1024),
		Phone: strings.Repeat("1", 128), Website: strings.Repeat("a", 2048),
	}
	if got := cleanDetails(value); got == nil || *got != *value {
		t.Fatal("values at byte limits rejected")
	}
	value.Address += "ä"
	value.OpeningHours += "a"
	value.Phone += "1"
	value.Website += "a"
	if cleanDetails(value) != nil {
		t.Fatal("oversized values accepted")
	}
	for _, invalid := range []string{"line\nbreak", "tab\tvalue", "nul\x00value", "zero\u200bwidth", "bidi\u202evalue", string([]byte{0xff})} {
		if cleanDetails(&PlaceDetails{Address: invalid, OpeningHours: invalid, Phone: invalid, Website: invalid}) != nil {
			t.Fatalf("invalid details accepted: %q", invalid)
		}
	}
	details := detailsFromTags(osm.Tags{"phone": "invalid\nphone", "contact:phone": "+49 123"})
	if details == nil || details.Phone != "+49 123" {
		t.Fatal("valid contact fallback lost")
	}
}
