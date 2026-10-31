package osmmini

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestCoordinateOnlyPassStillValidatesTheStringTable(t *testing.T) {
	group := testPBFPrimitiveGroup(1, testPBFNode(12, nil, nil, 480000000, 120000000))
	for _, table := range [][]byte{
		testPBFVarintField(1, 7), // String entries require length-delimited fields.
		{0x0a, 0x02, 'a'},        // Truncated string.
		{0x80},                   // Truncated field tag.
		{0x12, 0x05, 'a'},        // Unknown fields still need a valid wire payload.
	} {
		for _, tagged := range []bool{false, true} {
			calls := 0
			cb := Callbacks{Node: func(int64, float64, float64) error { calls++; return nil }}
			if tagged {
				cb.TaggedNode = func(Node) error { return nil }
			}
			// Field order is arbitrary; validate the table even after a group.
			raw := testPBFBytesField(2, group)
			raw = append(raw, testPBFBytesField(1, table)...)
			if err := processOSMData(raw, false, true, false, false, false, false, Options{}, cb); err == nil || calls != 0 {
				t.Fatalf("tagged=%v, table=%x: err=%v, callbacks=%d", tagged, table, err, calls)
			}
		}
	}
}

func TestDenseNodesRepeatedPackedFields(t *testing.T) {
	// Repeated packed fields are legal, including empty segments and an
	// interleaved field order. Deltas continue across their segment boundaries.
	raw := testPBFPackedSint64s(1, []int64{10, 2})
	raw = append(raw, testPBFPackedSint64s(8, []int64{480000000})...)
	raw = append(raw, testPBFPackedSint64s(9, []int64{110000000, -2, 1})...)
	raw = append(raw, testPBFBytesField(1, nil)...)
	raw = append(raw, testPBFPackedSint64s(1, []int64{-1})...)
	raw = append(raw, testPBFPackedSint64s(8, []int64{2, -1})...)
	raw = append(raw, testPBFPackedUvarints(10, []uint64{1, 2})...)
	raw = append(raw, testPBFPackedUvarints(10, []uint64{0, 0, 3, 4, 1, 2, 0})...)
	ctx := blockCtx{st: []string{"", "amenity", "cafe", "addr:housenumber", "7"}, granularity: 100}
	var coordinates []Node
	var tagged, addresses []Node
	err := processDenseNodes(raw, ctx, Options{TaggedNodeKey: func(key string) bool { return key == "amenity" }}, Callbacks{
		Node: func(id int64, lat, lon float64) error {
			coordinates = append(coordinates, Node{ID: id, Lat: lat, Lon: lon})
			return nil
		},
		AddressNode: func(n Node) error { addresses = append(addresses, n); return nil },
		TaggedNode:  func(n Node) error { tagged = append(tagged, n); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Node{{ID: 10, Lat: 48, Lon: 11}, {ID: 12, Lat: 48.0000002, Lon: 10.9999998}, {ID: 11, Lat: 48.0000001, Lon: 10.9999999}}
	if !reflect.DeepEqual(coordinates, want) {
		t.Fatalf("coordinates = %#v, want %#v", coordinates, want)
	}
	if len(tagged) != 2 || tagged[0].ID != 10 || tagged[1].ID != 11 || tagged[0].Tags["amenity"] != "cafe" {
		t.Fatalf("tagged = %#v", tagged)
	}
	if len(addresses) != 1 || addresses[0].ID != 11 || addresses[0].Tags["addr:housenumber"] != "7" {
		t.Fatalf("addresses = %#v", addresses)
	}
}

func TestDenseNodesRejectMalformedPackedStreamsBeforeCallbacks(t *testing.T) {
	good := testPBFDenseNodes([]int64{1}, []int64{1}, []int64{1}, nil)
	tests := map[string][]byte{
		"length mismatch": testPBFDenseNodes([]int64{1, 1}, []int64{1}, []int64{1}, nil),
		"unfinished ID":   append(good, testPBFBytesField(1, []byte{0x80})...),
		"unfinished tags": append(good, testPBFBytesField(10, []byte{0x80})...),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err := processDenseNodes(raw, blockCtx{granularity: 100}, Options{}, Callbacks{Node: func(int64, float64, float64) error {
				calls++
				return nil
			}})
			if err == nil || calls != 0 {
				t.Fatalf("err = %v, callbacks = %d; want error before any callbacks", err, calls)
			}
		})
	}
}

func TestDenseNodesCancellationWithinBlock(t *testing.T) {
	const count = 8000
	ids := make([]int64, count)
	for i := range ids {
		ids[i] = 1
	}
	raw := testPBFDenseNodes(ids, ids, ids, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := processDenseNodes(raw, blockCtx{granularity: 100}, Options{Context: ctx}, Callbacks{Node: func(int64, float64, float64) error {
		calls++
		cancel()
		return nil
	}})
	if !errors.Is(err, context.Canceled) || calls == 0 || calls > 1024 {
		t.Fatalf("err = %v, callbacks after cancellation = %d; want cancellation within a batch", err, calls)
	}
}

func TestExtractCancellationBetweenEntities(t *testing.T) {
	group := testPBFPrimitiveGroup(1, testPBFNode(1, nil, nil, 1, 1))
	group = append(group, testPBFPrimitiveGroup(1, testPBFNode(2, nil, nil, 1, 1))...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := Extract(bytes.NewReader(testPBFData([]string{""}, group)), Options{Context: ctx}, Callbacks{Node: func(int64, float64, float64) error {
		calls++
		cancel()
		return nil
	}})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err = %v, callbacks = %d; want cancellation after first entity", err, calls)
	}
}

func TestPackedTagsAndReferencesRetainIndependentResults(t *testing.T) {
	st := []string{"", "name", "First", "amenity", "cafe"}
	keys := [][]byte{{1}, {}, {3}}
	vals := [][]byte{{2, 4}}
	tags, err := buildTags(keys, vals, st, nil)
	if err != nil || !reflect.DeepEqual(tags, Tags{"name": "First", "amenity": "cafe"}) {
		t.Fatalf("tags = %v, err = %v", tags, err)
	}
	filtered, err := buildTags(keys, vals, st, func(key string) bool { return key == "amenity" })
	if err != nil || !reflect.DeepEqual(filtered, Tags{"amenity": "cafe"}) {
		t.Fatalf("filtered = %v, err = %v", filtered, err)
	}
	filtered["amenity"] = "changed"
	if tags["amenity"] != "cafe" {
		t.Fatal("independent tag results alias each other")
	}
	refs, err := decodeWayRefs([][]byte{{testPBFZigZagByte(10)}, {}, {testPBFZigZagByte(-2), testPBFZigZagByte(3)}})
	if err != nil || !reflect.DeepEqual(refs, []int64{10, 8, 11}) {
		t.Fatalf("refs = %v, err = %v", refs, err)
	}
}

func testPBFZigZagByte(value int64) byte { return byte(testPBFZigZag(value)) }

func TestPackedTagsValidateUnmatchedMalformedTail(t *testing.T) {
	for _, test := range []struct{ keys, vals [][]byte }{
		{[][]byte{{1, 0x80}}, [][]byte{{2}}},
		{[][]byte{{1}}, [][]byte{{2, 0x80}}},
	} {
		_, err := buildTags(test.keys, test.vals, []string{"", "name", "x"}, nil)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("err = %v, want unexpected EOF", err)
		}
	}
}

func TestPackedValueCountMatchesDecoder(t *testing.T) {
	for _, values := range [][]uint64{{}, {0, 1, 127, 128, 16384}, {^uint64(0)}} {
		var packed []byte
		for _, value := range values {
			packed = testPBFAppendUvarint(packed, value)
		}
		count, err := packedValueCount([][]byte{nil, packed, nil})
		if err != nil || count != len(values) {
			t.Fatalf("count(%v) = %d, %v; want %d", values, count, err, len(values))
		}
	}
	for _, malformed := range [][]byte{
		{0x80},
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02},
		{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80},
	} {
		_, countErr := packedValueCount([][]byte{malformed})
		i := 0
		_, decodeErr := readUvarint(malformed, &i)
		if countErr == nil || decodeErr == nil {
			t.Fatalf("malformed %x: count err=%v, decode err=%v; both must reject", malformed, countErr, decodeErr)
		}
	}
}

func FuzzPackedCursor(f *testing.F) {
	f.Add([]byte{0, 1, 127, 128, 1}, uint8(2))
	f.Add([]byte{0x80}, uint8(1))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 1}, uint8(10))
	f.Fuzz(func(t *testing.T, data []byte, split uint8) {
		cut := min(int(split), len(data))
		segments := [][]byte{data[:cut], nil, data[cut:]}
		count, countErr := packedValueCount(segments)
		cursor := packedCursor{segs: segments}
		decoded := 0
		for {
			_, ok, err := cursor.next()
			if err != nil {
				if countErr == nil {
					t.Fatalf("cursor rejected count-validated data %x: %v", data, err)
				}
				return
			}
			if !ok {
				if countErr != nil || count != decoded {
					t.Fatalf("count = %d/%v, decoded = %d for %x", count, countErr, decoded, data)
				}
				return
			}
			decoded++
		}
	})
}
