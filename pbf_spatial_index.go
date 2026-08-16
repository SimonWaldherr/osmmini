package osmmini

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const pbfSpatialIndexFormatVersion = 1

// PBFSpatialIndexPath returns the short default sidecar path for an OSM PBF.
// For example, europe.osm.pbf becomes europe.idx. The index is immutable once
// published and can be rebuilt safely while a previous index continues to
// serve range queries.
func PBFSpatialIndexPath(pbfPath string) string {
	base := strings.TrimSuffix(pbfPath, ".osm.pbf")
	if base == pbfPath {
		base = strings.TrimSuffix(pbfPath, filepath.Ext(pbfPath))
	}
	return base + ".idx"
}

// PBFBlockBounds is the geographic extent of the nodes in a single OSMData
// blob. It permits a zero-width or zero-height extent because a blob can
// legitimately contain only one node.
type PBFBlockBounds struct {
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

func (b PBFBlockBounds) intersects(window CoordWindow) bool {
	return b.MinLat <= window.MaxLat && b.MaxLat >= window.MinLat &&
		b.MinLon <= window.MaxLon && b.MaxLon >= window.MinLon
}

// PBFSpatialBlock identifies an OSMData blob in the original PBF. Offset and
// Length address the compressed on-disk PBF block, not a decoded copy.
type PBFSpatialBlock struct {
	Offset    int64          `json:"offset"`
	Length    int64          `json:"length"`
	Bounds    PBFBlockBounds `json:"bounds"`
	NodeCount int64          `json:"node_count"`
}

// PBFSourceFingerprint binds an index to one exact input file. Size and
// modtime make routine freshness checks cheap; SHA-256 records the complete
// source identity produced during the streaming index pass.
type PBFSourceFingerprint struct {
	Size            int64  `json:"size"`
	ModTimeUnixNano int64  `json:"mod_time_unix_nano"`
	SHA256          string `json:"sha256"`
}

// PBFSpatialIndex is a compact, disk-backed locator for node-bearing PBF
// blocks. It intentionally does not copy OSM entities: range queries return
// byte ranges that a later streaming extractor can seek and decode.
type PBFSpatialIndex struct {
	FormatVersion int                  `json:"format_version"`
	CreatedAt     time.Time            `json:"created_at"`
	Source        PBFSourceFingerprint `json:"source"`
	Blocks        []PBFSpatialBlock    `json:"blocks"`
}

// PBFSpatialIndexProgress describes a bounded index-build update. No PBF
// geometry or filesystem paths are included, so callers can expose it safely.
type PBFSpatialIndexProgress struct {
	BytesRead     int64 `json:"bytes_read"`
	SourceBytes   int64 `json:"source_bytes"`
	BlocksRead    int64 `json:"blocks_read"`
	IndexedBlocks int64 `json:"indexed_blocks"`
}

// BuildPBFSpatialIndex streams pbfPath once and atomically publishes an
// index at indexPath. It holds at most one decoded PBF blob plus the compact
// block directory in memory; the source PBF is never loaded wholesale.
func BuildPBFSpatialIndex(ctx context.Context, pbfPath, indexPath string, progress func(PBFSpatialIndexProgress)) (*PBFSpatialIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(pbfPath)
	if err != nil {
		return nil, fmt.Errorf("stat PBF: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("PBF path is a directory")
	}
	if indexPath == "" {
		indexPath = PBFSpatialIndexPath(pbfPath)
	}
	file, err := os.Open(pbfPath)
	if err != nil {
		return nil, fmt.Errorf("open PBF: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	readExact := func(buf []byte) error {
		if _, err := io.ReadFull(file, buf); err != nil {
			return err
		}
		_, _ = hash.Write(buf)
		return nil
	}
	index := &PBFSpatialIndex{
		FormatVersion: pbfSpatialIndexFormatVersion,
		CreatedAt:     time.Now().UTC(),
		Source: PBFSourceFingerprint{
			Size:            info.Size(),
			ModTimeUnixNano: info.ModTime().UnixNano(),
		},
		Blocks: make([]PBFSpatialBlock, 0, 1024),
	}

	var bytesRead, blocksRead int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset := bytesRead
		var lengthBuffer [4]byte
		err := readExact(lengthBuffer[:])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read PBF block length at byte %d: %w", offset, err)
		}
		bytesRead += int64(len(lengthBuffer))
		headerLength := binary.BigEndian.Uint32(lengthBuffer[:])
		if headerLength == 0 || headerLength > 64<<10 {
			return nil, fmt.Errorf("invalid PBF BlobHeader size %d at byte %d", headerLength, offset)
		}
		header := make([]byte, headerLength)
		if err := readExact(header); err != nil {
			return nil, fmt.Errorf("read PBF BlobHeader at byte %d: %w", offset, err)
		}
		bytesRead += int64(len(header))
		typ, dataSize, err := parseBlobHeader(header)
		if err != nil {
			return nil, fmt.Errorf("parse PBF BlobHeader at byte %d: %w", offset, err)
		}
		if dataSize <= 0 {
			return nil, fmt.Errorf("invalid PBF blob size %d at byte %d", dataSize, offset)
		}
		blob := make([]byte, dataSize)
		if err := readExact(blob); err != nil {
			return nil, fmt.Errorf("read PBF blob at byte %d: %w", offset, err)
		}
		bytesRead += int64(len(blob))
		blocksRead++
		if typ == "OSMData" {
			raw, err := parseAndDecompressBlob(blob)
			if err != nil {
				return nil, fmt.Errorf("decode PBF blob at byte %d: %w", offset, err)
			}
			bounds, nodes, err := pbfBlockNodeBounds(raw)
			if err != nil {
				return nil, fmt.Errorf("index PBF blob at byte %d: %w", offset, err)
			}
			if nodes > 0 {
				index.Blocks = append(index.Blocks, PBFSpatialBlock{
					Offset: offset, Length: bytesRead - offset, Bounds: bounds, NodeCount: nodes,
				})
			}
		}
		if progress != nil {
			progress(PBFSpatialIndexProgress{BytesRead: bytesRead, SourceBytes: info.Size(), BlocksRead: blocksRead, IndexedBlocks: int64(len(index.Blocks))})
		}
	}
	if bytesRead != info.Size() {
		return nil, fmt.Errorf("PBF changed while indexing: read %d of %d bytes", bytesRead, info.Size())
	}
	index.Source.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if err := writePBFSpatialIndex(indexPath, index); err != nil {
		return nil, err
	}
	return index, nil
}

func pbfBlockNodeBounds(raw []byte) (PBFBlockBounds, int64, error) {
	bounds := PBFBlockBounds{MinLat: math.Inf(1), MaxLat: math.Inf(-1), MinLon: math.Inf(1), MaxLon: math.Inf(-1)}
	var count int64
	err := processOSMData(raw, false, true, false, false, false, false, Options{}, Callbacks{
		Node: func(_ int64, lat, lon float64) error {
			bounds.MinLat = min(bounds.MinLat, lat)
			bounds.MaxLat = max(bounds.MaxLat, lat)
			bounds.MinLon = min(bounds.MinLon, lon)
			bounds.MaxLon = max(bounds.MaxLon, lon)
			count++
			return nil
		},
	})
	if err != nil {
		return PBFBlockBounds{}, 0, err
	}
	return bounds, count, nil
}

func writePBFSpatialIndex(path string, index *PBFSpatialIndex) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create index directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".osmmini-pbfidx-*")
	if err != nil {
		return fmt.Errorf("create temporary index: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(index); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode index: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync index: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close index: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish index: %w", err)
	}
	return nil
}

// LoadPBFSpatialIndex opens a previously published index. It deliberately
// does not hash the PBF again; FreshFor performs the inexpensive size and
// modification-time freshness check before a range query is used.
func LoadPBFSpatialIndex(path string) (*PBFSpatialIndex, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var index PBFSpatialIndex
	if err := json.NewDecoder(io.LimitReader(file, 256<<20)).Decode(&index); err != nil {
		return nil, fmt.Errorf("decode PBF spatial index: %w", err)
	}
	if index.FormatVersion != pbfSpatialIndexFormatVersion {
		return nil, fmt.Errorf("unsupported PBF spatial index format %d", index.FormatVersion)
	}
	return &index, nil
}

// FreshFor reports whether the index was built for the current source file.
// It is intentionally O(1); the full SHA-256 is retained in the index for
// explicit verification workflows without making every startup scan the PBF.
func (index *PBFSpatialIndex) FreshFor(pbfPath string) bool {
	if index == nil || index.FormatVersion != pbfSpatialIndexFormatVersion {
		return false
	}
	info, err := os.Stat(pbfPath)
	return err == nil && !info.IsDir() && info.Size() == index.Source.Size && info.ModTime().UnixNano() == index.Source.ModTimeUnixNano
}

// BlocksForWindow returns byte ranges whose node extent intersects window.
// The caller can seek only these blocks for a region-oriented streaming pass.
func (index *PBFSpatialIndex) BlocksForWindow(window CoordWindow) []PBFSpatialBlock {
	if index == nil || !window.Valid() {
		return nil
	}
	blocks := make([]PBFSpatialBlock, 0)
	for _, block := range index.Blocks {
		if block.Length > 0 && block.NodeCount > 0 && block.Bounds.intersects(window) {
			blocks = append(blocks, block)
		}
	}
	return blocks
}
