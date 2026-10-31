package osmmini

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkPBFSpatialIndexBlocks(b *testing.B) {
	group := testPBFPrimitiveGroup(1, testPBFNode(1, nil, nil, 480000000, 120000000))
	group = append(group, testPBFBytesField(25, bytes.Repeat([]byte{'x'}, 8192))...)
	block := testPBFData([]string{""}, group)
	input := bytes.Repeat(block, 512)
	root := b.TempDir()
	path := filepath.Join(root, "region.osm.pbf")
	if err := os.WriteFile(path, input, 0600); err != nil {
		b.Fatal(err)
	}
	output := filepath.Join(root, "region.idx")
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		index, err := BuildPBFSpatialIndex(b.Context(), path, output, nil)
		if err != nil || len(index.Blocks) != 512 {
			b.Fatalf("blocks: %v", err)
		}
	}
}
