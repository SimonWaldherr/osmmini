package osmmini

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestExtractRetainsEntitiesAcrossReusedBlockBuffers(t *testing.T) {
	var data []byte
	var want []string
	for i, padding := range []int{0, 8192, 3, 32768, 1} {
		name := "Campus " + strings.Repeat("M", padding)
		want = append(want, name)
		table := []string{"", "name", name, "amenity", "school"}
		group := testPBFPrimitiveGroup(1, testPBFNode(int64(i+1), []uint64{1, 3}, []uint64{2, 4}, 480000000, 120000000))
		block := testPBFData(table, group)
		if i%2 == 1 {
			raw := testPBFBytesField(1, testPBFStringTable(table))
			raw = append(raw, testPBFBytesField(2, group)...)
			block = testPBFBlobFrame(testPBFZlibBlob(t, raw, len(raw)))
		}
		data = append(data, block...)
	}
	var got []Node
	err := Extract(bytes.NewReader(data), Options{}, Callbacks{TaggedNode: func(node Node) error { got = append(got, node); return nil }})
	if err != nil || len(got) != len(want) {
		t.Fatalf("entities: %d, %v", len(got), err)
	}
	for i, node := range got {
		if node.ID != int64(i+1) || node.Tags["name"] != want[i] || node.Tags["amenity"] != "school" {
			t.Fatalf("block %d entity changed after buffer reuse", i)
		}
	}
}

func TestPBFBlobLimitsAndTruncatedSizeDeclarations(t *testing.T) {
	var decoder pbfBlobDecoder
	defer decoder.close()
	if _, err := decoder.readBlob(bytes.NewReader(nil), maxPBFEncodedBytes+1); err == nil {
		t.Fatal("oversized encoded blob accepted")
	}
	if _, err := decoder.readBlob(bytes.NewReader([]byte{1, 2}), maxPBFEncodedBytes); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short blob: %v", err)
	}
	if decoder.encoded.Cap() > 4096 {
		t.Fatalf("truncated input reserved declared size: %d", decoder.encoded.Cap())
	}
	raw := bytes.Repeat([]byte{'x'}, maxPBFDecodedBytes+1)
	for _, blob := range [][]byte{testPBFBytesField(1, raw), testPBFZlibBlob(t, raw, 0)} {
		if _, err := decoder.decode(blob); err == nil || !strings.Contains(err.Error(), "decoded size limit") {
			t.Fatalf("oversized decoded blob: %v", err)
		}
	}
}

func TestPBFBlockBoundsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	raw := testPBFBytesField(1, testPBFStringTable([]string{""}))
	raw = append(raw, testPBFBytesField(2, testPBFPrimitiveGroup(1, testPBFNode(1, nil, nil, 1, 1)))...)
	if _, _, err := pbfBlockNodeBounds(ctx, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bounds scan: %v", err)
	}
}

func TestExtractSkipsUnknownBlockCompression(t *testing.T) {
	known := testPBFData([]string{""}, testPBFPrimitiveGroup(1, testPBFNode(1, nil, nil, 1, 1)))
	blob := testPBFBytesField(7, []byte("extension-specific compressed bytes"))
	header := testPBFBytesField(1, []byte("VendorMetadata"))
	header = append(header, testPBFVarintField(3, uint64(len(blob)))...)
	frame := make([]byte, 4)
	binary.BigEndian.PutUint32(frame, uint32(len(header)))
	frame = append(frame, header...)
	frame = append(frame, blob...)
	data := append(append(append([]byte(nil), known...), frame...), known...)
	count := 0
	err := Extract(bytes.NewReader(data), Options{}, Callbacks{Node: func(_ int64, _, _ float64) error { count++; return nil }})
	if err != nil || count != 2 {
		t.Fatalf("extension block: nodes=%d, err=%v", count, err)
	}
	if err := Extract(bytes.NewReader(testPBFBlobFrame(blob)), Options{}, Callbacks{}); err == nil {
		t.Fatal("unsupported compression in OSMData accepted")
	}
}
