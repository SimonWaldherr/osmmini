package osmmini

// The mobile graph is an immutable, little-endian adjacency file. Coordinates,
// edges and the spatial grid stay file-backed; the same routing algorithms work
// with both the server's map graph and this representation. No unsafe casts or
// machine-dependent Go serialization form part of the on-disk contract.
import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

const graphHeaderSize = 96
const graphNodeSize = 32
const graphEdgeSize = 16
const graphCellSize = 12
const graphAttributeSize = 32
const graphMagic = "OSMMGR02"
const graphLegacyMagic = "OSMMGR01"
const graphLegacyNodeSize = 40
const graphLegacyEdgeSize = 48
const graphLegacyCellSize = 16

type graphMetadata struct {
	Classes []string               `json:"classes"`
	Streets map[string]streetEntry `json:"streets"`
}

// Attributes are keyed by their float bits so the compact representation
// preserves exact values, including signed zero, without quantization.
type graphEdgeAttributes struct {
	speed, height, weight uint64
	class                 uint32
}

type diskGraph struct {
	data                                           []byte
	nodes, edgeCount, edgesOffset, cellsOffset     int
	nodeSize, edgeSize, cellSize, attributesOffset int
	chainsOffset, verticesOffset                   int
	storedEdges                                    int
	compact                                        bool
	classes                                        []string
	release                                        func() error
	checksum                                       [32]byte
	checksumReady                                  bool
}

func (g Graph) coord(id int64) (Coord, bool) {
	if g.store == nil {
		c, ok := g.coords[id]
		return c, ok
	}
	i := g.store.nodeIndex(id)
	if i < 0 {
		return Coord{}, false
	}
	return g.store.coordAt(i), true
}

// nodeRecord and coordAt are for scans that already know the record index.
// Keeping that index avoids looking up each node ID again in the sorted table.
func (d *diskGraph) nodeRecord(i int) []byte {
	return d.data[graphHeaderSize+i*d.nodeSize : graphHeaderSize+(i+1)*d.nodeSize]
}

func (d *diskGraph) coordAt(i int) Coord {
	b := d.nodeRecord(i)
	return Coord{Lat: math.Float64frombits(binary.LittleEndian.Uint64(b[8:])), Lon: math.Float64frombits(binary.LittleEndian.Uint64(b[16:]))}
}

func (d *diskGraph) nodeIndex(id int64) int {
	i := sort.Search(d.nodes, func(i int) bool {
		return int64(binary.LittleEndian.Uint64(d.data[graphHeaderSize+i*d.nodeSize:])) >= id
	})
	if i == d.nodes || int64(binary.LittleEndian.Uint64(d.data[graphHeaderSize+i*d.nodeSize:])) != id {
		return -1
	}
	return i
}

func (d *diskGraph) edgeRange(i int) (start, count int) {
	b := d.nodeRecord(i)
	if d.compact {
		if d.chainsOffset != 0 && binary.LittleEndian.Uint32(b[28:])&graphChainFlag != 0 {
			return 0, 2
		}
		return int(binary.LittleEndian.Uint32(b[24:])), int(binary.LittleEndian.Uint32(b[28:]))
	}
	return int(binary.LittleEndian.Uint64(b[24:])), int(binary.LittleEndian.Uint32(b[32:]))
}

type edgeIterator struct {
	memory   []Edge
	data     []byte
	store    *diskGraph
	index    int
	chain    []byte
	position uint32
	reversed bool
}

func (g Graph) edges(id int64) edgeIterator {
	if g.store == nil {
		return edgeIterator{memory: g.adj[id], index: -1}
	}
	d := g.store
	i := d.nodeIndex(id)
	if i < 0 {
		return edgeIterator{index: -1}
	}
	return d.edgesAt(i)
}

func (d *diskGraph) edgesAt(i int) edgeIterator {
	if chain, position, reversed := d.chainNode(i); chain != nil {
		return edgeIterator{store: d, chain: chain, position: position, reversed: reversed, index: -1}
	}
	start, count := d.edgeRange(i)
	return edgeIterator{data: d.data[d.edgesOffset+start*d.edgeSize : d.edgesOffset+(start+count)*d.edgeSize], store: d, index: -1}
}
func (i *edgeIterator) next() bool {
	i.index++
	if i.chain != nil {
		return i.index < 2
	}
	if i.data != nil {
		return i.index < len(i.data)/i.store.edgeSize
	}
	return i.index < len(i.memory)
}
func (i *edgeIterator) edge() Edge {
	if i.chain != nil {
		return i.store.chainEdge(i.chain, i.position, (i.index == 1) != i.reversed)
	}
	if i.data == nil {
		return i.memory[i.index]
	}
	d := i.store
	b := i.data[i.index*d.edgeSize:]
	if d.compact {
		return d.compactEdge(binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint64(b[8:]))
	}
	return Edge{To: int64(binary.LittleEndian.Uint64(b)), DistM: math.Float64frombits(binary.LittleEndian.Uint64(b[8:])), SpeedKph: math.Float64frombits(binary.LittleEndian.Uint64(b[16:])), MaxHeightM: math.Float64frombits(binary.LittleEndian.Uint64(b[24:])), MaxWeightT: math.Float64frombits(binary.LittleEndian.Uint64(b[32:])), HwyType: d.classes[binary.LittleEndian.Uint32(b[40:])]}
}

// Route relaxation needs only these encoded fields. Avoid expanding an Edge
// and its target OSM ID/shared attributes on every visit to a compact edge.
func (i *edgeIterator) compactFields() (target, attribute uint32, distance float64) {
	if i.chain != nil {
		start := binary.LittleEndian.Uint32(i.chain)
		position, segment := i.position-1, i.position-1
		if (i.index == 1) != i.reversed {
			position, segment = i.position+1, i.position
		}
		target = binary.LittleEndian.Uint32(i.store.data[i.store.verticesOffset+int(start+position)*graphChainVertexSize:])
		attribute = binary.LittleEndian.Uint32(i.chain[8:])
		distance = math.Float64frombits(binary.LittleEndian.Uint64(i.store.data[i.store.verticesOffset+int(start+segment)*graphChainVertexSize+4:]))
		return
	}
	b := i.data[i.index*i.store.edgeSize:]
	return binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint32(b[4:]), math.Float64frombits(binary.LittleEndian.Uint64(b[8:]))
}
func (g Graph) degree(id int64) int {
	if g.store == nil {
		return len(g.adj[id])
	}
	i := g.store.nodeIndex(id)
	if i < 0 {
		return 0
	}
	_, count := g.store.edgeRange(i)
	return count
}

func (g Graph) eachNode(visit func(int64, Coord)) {
	if g.store == nil {
		for id, c := range g.coords {
			visit(id, c)
		}
		return
	}
	for i := 0; i < g.store.nodes; i++ {
		b := g.store.nodeRecord(i)
		id := int64(binary.LittleEndian.Uint64(b))
		visit(id, g.store.coordAt(i))
	}
}

func (g Graph) eachRoutableNode(visit func(int64, Coord)) {
	if g.store == nil {
		for id, c := range g.coords {
			if len(g.adj[id]) > 0 {
				visit(id, c)
			}
		}
		return
	}
	for i := 0; i < g.store.nodes; i++ {
		b := g.store.nodeRecord(i)
		if _, count := g.store.edgeRange(i); count > 0 {
			visit(int64(binary.LittleEndian.Uint64(b)), g.store.coordAt(i))
		}
	}
}

// cellNodeIterator is a borrowed view of the spatial index, valid until Close.
// It reads IDs in place rather than allocating a slice for every visited cell.
// Compact cells also retain their node index for direct coordinate access.
type cellNodeIterator struct {
	data  []byte
	store *diskGraph
	key   int64
	id    int64
	node  int
}

func (d *diskGraph) cellNodesIter(key int64) cellNodeIterator {
	start := sort.Search(d.nodes, func(i int) bool {
		return int64(binary.LittleEndian.Uint64(d.data[d.cellsOffset+i*d.cellSize:])) >= key
	})
	return cellNodeIterator{data: d.data[d.cellsOffset+start*d.cellSize : d.cellsOffset+d.nodes*d.cellSize], store: d, key: key}
}

func (i *cellNodeIterator) next() bool {
	if len(i.data) == 0 || len(i.data) < i.store.cellSize || int64(binary.LittleEndian.Uint64(i.data)) != i.key {
		i.data = nil
		return false
	}
	if i.store.compact {
		i.node = int(binary.LittleEndian.Uint32(i.data[8:]))
		i.id = int64(binary.LittleEndian.Uint64(i.store.nodeRecord(i.node)))
	} else {
		i.id = int64(binary.LittleEndian.Uint64(i.data[8:]))
	}
	i.data = i.data[i.store.cellSize:]
	return true
}

func (i cellNodeIterator) nodeID() int64 {
	return i.id
}

func (i cellNodeIterator) coord() Coord {
	if i.store.compact {
		return i.store.coordAt(i.node)
	}
	// V1 cells store OSM IDs rather than indices into the node table.
	if node := i.store.nodeIndex(i.id); node >= 0 {
		return i.store.coordAt(node)
	}
	return Coord{}
}

func (d *diskGraph) export() (map[int64]Coord, map[int64][]Edge) {
	coords := make(map[int64]Coord, d.nodes)
	adj := make(map[int64][]Edge, d.nodes)
	// One edge allocation replaces repeated growth of every node's adjacency
	// slice. Full slice expressions keep append on one node from overwriting
	// a neighbouring node's edges in the shared backing array.
	edges := make([]Edge, d.edgeCount)
	logicalStart := 0
	for i := 0; i < d.nodes; i++ {
		b := d.nodeRecord(i)
		id := int64(binary.LittleEndian.Uint64(b))
		coords[id] = d.coordAt(i)
		start, count := d.edgeRange(i)
		if d.chainsOffset != 0 {
			start = logicalStart
			logicalStart += count
		}
		if count > 0 {
			nodeEdges := edges[start : start+count : start+count]
			it := d.edgesAt(i)
			for j := range nodeEdges {
				it.next()
				nodeEdges[j] = it.edge()
			}
			adj[id] = nodeEdges
		}
	}
	return coords, adj
}

// WriteGraph writes a portable graph snapshot, including street names. The
// caller publishes the containing region atomically once all files are ready.
func (r *Router) WriteGraph(ctx context.Context, path string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.g.store != nil {
		// A file-backed graph is immutable and already has the portable format.
		// Copy it in bounded chunks instead of sorting all IDs, decoding every
		// edge and rebuilding its spatial index merely to serialize it again.
		return writeGraphSnapshot(ctx, path, r.g.store.data)
	}
	ids := make([]int64, 0, r.NodeCount())
	r.g.eachNode(func(id int64, _ Coord) { ids = append(ids, id) })
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	edgeCount := r.EdgeCount()
	if len(ids) == 0 || uint64(len(ids)) > math.MaxUint32 || uint64(edgeCount) > math.MaxUint32 {
		return errors.New("graph exceeds compact format limits; split the region")
	}
	bounds, ok := r.Bounds()
	if !ok {
		// A graph can lie on one latitude/longitude or contain a single node;
		// CoordWindow.Valid deliberately rejects such windows for UI queries.
		c, _ := r.g.coord(ids[0])
		bounds = CoordWindow{MinLat: c.Lat, MaxLat: c.Lat, MinLon: c.Lon, MaxLon: c.Lon}
		for _, id := range ids[1:] {
			c, _ := r.g.coord(id)
			bounds.MinLat = math.Min(bounds.MinLat, c.Lat)
			bounds.MaxLat = math.Max(bounds.MaxLat, c.Lat)
			bounds.MinLon = math.Min(bounds.MinLon, c.Lon)
			bounds.MaxLon = math.Max(bounds.MaxLon, c.Lon)
		}
	}
	if !validGraphBounds(bounds) {
		return errors.New("invalid graph bounds")
	}
	classes := []string{}
	classIDs := map[string]uint32{}
	attributes := []graphEdgeAttributes{}
	attributeIDs := map[graphEdgeAttributes]uint32{}
	indexedEdges := 0
	for i, id := range ids {
		c, _ := r.g.coord(id)
		if !bounds.Contains(c) {
			return errors.New("graph coordinate outside bounds")
		}
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		for it := r.g.edges(id); it.next(); {
			indexedEdges++
			e := it.edge()
			if _, ok := r.g.coord(e.To); !ok {
				return fmt.Errorf("graph edge references missing node %d", e.To)
			}
			class, ok := classIDs[e.HwyType]
			if !ok {
				class = uint32(len(classes))
				classIDs[e.HwyType] = class
				classes = append(classes, e.HwyType)
			}
			a := edgeAttributes(e, class)
			if _, ok := attributeIDs[a]; !ok {
				attributeIDs[a] = uint32(len(attributes))
				attributes = append(attributes, a)
			}
		}
	}

	if indexedEdges != edgeCount {
		return errors.New("graph adjacency references missing source nodes")
	}
	packing, err := r.packGraphChains(ctx, ids, classIDs, attributeIDs)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(graphMetadata{classes, r.streets})
	if err != nil {
		return err
	}
	if len(meta) > 64<<20 {
		return errors.New("graph street metadata exceeds format limit; split the region")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	w := bufio.NewWriterSize(f, 1<<20)
	var header [graphHeaderSize]byte
	copy(header[:], graphMagic)
	put := binary.LittleEndian.PutUint64
	put(header[8:], uint64(len(ids)))
	put(header[16:], uint64(edgeCount))
	put(header[64:], uint64(len(attributes)))
	put(header[24:], uint64(len(meta)))
	if packing != nil {
		copy(header[:], graphChainMagic)
		put(header[72:], uint64(packing.storedEdges))
		put(header[80:], uint64(len(packing.chains)))
		put(header[88:], uint64(len(packing.vertices)))
	}
	for i, v := range []float64{bounds.MinLat, bounds.MaxLat, bounds.MinLon, bounds.MaxLon} {
		put(header[32+i*8:], math.Float64bits(v))
	}
	if _, err = w.Write(header[:]); err != nil {
		return err
	}
	var record [graphAttributeSize]byte
	var edgeStart uint32
	for n, id := range ids {
		if n%4096 == 0 {
			if err = ctx.Err(); err != nil {
				return err
			}
		}
		clear(record[:])
		c, _ := r.g.coord(id)
		put(record[:], uint64(id))
		put(record[8:], math.Float64bits(c.Lat))
		put(record[16:], math.Float64bits(c.Lon))
		binary.LittleEndian.PutUint32(record[24:], edgeStart)
		degree := r.g.degree(id)
		if packing != nil && packing.nodePosition[n] != 0 {
			binary.LittleEndian.PutUint32(record[24:], packing.nodeChain[n])
			binary.LittleEndian.PutUint32(record[28:], packing.nodePosition[n])
		} else {
			if uint64(degree) >= uint64(graphChainFlag) {
				return errors.New("graph node exceeds compact degree limit")
			}
			binary.LittleEndian.PutUint32(record[28:], uint32(degree))
			edgeStart += uint32(degree)
		}
		if _, err = w.Write(record[:graphNodeSize]); err != nil {
			return err
		}
	}
	for n, id := range ids {
		if n%4096 == 0 {
			if err = ctx.Err(); err != nil {
				return err
			}
		}
		if packing != nil && packing.nodePosition[n] != 0 {
			continue
		}
		for it := r.g.edges(id); it.next(); {
			e := it.edge()
			target := sort.Search(len(ids), func(i int) bool { return ids[i] >= e.To })
			binary.LittleEndian.PutUint32(record[:], uint32(target))
			binary.LittleEndian.PutUint32(record[4:], attributeIDs[edgeAttributes(e, classIDs[e.HwyType])])
			put(record[8:], math.Float64bits(e.DistM))
			if _, err = w.Write(record[:graphEdgeSize]); err != nil {
				return err
			}
		}
	}

	type cell struct {
		key  int64
		node uint32
	}
	cells := make([]cell, len(ids))
	idx := &spatialIndex{cellSize: 0.01, minLat: bounds.MinLat, minLon: bounds.MinLon}
	for i, id := range ids {
		c, _ := r.g.coord(id)
		x, y := idx.cell(c.Lat, c.Lon)
		cells[i] = cell{cellKey(x, y), uint32(i)}
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].key != cells[j].key {
			return cells[i].key < cells[j].key
		}
		return cells[i].node < cells[j].node
	})
	for _, c := range cells {
		put(record[:], uint64(c.key))
		binary.LittleEndian.PutUint32(record[8:], c.node)
		if _, err = w.Write(record[:graphCellSize]); err != nil {
			return err
		}
	}
	for _, a := range attributes {
		clear(record[:])
		put(record[:], a.speed)
		put(record[8:], a.height)
		put(record[16:], a.weight)
		binary.LittleEndian.PutUint32(record[24:], a.class)
		if _, err = w.Write(record[:]); err != nil {
			return err
		}
	}
	if packing != nil {
		for n, chain := range packing.chains {
			if n%4096 == 0 {
				if err = ctx.Err(); err != nil {
					return err
				}
			}
			clear(record[:])
			binary.LittleEndian.PutUint32(record[:], chain.start)
			binary.LittleEndian.PutUint32(record[4:], chain.count)
			binary.LittleEndian.PutUint32(record[8:], chain.attribute)
			if _, err = w.Write(record[:graphChainSize]); err != nil {
				return err
			}
		}
		for n, vertex := range packing.vertices {
			if n%4096 == 0 {
				if err = ctx.Err(); err != nil {
					return err
				}
			}
			binary.LittleEndian.PutUint32(record[:], vertex.node)
			put(record[4:], vertex.distance)
			if _, err = w.Write(record[:graphChainVertexSize]); err != nil {
				return err
			}
		}
	}

	if _, err = w.Write(meta); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = w.Flush(); err != nil {
		return err
	}
	return f.Sync()
}

func validGraphBounds(b CoordWindow) bool {
	return b.MinLat >= -90 && b.MaxLat <= 90 && b.MinLon >= -180 && b.MaxLon <= 180 && b.MinLat <= b.MaxLat && b.MinLon <= b.MaxLon
}

func edgeAttributes(e Edge, class uint32) graphEdgeAttributes {
	return graphEdgeAttributes{math.Float64bits(e.SpeedKph), math.Float64bits(e.MaxHeightM), math.Float64bits(e.MaxWeightT), class}
}

func writeGraphSnapshot(ctx context.Context, path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	const chunkSize = 1 << 20
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(len(data), chunkSize)
		written, err := f.Write(data[:n])
		if err != nil {
			return err
		}
		if written != n {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.Sync()
}

// OpenGraph opens a read-only, file-backed graph without rebuilding it from
// PBF or expanding coordinates and edges into maps. Close after all queries end.
func OpenGraph(path string) (*Router, error) {
	data, release, err := mapGraphFile(path)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*Router, error) { _ = release(); return nil, e }
	if len(data) < graphHeaderSize {
		return fail(errors.New("unsupported or truncated graph"))
	}
	chained := string(data[:8]) == graphChainMagic
	compact := string(data[:8]) == graphMagic || chained
	if !compact && string(data[:8]) != graphLegacyMagic {
		return fail(errors.New("unsupported or truncated graph"))
	}
	nodeSize, edgeSize, cellSize := graphLegacyNodeSize, graphLegacyEdgeSize, graphLegacyCellSize
	if compact {
		nodeSize, edgeSize, cellSize = graphNodeSize, graphEdgeSize, graphCellSize
	}
	n, e, m := binary.LittleEndian.Uint64(data[8:]), binary.LittleEndian.Uint64(data[16:]), binary.LittleEndian.Uint64(data[24:])
	attributes := uint64(0)
	storedEdges, chains, vertices := e, uint64(0), uint64(0)
	if compact {
		attributes = binary.LittleEndian.Uint64(data[64:])
	}
	if chained {
		storedEdges, chains, vertices = binary.LittleEndian.Uint64(data[72:]), binary.LittleEndian.Uint64(data[80:]), binary.LittleEndian.Uint64(data[88:])
	}
	// Validate sections against the remaining file size before advancing offsets;
	// multiplication and addition must remain safe for hostile header counts.
	size := uint64(len(data))
	if n == 0 || n > size/uint64(nodeSize) || storedEdges > size/uint64(edgeSize) || m > 64<<20 || attributes > size/graphAttributeSize || (compact && (n > math.MaxUint32 || e > math.MaxUint32 || attributes > math.MaxUint32)) || storedEdges > e || chains > n || vertices > size/graphChainVertexSize || vertices > math.MaxUint32 || (chained && (chains == 0 || e-storedEdges > 2*n)) {
		return fail(errors.New("invalid graph sizes"))
	}
	offset := uint64(graphHeaderSize)
	take := func(count uint64, recordSize int) (uint64, bool) {
		start := offset
		if count > (size-offset)/uint64(recordSize) {
			return 0, false
		}
		offset += count * uint64(recordSize)
		return start, true
	}
	if _, ok := take(n, nodeSize); !ok {
		return fail(io.ErrUnexpectedEOF)
	}
	edgesOffset, ok := take(storedEdges, edgeSize)
	if !ok {
		return fail(io.ErrUnexpectedEOF)
	}
	cellsOffset, ok := take(n, cellSize)
	if !ok {
		return fail(io.ErrUnexpectedEOF)
	}
	attributesOffset, ok := take(attributes, graphAttributeSize)
	if !ok {
		return fail(io.ErrUnexpectedEOF)
	}
	chainsOffset, verticesOffset := uint64(0), uint64(0)
	if chained {
		if chainsOffset, ok = take(chains, graphChainSize); !ok {
			return fail(io.ErrUnexpectedEOF)
		}
		if verticesOffset, ok = take(vertices, graphChainVertexSize); !ok {
			return fail(io.ErrUnexpectedEOF)
		}
	}
	metaOffset := offset
	if m != size-metaOffset {
		return fail(io.ErrUnexpectedEOF)
	}
	var meta graphMetadata
	if err = json.Unmarshal(data[metaOffset:], &meta); err != nil {
		return fail(err)
	}
	if len(meta.Classes) == 0 && e > 0 {
		return fail(errors.New("missing graph classes"))
	}
	graphBounds := CoordWindow{MinLat: math.Float64frombits(binary.LittleEndian.Uint64(data[32:])), MaxLat: math.Float64frombits(binary.LittleEndian.Uint64(data[40:])), MinLon: math.Float64frombits(binary.LittleEndian.Uint64(data[48:])), MaxLon: math.Float64frombits(binary.LittleEndian.Uint64(data[56:]))}
	if !validGraphBounds(graphBounds) {
		return fail(errors.New("invalid graph bounds"))
	}
	spatial := &spatialIndex{cellSize: 0.01, minLat: graphBounds.MinLat, minLon: graphBounds.MinLon}
	d := &diskGraph{data: data, nodes: int(n), edgeCount: int(e), storedEdges: int(storedEdges), edgesOffset: int(edgesOffset), cellsOffset: int(cellsOffset), nodeSize: nodeSize, edgeSize: edgeSize, cellSize: cellSize, attributesOffset: int(attributesOffset), chainsOffset: int(chainsOffset), verticesOffset: int(verticesOffset), compact: compact, classes: meta.Classes, release: release}
	var last int64
	var nextEdge uint64
	var chainNodes uint64
	for i := 0; i < d.nodes; i++ {
		b := d.nodeRecord(i)
		id := int64(binary.LittleEndian.Uint64(b))
		if compact && !graphBounds.Contains(d.coordAt(i)) {
			return fail(errors.New("graph coordinate outside bounds"))
		}
		var start, count uint64
		if compact {
			start, count = uint64(binary.LittleEndian.Uint32(b[24:])), uint64(binary.LittleEndian.Uint32(b[28:]))
		} else {
			start, count = binary.LittleEndian.Uint64(b[24:]), uint64(binary.LittleEndian.Uint32(b[32:]))
		}
		if i > 0 && id <= last {
			return fail(errors.New("invalid graph node index"))
		}
		last = id
		if chained && uint32(count)&graphChainFlag != 0 {
			if start >= chains {
				return fail(errors.New("invalid graph chain index"))
			}
			chainNodes++
			continue
		}
		if start > storedEdges || count > storedEdges-start || (compact && start != nextEdge) {
			return fail(errors.New("invalid graph node index"))
		}
		nextEdge = start + count
	}
	if compact && (nextEdge != storedEdges || storedEdges+2*chainNodes != e) {
		return fail(errors.New("invalid graph adjacency ranges"))
	}
	for i := 0; i < d.storedEdges; i++ {
		b := data[d.edgesOffset+i*d.edgeSize:]
		if compact {
			if uint64(binary.LittleEndian.Uint32(b)) >= n || uint64(binary.LittleEndian.Uint32(b[4:])) >= attributes {
				return fail(errors.New("invalid graph edge index"))
			}
		} else if uint64(binary.LittleEndian.Uint32(b[40:])) >= uint64(len(d.classes)) {
			return fail(errors.New("invalid graph edge class"))
		}
	}
	if chained {
		if err := d.validateChains(chains, vertices, attributes, chainNodes); err != nil {
			return fail(err)
		}
	}
	if compact {
		for i := uint64(0); i < attributes; i++ {
			if uint64(binary.LittleEndian.Uint32(data[attributesOffset+i*graphAttributeSize+24:])) >= uint64(len(d.classes)) {
				return fail(errors.New("invalid graph attribute class"))
			}
		}
		var lastKey int64
		var lastNode uint32
		for i := 0; i < d.nodes; i++ {
			b := data[d.cellsOffset+i*d.cellSize:]
			key, node := int64(binary.LittleEndian.Uint64(b)), binary.LittleEndian.Uint32(b[8:])
			if uint64(node) >= n || (i > 0 && (key < lastKey || (key == lastKey && node <= lastNode))) {
				return fail(errors.New("invalid graph spatial index"))
			}
			c := d.coordAt(int(node))
			x, y := spatial.cell(c.Lat, c.Lon)
			if key != cellKey(x, y) {
				return fail(errors.New("graph spatial cell does not match coordinate"))
			}
			lastKey, lastNode = key, node
		}
	}

	idx := &spatialIndex{cellSize: 0.01, minLat: graphBounds.MinLat, minLon: graphBounds.MinLon, maxX: int32(math.Floor((graphBounds.MaxLat - graphBounds.MinLat) / 0.01)), maxY: int32(math.Floor((graphBounds.MaxLon - graphBounds.MinLon) / 0.01))}
	return &Router{g: Graph{store: d}, streets: meta.Streets, streetNameByNode: buildStreetNameIndex(meta.Streets), idx: idx, bounds: &graphBounds}, nil
}

// Close releases a file-backed graph. Map-built routers need no cleanup.
func (r *Router) Close() error {
	var landmarkErr error
	if r.landmarks != nil {
		landmarkErr = r.landmarks.release()
		r.landmarks = nil
	}
	if r.g.store != nil && r.g.store.release != nil {
		err := r.g.store.release()
		r.g.store.release = nil
		return errors.Join(landmarkErr, err)
	}
	return landmarkErr
}
