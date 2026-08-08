package main

import (
	"fmt"
	"os"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini"
)

func TestCollectPOINodeReferencesKeepsOnlyIndexedGeometry(t *testing.T) {
	ways := map[int64]osmmini.Way{
		10: {ID: 10, NodeIDs: []int64{1, 2, 2}},
	}
	rels := map[int64]osmmini.Relation{
		20: {ID: 20, Members: []osmmini.Member{
			{Type: osmmini.MemberNode, ID: 3},
			{Type: osmmini.MemberWay, ID: 10},
			{Type: osmmini.MemberRelation, ID: 99},
		}},
	}

	got := collectPOINodeReferences(ways, rels)
	if len(got) != 3 {
		t.Fatalf("coordinate references = %d, want 3 (%v)", len(got), got)
	}
	for _, id := range []int64{1, 2, 3} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing required node %d", id)
		}
	}
	if _, ok := got[99]; ok {
		t.Error("relation member ID was treated as a node coordinate")
	}
}

func TestBuildPOIIndexUsesSecondPassOnlyForReferencedCoordinates(t *testing.T) {
	nodes := make(map[int64]osmmini.Coord)
	taggedNodes := make(map[int64]osmmini.Node)
	ways := make(map[int64]osmmini.Way)
	rels := make(map[int64]osmmini.Relation)

	calls := 0
	extract := func(opts osmmini.Options, cb osmmini.Callbacks) error {
		calls++
		switch calls {
		case 1:
			if cb.Node != nil || cb.TaggedNode == nil || cb.TaggedWay == nil || cb.TaggedRelation == nil {
				return fmt.Errorf("first pass callbacks do not only collect POI entities")
			}
			if !opts.EmitWayNodeIDs || !opts.EmitRelationMembers || opts.TaggedNodeKey == nil || opts.TaggedWayKey == nil || opts.TaggedRelationKey == nil {
				return fmt.Errorf("first pass is missing POI extraction options")
			}
			if err := cb.TaggedNode(osmmini.Node{
				ID: 10, Lat: 48.137, Lon: 11.575,
				Tags: osmmini.Tags{"amenity": "cafe", "name": "Punkt-POI"},
			}); err != nil {
				return err
			}
			if err := cb.TaggedWay(osmmini.Way{
				ID: 20, NodeIDs: []int64{1, 2},
				Tags: osmmini.Tags{"amenity": "parking", "name": "Flächen-POI"},
			}); err != nil {
				return err
			}
			return cb.TaggedRelation(osmmini.Relation{
				ID: 30,
				Members: []osmmini.Member{
					{Type: osmmini.MemberWay, ID: 20},
					{Type: osmmini.MemberNode, ID: 3},
				},
				Tags: osmmini.Tags{"boundary": "administrative", "name": "Testgebiet"},
			})
		case 2:
			if cb.Node == nil || cb.TaggedNode != nil || cb.TaggedWay != nil || cb.TaggedRelation != nil {
				return fmt.Errorf("second pass should only resolve node coordinates")
			}
			if opts.EmitWayNodeIDs || opts.EmitRelationMembers || opts.KeepTag != nil {
				return fmt.Errorf("second pass unexpectedly decodes POI metadata")
			}
			for id, coord := range map[int64]osmmini.Coord{
				1:  {Lat: 48.1, Lon: 11.5},
				2:  {Lat: 48.2, Lon: 11.6},
				3:  {Lat: 48.3, Lon: 11.7},
				99: {Lat: 49.0, Lon: 12.0}, // must not be retained
			} {
				if err := cb.Node(id, coord.Lat, coord.Lon); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("unexpected extraction pass %d", calls)
		}
	}

	if err := buildPOIIndex(extract, nodes, taggedNodes, ways, rels); err != nil {
		t.Fatalf("buildPOIIndex() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("extraction passes = %d, want 2", calls)
	}
	if len(nodes) != 4 { // 1, 2, 3 plus the tagged point POI 10
		t.Fatalf("retained coordinates = %d, want 4: %#v", len(nodes), nodes)
	}
	if _, ok := nodes[99]; ok {
		t.Fatal("unreferenced PBF node was retained")
	}
	if got := taggedNodes[10].Tags["amenity"]; got != "cafe" {
		t.Fatalf("tagged point POI = %q, want cafe", got)
	}
	if _, ok := ways[20]; !ok {
		t.Fatal("indexed POI way was not retained")
	}
	if _, ok := rels[30]; !ok {
		t.Fatal("indexed POI relation was not retained")
	}
}

func TestPOICacheRejectsPreTwoPassFormat(t *testing.T) {
	path := t.TempDir() + "/poi.json"
	if err := os.WriteFile(path, []byte(`{"version":2,"nodes":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &server{}
	err := s.loadPOICache(path, map[int64]osmmini.Coord{}, map[int64]osmmini.Node{}, map[int64]osmmini.Way{}, map[int64]osmmini.Relation{})
	if err == nil {
		t.Fatal("pre-two-pass POI cache was accepted")
	}
}
