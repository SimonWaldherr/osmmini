package osmmini

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func testPBFZlibBlob(t testing.TB, raw []byte, size int) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	blob := testPBFVarintField(2, uint64(size))
	return append(blob, testPBFBytesField(3, compressed.Bytes())...)
}

func TestPBFZlibBlobSizeHintsAndIntegrity(t *testing.T) {
	raw := bytes.Repeat([]byte("OSM primitive block data\x00"), 8000)
	for _, size := range []int{0, -1, len(raw)} {
		decoded, err := parseAndDecompressBlob(testPBFZlibBlob(t, raw, size))
		if err != nil || !bytes.Equal(decoded, raw) {
			t.Fatalf("size hint %d: decoded %d bytes, err=%v", size, len(decoded), err)
		}
	}
	for _, size := range []int{1, len(raw) - 1, len(raw) + 1, 1 << 30} {
		_, err := parseAndDecompressBlob(testPBFZlibBlob(t, raw, size))
		if err == nil || !strings.Contains(err.Error(), "zlib blob size mismatch") {
			t.Fatalf("size hint %d: want size mismatch, got %v", size, err)
		}
	}
	for _, test := range []struct {
		name string
		cut  int
		want error
	}{
		{"checksum", 0, zlib.ErrChecksum},
		{"truncated", 2, io.ErrUnexpectedEOF},
	} {
		t.Run(test.name, func(t *testing.T) {
			var compressed bytes.Buffer
			writer := zlib.NewWriter(&compressed)
			_, _ = writer.Write(raw)
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			data := compressed.Bytes()
			if test.cut == 0 {
				data[len(data)-1] ^= 1
			} else {
				data = data[:len(data)-test.cut]
			}
			blob := testPBFVarintField(2, uint64(len(raw)))
			blob = append(blob, testPBFBytesField(3, data)...)
			if _, err := parseAndDecompressBlob(blob); !errors.Is(err, test.want) {
				t.Fatalf("want %v, got %v", test.want, err)
			}
		})
	}
}

func TestPBFBlobDecoderReadsIndependentBlocksAndRecoversAfterErrors(t *testing.T) {
	var decoder pbfBlobDecoder
	defer decoder.close()
	first := []byte("first compressed block")
	second := bytes.Repeat([]byte("second block"), 1024)
	for _, raw := range [][]byte{first, second} {
		got, err := decoder.decode(testPBFZlibBlob(t, raw, len(raw)))
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("decode: %v", err)
		}
	}
	bad := testPBFZlibBlob(t, first, len(first))
	bad[len(bad)-1] ^= 1
	for _, test := range []struct {
		blob []byte
		want error
	}{
		{bad, zlib.ErrChecksum},
		{testPBFBytesField(3, []byte{0}), io.ErrUnexpectedEOF},
		{testPBFBytesField(3, []byte{0, 0}), zlib.ErrHeader},
	} {
		if _, err := decoder.decode(test.blob); !errors.Is(err, test.want) {
			t.Fatalf("want %v, got %v", test.want, err)
		}
		got, err := decoder.decode(testPBFZlibBlob(t, second, len(second)))
		if err != nil || !bytes.Equal(got, second) {
			t.Fatalf("decode after %v: %v", test.want, err)
		}
	}
}

func TestExtractReadsMixedBlobEncodings(t *testing.T) {
	var stream []byte
	for id := int64(1); id <= 3; id++ {
		raw := testPBFBytesField(1, testPBFStringTable([]string{""}))
		raw = append(raw, testPBFBytesField(2, testPBFPrimitiveGroup(1, testPBFNode(id, nil, nil, 1, 1)))...)
		blob := testPBFBytesField(1, raw)
		if id != 2 {
			blob = testPBFZlibBlob(t, raw, len(raw))
		}
		stream = append(stream, testPBFBlobFrame(blob)...)
	}
	var ids []int64
	err := Extract(bytes.NewReader(stream), Options{}, Callbacks{Node: func(id int64, _, _ float64) error {
		ids = append(ids, id)
		return nil
	}})
	if err != nil || len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 3 {
		t.Fatalf("mixed raw/zlib stream: nodes=%v, err=%v", ids, err)
	}
}

func testPBFBlobFrame(blob []byte) []byte {
	header := testPBFBytesField(1, []byte("OSMData"))
	header = append(header, testPBFVarintField(3, uint64(len(blob)))...)
	frame := make([]byte, 4, 4+len(header)+len(blob))
	binary.BigEndian.PutUint32(frame, uint32(len(header)))
	frame = append(frame, header...)
	return append(frame, blob...)
}

func TestExtractRejectsTruncatedBlocks(t *testing.T) {
	raw := testPBFBytesField(1, testPBFStringTable([]string{""}))
	raw = append(raw, testPBFBytesField(2, testPBFPrimitiveGroup(1, testPBFNode(1, nil, nil, 1, 1)))...)
	frame := testPBFBlobFrame(testPBFZlibBlob(t, raw, len(raw)))
	for _, prefix := range [][]byte{nil, frame} {
		for cut := 1; cut < len(frame); cut++ {
			stream := append(append([]byte(nil), prefix...), frame[:cut]...)
			if err := Extract(bytes.NewReader(stream), Options{}, Callbacks{}); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("prefix=%d, cut=%d: want unexpected EOF, got %v", len(prefix), cut, err)
			}
		}
	}
}
