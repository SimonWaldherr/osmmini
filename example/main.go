package main

import (
	"fmt"
	"log"
	"strings"

	"simonwaldherr.de/go/osmmini"
)

func main() {
	var highways, addrNodes, addrWays, addrRels int

	opts := osmmini.Options{
		EmitWayNodeIDs:      true,
		EmitRelationMembers: false,
		KeepTag: func(k string) bool { // optional: nur relevante Tags behalten
			return k == "highway" || k == "name" || strings.HasPrefix(k, "addr:")
		},
	}

	err := osmmini.ExtractFile("region.osm.pbf", opts, osmmini.Callbacks{
		HighwayWay: func(w osmmini.Way) error {
			highways++
			return nil
		},
		AddressNode: func(n osmmini.Node) error {
			addrNodes++
			return nil
		},
		AddressWay: func(w osmmini.Way) error {
			addrWays++
			return nil
		},
		AddressRelation: func(r osmmini.Relation) error {
			addrRels++
			return nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("highways=%d addr_nodes=%d addr_ways=%d addr_relations=%d\n",
		highways, addrNodes, addrWays, addrRels)
}
