package mobile

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

func TestSearchAcceptsEscapedJSONTextAcrossStorageFormats(t *testing.T) {
	path := writeSearchCatalog(t, []SearchResult{{ID: 42, Kind: "node", Label: "Café"}})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte(`"text":"cafe"`), []byte(`"text":"\u0063afe"`))
	if !bytes.Contains(data, []byte(`\u0063`)) {
		t.Fatal("fixture lacks escaped search text")
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(path + ".idx")
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(index[104:108], uint32(len(data)))
	if err = os.WriteFile(path+".idx", index, 0600); err != nil {
		t.Fatal(err)
	}
	compact := compactSearchCatalog(t, path)
	for _, scenario := range []struct{ name, path string }{{"legacy", path}, {"compact", compact}, {"compressed", compressedSearchCatalog(t, compact)}} {
		t.Run(scenario.name, func(t *testing.T) {
			reader, err := openSearch(scenario.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.close()
			found, err := reader.search(t.Context(), "cafe", 20)
			if err != nil || len(found) != 1 || found[0].ID != 42 {
				t.Fatalf("escaped record not found: %+v, %v", found, err)
			}
		})
	}
}
