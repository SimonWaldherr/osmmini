package mobile

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"

	osm "simonwaldherr.de/go/osmmini/core"
)

const searchNodeSortIDs = 128 << 10
const searchNodeRowBytes = 24
const searchNodeCacheRows = 4096

// References are sorted in bounded chunks instead of retaining every selected
// way's IDs and a second regional coordinate array on the Go heap.
type searchNodeBuilder struct {
	ctx    context.Context
	parent string
	dir    string
	ids    []int64
	runs   []string
}

func (b *searchNodeBuilder) close() {
	if b.dir != "" {
		_ = os.RemoveAll(b.dir)
	}
}

func (b *searchNodeBuilder) ensureDirectory() error {
	if b.dir != "" {
		return nil
	}
	var err error
	b.dir, err = os.MkdirTemp(b.parent, ".search-nodes-*")
	return err
}

func (b *searchNodeBuilder) add(ids []int64) error {
	for len(ids) > 0 {
		if err := b.ctx.Err(); err != nil {
			return err
		}
		if b.ids == nil {
			b.ids = make([]int64, 0, searchNodeSortIDs)
		}
		n := min(len(ids), searchNodeSortIDs-len(b.ids))
		b.ids = append(b.ids, ids[:n]...)
		ids = ids[n:]
		if len(b.ids) == searchNodeSortIDs {
			if err := b.flush(); err != nil {
				return err
			}
		}
	}
	return b.ctx.Err()
}

func (b *searchNodeBuilder) flush() error {
	if err := b.ctx.Err(); err != nil || len(b.ids) == 0 {
		return err
	}
	if err := b.ensureDirectory(); err != nil {
		return err
	}
	slices.Sort(b.ids)
	b.ids = slices.Compact(b.ids)
	path := filepath.Join(b.dir, fmt.Sprintf("run-%d", len(b.runs)))
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	writer := bufio.NewWriterSize(file, 64<<10)
	for i, id := range b.ids {
		if i%4096 == 0 {
			if err = b.ctx.Err(); err != nil {
				break
			}
		}
		if err = writeSearchNodeID(writer, id); err != nil {
			break
		}
	}
	if err == nil {
		err = writer.Flush()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	b.runs = append(b.runs, path)
	b.ids = b.ids[:0]
	return nil
}

func writeSearchNodeID(writer *bufio.Writer, id int64) error {
	if writer.Available() < 8 {
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	data := writer.AvailableBuffer()[:8]
	binary.LittleEndian.PutUint64(data, uint64(id))
	_, err := writer.Write(data)
	return err
}

type searchNodeRun struct {
	reader *bufio.Reader
	id     int64
	buffer [8]byte
}

func (r *searchNodeRun) advance() error {
	if _, err := io.ReadFull(r.reader, r.buffer[:]); err != nil {
		return err
	}
	r.id = int64(binary.LittleEndian.Uint64(r.buffer[:]))
	return nil
}

func repairSearchNodeHeap(queue []*searchNodeRun, parent int) {
	for child := parent*2 + 1; child < len(queue); child = parent*2 + 1 {
		if child+1 < len(queue) && queue[child+1].id < queue[child].id {
			child++
		}
		if queue[parent].id <= queue[child].id {
			return
		}
		queue[parent], queue[child] = queue[child], queue[parent]
		parent = child
	}
}

func mergeSearchNodeIDs(ctx context.Context, paths []string, emit func(int64) error) error {
	var queue []*searchNodeRun
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		run := &searchNodeRun{reader: bufio.NewReaderSize(file, 32<<10)}
		if err := run.advance(); err == io.EOF {
			continue
		} else if err != nil {
			return err
		}
		queue = append(queue, run)
	}
	for i := len(queue)/2 - 1; i >= 0; i-- {
		repairSearchNodeHeap(queue, i)
	}
	var previous int64
	havePrevious := false
	for i := 0; len(queue) > 0; i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		run := queue[0]
		if !havePrevious || run.id != previous {
			if err := emit(run.id); err != nil {
				return err
			}
			previous, havePrevious = run.id, true
		}
		if err := run.advance(); err == io.EOF {
			queue[0] = queue[len(queue)-1]
			queue = queue[:len(queue)-1]
		} else if err != nil {
			return err
		}
		repairSearchNodeHeap(queue, 0)
	}
	return ctx.Err()
}

func (b *searchNodeBuilder) finish() (_ *searchNodeTable, err error) {
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	if len(b.ids) == 0 && len(b.runs) == 0 {
		return nil, nil
	}
	if err := b.ensureDirectory(); err != nil {
		return nil, err
	}
	if len(b.runs) > 0 {
		if err := b.flush(); err != nil {
			return nil, err
		}
	}
	runs := b.runs
	for pass := 0; len(runs) > searchMergeRuns; pass++ {
		var next []string
		for i := 0; i < len(runs); i += searchMergeRuns {
			group := runs[i:min(len(runs), i+searchMergeRuns)]
			path := filepath.Join(b.dir, fmt.Sprintf("merge-%d-%d", pass, i))
			file, err := os.Create(path)
			if err != nil {
				return nil, err
			}
			writer := bufio.NewWriterSize(file, 64<<10)
			err = mergeSearchNodeIDs(b.ctx, group, func(id int64) error { return writeSearchNodeID(writer, id) })
			if err == nil {
				err = writer.Flush()
			}
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return nil, err
			}
			next = append(next, path)
			for _, old := range group {
				if err := os.Remove(old); err != nil {
					return nil, err
				}
			}
		}
		runs = next
	}
	file, err := os.Create(filepath.Join(b.dir, "coordinates"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			file.Close()
		}
	}()
	writer := bufio.NewWriterSize(file, 64<<10)
	count := 0
	emit := func(id int64) error {
		if count%4096 == 0 {
			if err := b.ctx.Err(); err != nil {
				return err
			}
		}
		if err := writeSearchNodeID(writer, id); err != nil {
			return err
		}
		count++
		return nil
	}
	if len(runs) == 0 {
		slices.Sort(b.ids)
		b.ids = slices.Compact(b.ids)
		for _, id := range b.ids {
			if err = emit(id); err != nil {
				return nil, err
			}
		}
	} else if err = mergeSearchNodeIDs(b.ctx, runs, emit); err != nil {
		return nil, err
	}
	for i := range count {
		if i%4096 == 0 {
			if err = b.ctx.Err(); err != nil {
				return nil, err
			}
		}
		if err = writeSearchNodeCoordinate(writer, math.NaN(), 0); err != nil {
			return nil, err
		}
	}
	if err = writer.Flush(); err != nil {
		return nil, err
	}
	if _, err = file.Seek(int64(count)*8, io.SeekStart); err != nil {
		return nil, err
	}
	b.ids = nil
	data, unmap, _ := mapSearchNodeFile(file, int64(count)*searchNodeRowBytes)
	return &searchNodeTable{
		ctx: b.ctx, file: file, data: data, unmap: unmap, count: count,
		writer: bufio.NewWriterSize(file, 64<<10), lastID: math.MinInt64, lookupAt: -1,
	}, nil
}

func writeSearchNodeCoordinate(writer *bufio.Writer, lat, lon float64) error {
	if writer.Available() < 16 {
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	data := writer.AvailableBuffer()[:16]
	binary.LittleEndian.PutUint64(data, math.Float64bits(lat))
	binary.LittleEndian.PutUint64(data[8:], math.Float64bits(lon))
	_, err := writer.Write(data)
	return err
}

// IDs stay immutable in this private file. The usual sorted node pass updates
// coordinates in buffered order; an unsorted source switches to WriteAt once.
// Read-only mappings let the OS reclaim file-backed pages under memory pressure.
type searchNodeTable struct {
	ctx                  context.Context
	file                 *os.File
	data                 []byte
	unmap                func() error
	count                int
	writer               *bufio.Writer
	written              int
	cursor               int
	lastID               int64
	lookupAt             int
	idCache              []byte
	idCacheAt            int
	idCacheCount         int
	coordinateCache      []byte
	coordinateCacheAt    int
	coordinateCacheCount int
}

func (t *searchNodeTable) close() {
	if t.unmap != nil {
		_ = t.unmap()
		t.unmap = nil
	}
	_ = t.file.Close()
}

func (t *searchNodeTable) id(index int) (int64, error) {
	if index < 0 || index >= t.count {
		return 0, fmt.Errorf("invalid temporary search node index")
	}
	if t.data != nil {
		return int64(binary.LittleEndian.Uint64(t.data[index*8:])), nil
	}
	if index < t.idCacheAt || index >= t.idCacheAt+t.idCacheCount {
		if t.idCache == nil {
			t.idCache = make([]byte, searchNodeCacheRows*8)
		}
		first := index / searchNodeCacheRows * searchNodeCacheRows
		n := min(searchNodeCacheRows, t.count-first)
		if _, err := t.file.ReadAt(t.idCache[:n*8], int64(first)*8); err != nil {
			return 0, err
		}
		t.idCacheAt, t.idCacheCount = first, n
	}
	return int64(binary.LittleEndian.Uint64(t.idCache[(index-t.idCacheAt)*8:])), nil
}

func (t *searchNodeTable) lowerBound(id int64) (int, error) {
	lo, hi := 0, t.count
	if t.data != nil {
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			if int64(binary.LittleEndian.Uint64(t.data[mid*8:])) < id {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		return lo, nil
	}
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		value, err := t.id(mid)
		if err != nil {
			return 0, err
		}
		if value < id {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, nil
}

func (t *searchNodeTable) finishCoordinates() error {
	if t.writer != nil {
		if err := t.writer.Flush(); err != nil {
			return err
		}
		t.writer = nil
	}
	t.coordinateCacheCount = 0
	return t.ctx.Err()
}

func (t *searchNodeTable) record(id int64, lat, lon float64) error {
	if id < t.lastID {
		if err := t.finishCoordinates(); err != nil {
			return err
		}
		var err error
		t.cursor, err = t.lowerBound(id)
		if err != nil {
			return err
		}
	}
	t.lastID = id
	for t.cursor < t.count {
		needed, err := t.id(t.cursor)
		if err != nil {
			return err
		}
		if needed > id {
			return nil
		}
		if needed == id {
			break
		}
		t.cursor++
		if t.cursor%4096 == 0 {
			if err := t.ctx.Err(); err != nil {
				return err
			}
		}
	}
	if t.cursor == t.count {
		return nil
	}
	if t.cursor < t.written {
		if err := t.finishCoordinates(); err != nil {
			return err
		}
	}
	if t.writer == nil {
		var data [16]byte
		binary.LittleEndian.PutUint64(data[:], math.Float64bits(lat))
		binary.LittleEndian.PutUint64(data[8:], math.Float64bits(lon))
		_, err := t.file.WriteAt(data[:], int64(t.count)*8+int64(t.cursor)*16)
		t.coordinateCacheCount = 0
		return err
	}
	for t.written < t.cursor {
		if t.written%4096 == 0 {
			if err := t.ctx.Err(); err != nil {
				return err
			}
		}
		if err := writeSearchNodeCoordinate(t.writer, math.NaN(), 0); err != nil {
			return err
		}
		t.written++
	}
	if err := writeSearchNodeCoordinate(t.writer, lat, lon); err != nil {
		return err
	}
	t.written++
	return nil
}

// Consecutive vertices frequently have adjacent node IDs. Reuse that position
// before doing another full binary search; repeated references reuse it too.
func (t *searchNodeTable) coordinateIndex(id int64) (int, error) {
	if t.lookupAt >= 0 {
		value, err := t.id(t.lookupAt)
		if err != nil {
			return 0, err
		}
		if value == id {
			return t.lookupAt, nil
		}
		next := t.lookupAt + 1
		if id < value {
			next = t.lookupAt - 1
		}
		if next >= 0 && next < t.count {
			value, err := t.id(next)
			if err != nil {
				return 0, err
			}
			if value == id {
				t.lookupAt = next
				return next, nil
			}
		}
	}
	at, err := t.lowerBound(id)
	t.lookupAt = -1
	if err == nil && at < t.count {
		t.lookupAt = at
	}
	return at, err
}

func (t *searchNodeTable) coordinate(id int64) (osm.Coord, bool, error) {
	at, err := t.coordinateIndex(id)
	if err != nil || at == t.count {
		return osm.Coord{}, false, err
	}
	value, err := t.id(at)
	if err != nil {
		return osm.Coord{}, false, err
	}
	if value != id {
		return osm.Coord{}, false, nil
	}
	var data []byte
	if t.data != nil {
		start := int64(t.count)*8 + int64(at)*16
		data = t.data[start : start+16]
	} else {
		if at < t.coordinateCacheAt || at >= t.coordinateCacheAt+t.coordinateCacheCount {
			if t.coordinateCache == nil {
				t.coordinateCache = make([]byte, searchNodeCacheRows*16)
			}
			first := at / searchNodeCacheRows * searchNodeCacheRows
			n := min(searchNodeCacheRows, t.count-first)
			if _, err := t.file.ReadAt(t.coordinateCache[:n*16], int64(t.count)*8+int64(first)*16); err != nil {
				return osm.Coord{}, false, err
			}
			t.coordinateCacheAt, t.coordinateCacheCount = first, n
		}
		data = t.coordinateCache[(at-t.coordinateCacheAt)*16:]
	}
	lat := math.Float64frombits(binary.LittleEndian.Uint64(data))
	if math.IsNaN(lat) {
		return osm.Coord{}, false, nil
	}
	return osm.Coord{Lat: lat, Lon: math.Float64frombits(binary.LittleEndian.Uint64(data[8:]))}, true, nil
}
