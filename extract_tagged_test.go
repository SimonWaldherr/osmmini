package osmmini

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestExtractTaggedNodeKeepsAddressCallback(t *testing.T) {
	strings := []string{"", "amenity", "cafe", "addr:housenumber", "7"}
	node := testPBFNode(101, []uint64{1, 3}, []uint64{2, 4}, 480000000, 110000000)
	data := testPBFData(strings, testPBFPrimitiveGroup(1, node))

	var addressNodes []Node
	var taggedNodes []Node
	err := Extract(bytes.NewReader(data), Options{
		KeepTag: func(k string) bool { return k == "amenity" || k == "addr:housenumber" },
	}, Callbacks{
		AddressNode: func(n Node) error {
			addressNodes = append(addressNodes, n)
			return nil
		},
		TaggedNode: func(n Node) error {
			taggedNodes = append(taggedNodes, n)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(addressNodes) != 1 {
		t.Fatalf("address callbacks = %d, want 1", len(addressNodes))
	}
	if len(taggedNodes) != 1 {
		t.Fatalf("tagged callbacks = %d, want 1", len(taggedNodes))
	}
	if got := taggedNodes[0]; got.ID != 101 || got.Tags["amenity"] != "cafe" || got.Tags["addr:housenumber"] != "7" {
		t.Fatalf("tagged node = %#v, want id 101 with selected tags", got)
	}
	if got := addressNodes[0]; got.ID != 101 || got.Tags["amenity"] != "cafe" || got.Tags["addr:housenumber"] != "7" {
		t.Fatalf("address node = %#v, want unchanged address callback payload", got)
	}
}

func TestExtractTaggedDenseNode(t *testing.T) {
	strings := []string{"", "amenity", "charging_station"}
	dense := testPBFDenseNodes(
		[]int64{202},
		[]int64{480000000},
		[]int64{110000000},
		[]uint64{1, 2, 0},
	)
	data := testPBFData(strings, testPBFPrimitiveGroup(2, dense))

	var tagged []Node
	err := Extract(bytes.NewReader(data), Options{
		KeepTag: func(k string) bool { return k == "amenity" },
	}, Callbacks{
		TaggedNode: func(n Node) error {
			tagged = append(tagged, n)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(tagged) != 1 {
		t.Fatalf("tagged dense callbacks = %d, want 1", len(tagged))
	}
	if got := tagged[0]; got.ID != 202 || got.Tags["amenity"] != "charging_station" {
		t.Fatalf("tagged dense node = %#v, want charging-station node", got)
	}
}

func TestExtractTaggedKeyFiltersNamedEntities(t *testing.T) {
	strings := []string{"", "name", "Unkategorisiert", "amenity", "cafe"}
	namedOnly := testPBFNode(203, []uint64{1}, []uint64{2}, 480000000, 110000000)
	poi := testPBFNode(204, []uint64{1, 3}, []uint64{2, 4}, 480000000, 110000000)
	group := append(testPBFPrimitiveGroup(1, namedOnly), testPBFPrimitiveGroup(1, poi)...)
	data := testPBFData(strings, group)

	var tagged []Node
	err := Extract(bytes.NewReader(data), Options{
		KeepTag:       func(k string) bool { return k == "name" || k == "amenity" },
		TaggedNodeKey: func(k string) bool { return k == "amenity" },
	}, Callbacks{
		TaggedNode: func(n Node) error {
			tagged = append(tagged, n)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(tagged) != 1 || tagged[0].ID != 204 || tagged[0].Tags["name"] != "Unkategorisiert" {
		t.Fatalf("tagged nodes = %#v, want only categorized POI", tagged)
	}
}

func TestExtractProcessesTaggedWaysAndRelationsWithoutAddressCallbacks(t *testing.T) {
	strings := []string{"", "amenity", "parking", "type", "multipolygon"}
	way := testPBFWay(301, []uint64{1}, []uint64{2}, []int64{1})
	relation := testPBFRelation(401, []uint64{3}, []uint64{4})
	group := append(testPBFPrimitiveGroup(3, way), testPBFPrimitiveGroup(4, relation)...)
	data := testPBFData(strings, group)

	var ways []Way
	var relations []Relation
	err := Extract(bytes.NewReader(data), Options{
		KeepTag: func(k string) bool { return k == "amenity" || k == "type" },
	}, Callbacks{
		TaggedWay: func(w Way) error {
			ways = append(ways, w)
			return nil
		},
		TaggedRelation: func(r Relation) error {
			relations = append(relations, r)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(ways) != 1 || ways[0].ID != 301 || ways[0].Tags["amenity"] != "parking" {
		t.Fatalf("tagged ways = %#v, want parking way", ways)
	}
	if len(relations) != 1 || relations[0].ID != 401 || relations[0].Tags["type"] != "multipolygon" {
		t.Fatalf("tagged relations = %#v, want multipolygon relation", relations)
	}
}

func TestExtractAddressRelationStillRuns(t *testing.T) {
	strings := []string{"", "addr:housenumber", "9"}
	relation := testPBFRelation(501, []uint64{1}, []uint64{2})
	data := testPBFData(strings, testPBFPrimitiveGroup(4, relation))

	var addressRelations []Relation
	err := Extract(bytes.NewReader(data), Options{
		KeepTag: func(k string) bool { return k == "addr:housenumber" },
	}, Callbacks{
		AddressRelation: func(r Relation) error {
			addressRelations = append(addressRelations, r)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(addressRelations) != 1 || addressRelations[0].ID != 501 || addressRelations[0].Tags["addr:housenumber"] != "9" {
		t.Fatalf("address relations = %#v, want address relation", addressRelations)
	}
}

func testPBFData(strings []string, group []byte) []byte {
	block := testPBFBytesField(1, testPBFStringTable(strings))
	block = append(block, testPBFBytesField(2, group)...)

	blob := testPBFBytesField(1, block)
	header := testPBFBytesField(1, []byte("OSMData"))
	header = append(header, testPBFVarintField(3, uint64(len(blob)))...)

	out := make([]byte, 4, 4+len(header)+len(blob))
	binary.BigEndian.PutUint32(out, uint32(len(header)))
	out = append(out, header...)
	out = append(out, blob...)
	return out
}

func testPBFStringTable(strings []string) []byte {
	var out []byte
	for _, s := range strings {
		out = append(out, testPBFBytesField(1, []byte(s))...)
	}
	return out
}

func testPBFPrimitiveGroup(field int, entity []byte) []byte {
	return testPBFBytesField(field, entity)
}

func testPBFNode(id int64, keys, vals []uint64, lat, lon int64) []byte {
	out := testPBFVarintField(1, testPBFZigZag(id))
	out = append(out, testPBFPackedUvarints(2, keys)...)
	out = append(out, testPBFPackedUvarints(3, vals)...)
	out = append(out, testPBFVarintField(8, testPBFZigZag(lat))...)
	out = append(out, testPBFVarintField(9, testPBFZigZag(lon))...)
	return out
}

func testPBFDenseNodes(ids, lats, lons []int64, keysVals []uint64) []byte {
	out := testPBFPackedSint64s(1, ids)
	out = append(out, testPBFPackedSint64s(8, lats)...)
	out = append(out, testPBFPackedSint64s(9, lons)...)
	out = append(out, testPBFPackedUvarints(10, keysVals)...)
	return out
}

func testPBFWay(id int64, keys, vals []uint64, refs []int64) []byte {
	out := testPBFVarintField(1, uint64(id))
	out = append(out, testPBFPackedUvarints(2, keys)...)
	out = append(out, testPBFPackedUvarints(3, vals)...)
	out = append(out, testPBFPackedSint64s(8, refs)...)
	return out
}

func testPBFRelation(id int64, keys, vals []uint64) []byte {
	out := testPBFVarintField(1, uint64(id))
	out = append(out, testPBFPackedUvarints(2, keys)...)
	out = append(out, testPBFPackedUvarints(3, vals)...)
	return out
}

func testPBFPackedUvarints(field int, values []uint64) []byte {
	if len(values) == 0 {
		return nil
	}
	var packed []byte
	for _, value := range values {
		packed = testPBFAppendUvarint(packed, value)
	}
	return testPBFBytesField(field, packed)
}

func testPBFPackedSint64s(field int, values []int64) []byte {
	if len(values) == 0 {
		return nil
	}
	var packed []byte
	for _, value := range values {
		packed = testPBFAppendUvarint(packed, testPBFZigZag(value))
	}
	return testPBFBytesField(field, packed)
}

func testPBFVarintField(field int, value uint64) []byte {
	out := testPBFAppendUvarint(nil, uint64(field<<3))
	return testPBFAppendUvarint(out, value)
}

func testPBFBytesField(field int, value []byte) []byte {
	out := testPBFAppendUvarint(nil, uint64(field<<3|2))
	out = testPBFAppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}

func testPBFAppendUvarint(dst []byte, value uint64) []byte {
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}

func testPBFZigZag(value int64) uint64 {
	return uint64(value<<1) ^ uint64(value>>63)
}
