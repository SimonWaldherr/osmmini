package osmmini

// V3 stores long, bidirectional degree-two chains once. Every original node,
// coordinate, directed edge, adjacency order and float bit remains observable;
// this is a storage representation, not geometric simplification.
//
// Header offsets 16/72/80/88 hold logical edges, explicit edges, chains and
// vertices. Sections are nodes, explicit edges, spatial cells, attributes,
// chains, vertices and JSON metadata. Ordinary nodes retain V2 adjacency
// ranges. Chain nodes use offset 24 for their chain index and offset 28 for
// ChainFlag | [ChainReverse] | position (30 bits). A 16-byte chain stores
// vertex start/count, attribute index and a zero reserved word. A 12-byte
// vertex stores node index plus float64 distance to the next vertex; the final
// vertex has zero distance. Endpoints remain ordinary and retain entry edges.
import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

const graphChainMagic = "OSMMGR03"
const graphChainSize = 16
const graphChainVertexSize = 12
const graphChainFlag uint32 = 1 << 31
const graphChainReverse uint32 = 1 << 30
const graphChainPositionMask = graphChainReverse - 1

type graphChain struct {
	start, count, attribute uint32
}

type graphChainVertex struct {
	node     uint32
	distance uint64 // exact distance to the next vertex; zero on the last vertex
}

type graphChainPacking struct {
	chains       []graphChain
	vertices     []graphChainVertex
	nodeChain    []uint32
	nodePosition []uint32 // zero for ordinary nodes; position plus flags otherwise
	storedEdges  int
}

func (r *Router) packGraphChains(ctx context.Context, ids []int64, classes map[string]uint32, attributes map[graphEdgeAttributes]uint32) (*graphChainPacking, error) {
	index := func(id int64) int { return sort.Search(len(ids), func(i int) bool { return ids[i] >= id }) }
	incoming := make([]uint32, len(ids))
	for n, id := range ids {
		if n%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for it := r.g.edges(id); it.next(); {
			incoming[index(it.edge().To)]++
		}
	}
	candidates := make([]bool, len(ids))
	for n, id := range ids {
		if n%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if incoming[n] != 2 || r.g.degree(id) != 2 {
			continue
		}
		it := r.g.edges(id)
		it.next()
		a := it.edge()
		it.next()
		b := it.edge()
		if a.To == id || b.To == id || a.To == b.To {
			continue
		}
		attr := edgeAttributes(a, classes[a.HwyType])
		if attr != edgeAttributes(b, classes[b.HwyType]) {
			continue
		}
		valid := true
		for _, e := range [2]Edge{a, b} {
			// Keep the node next to an unusually large junction ordinary. This
			// bounds reciprocal checks even on a star with millions of spokes.
			if r.g.degree(e.To) > 64 {
				valid = false
				break
			}
			matches := 0
			for back := r.g.edges(e.To); back.next(); {
				reverse := back.edge()
				if reverse.To != id {
					continue
				}
				matches++
				if math.Float64bits(reverse.DistM) != math.Float64bits(e.DistM) || edgeAttributes(reverse, classes[reverse.HwyType]) != attr {
					valid = false
				}
			}
			if matches != 1 {
				valid = false
			}
		}
		candidates[n] = valid
	}
	clear(incoming)
	p := &graphChainPacking{nodeChain: incoming, nodePosition: make([]uint32, len(ids)), storedEdges: r.EdgeCount()}
	// Each candidate component is a path or a ring. Keep one ordinary anchor
	// for rings; otherwise walk backwards to the ordinary endpoint first.
	other := func(node, previous int64) Edge {
		it := r.g.edges(node)
		it.next()
		e := it.edge()
		if e.To == previous {
			it.next()
			e = it.edge()
		}
		return e
	}
	var path []graphChainVertex
	for n, id := range ids {
		if !candidates[n] || p.nodePosition[n] != 0 {
			continue
		}
		previous, current := id, other(id, id).To
		for steps := 0; candidates[index(current)] && current != id; steps++ {
			if steps%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			next := other(current, previous).To
			previous, current = current, next
		}
		if current == id {
			candidates[n] = false
		}
		// current is the ordinary endpoint; previous is its adjacent candidate.
		path = path[:0]
		start := current
		var attribute uint32
		for steps := 0; ; steps++ {
			if steps%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			var edge Edge
			if len(path) == 0 {
				for it := r.g.edges(current); it.next(); {
					if e := it.edge(); e.To == previous {
						edge = e
						break
					}
				}
				attribute = attributes[edgeAttributes(edge, classes[edge.HwyType])]
			} else {
				edge = other(current, previous)
			}
			path = append(path, graphChainVertex{uint32(index(current)), math.Float64bits(edge.DistM)})
			previous, current = current, edge.To
			if !candidates[index(current)] || current == start {
				path = append(path, graphChainVertex{node: uint32(index(current))})
				break
			}
		}
		// A chain needs three interior nodes to save bytes: 32*k old bytes
		// versus 12*(k+2)+16 new bytes. Short chains stay ordinary.
		if len(path) < 5 {
			for _, v := range path[1 : len(path)-1] {
				candidates[v.node] = false
			}
			continue
		}
		if len(path) > int(graphChainPositionMask) || uint64(len(p.vertices))+uint64(len(path)) > math.MaxUint32 {
			return nil, errors.New("graph chains exceed compact format limits; split the region")
		}
		chain := uint32(len(p.chains))
		p.chains = append(p.chains, graphChain{uint32(len(p.vertices)), uint32(len(path)), attribute})
		p.vertices = append(p.vertices, path...)
		for position := 1; position < len(path)-1; position++ {
			v := path[position]
			it := r.g.edges(ids[v.node])
			it.next()
			flags := graphChainFlag | uint32(position)
			if it.edge().To != ids[path[position-1].node] {
				flags |= graphChainReverse
			}
			p.nodeChain[v.node], p.nodePosition[v.node] = chain, flags
			p.storedEdges -= 2
		}
	}
	if len(p.chains) == 0 {
		return nil, nil
	}
	return p, nil
}

func (d *diskGraph) chainNode(i int) (chain []byte, position uint32, reversed bool) {
	if d.chainsOffset == 0 {
		return nil, 0, false
	}
	b := d.nodeRecord(i)
	flags := binary.LittleEndian.Uint32(b[28:])
	if flags&graphChainFlag == 0 {
		return nil, 0, false
	}
	chainIndex := binary.LittleEndian.Uint32(b[24:])
	return d.data[d.chainsOffset+int(chainIndex)*graphChainSize:], flags & graphChainPositionMask, flags&graphChainReverse != 0
}

func (d *diskGraph) chainEdge(chain []byte, position uint32, next bool) Edge {
	start := binary.LittleEndian.Uint32(chain)
	target, segment := position-1, position-1
	if next {
		target, segment = position+1, position
	}
	vertex := d.data[d.verticesOffset+int(start+target)*graphChainVertexSize:]
	distance := d.data[d.verticesOffset+int(start+segment)*graphChainVertexSize+4:]
	return d.compactEdge(binary.LittleEndian.Uint32(vertex), binary.LittleEndian.Uint32(chain[8:]), binary.LittleEndian.Uint64(distance))
}

func (d *diskGraph) compactEdge(target, attribute uint32, distance uint64) Edge {
	a := d.data[d.attributesOffset+int(attribute)*graphAttributeSize:]
	return Edge{To: int64(binary.LittleEndian.Uint64(d.nodeRecord(int(target)))), DistM: math.Float64frombits(distance), SpeedKph: math.Float64frombits(binary.LittleEndian.Uint64(a)), MaxHeightM: math.Float64frombits(binary.LittleEndian.Uint64(a[8:])), MaxWeightT: math.Float64frombits(binary.LittleEndian.Uint64(a[16:])), HwyType: d.classes[binary.LittleEndian.Uint32(a[24:])]}
}

func (d *diskGraph) validateChains(chains, vertices, attributes, chainNodes uint64) error {
	var nextVertex, interior uint64
	for i := uint64(0); i < chains; i++ {
		b := d.data[d.chainsOffset+int(i)*graphChainSize:]
		start, count := uint64(binary.LittleEndian.Uint32(b)), uint64(binary.LittleEndian.Uint32(b[4:]))
		attribute := binary.LittleEndian.Uint32(b[8:])
		if start != nextVertex || count < 5 || start > vertices || count > vertices-start || uint64(attribute) >= attributes || binary.LittleEndian.Uint32(b[12:]) != 0 {
			return errors.New("invalid graph chain record")
		}
		for position := uint64(0); position < count; position++ {
			v := d.data[d.verticesOffset+int(start+position)*graphChainVertexSize:]
			node := binary.LittleEndian.Uint32(v)
			if uint64(node) >= uint64(d.nodes) {
				return errors.New("invalid graph chain vertex")
			}
			n := d.nodeRecord(int(node))
			flags := binary.LittleEndian.Uint32(n[28:])
			if position > 0 && position < count-1 {
				if flags&graphChainFlag == 0 || uint64(flags&graphChainPositionMask) != position || uint64(binary.LittleEndian.Uint32(n[24:])) != i {
					return errors.New("invalid graph chain node position")
				}
			} else if flags&graphChainFlag != 0 {
				return errors.New("invalid graph chain endpoint")
			}
		}
		last := d.data[d.verticesOffset+int(start+count-1)*graphChainVertexSize:]
		if binary.LittleEndian.Uint64(last[4:]) != 0 {
			return errors.New("invalid graph chain terminal distance")
		}
		nextVertex += count
		interior += count - 2
	}
	if nextVertex != vertices || interior != chainNodes {
		return errors.New("invalid graph chain coverage")
	}
	// Check each explicit edge once, rather than rescanning a junction for
	// every attached chain. Require exactly one entry at either chain end.
	entries := make([][2]uint8, int(chains))
	for node := 0; node < d.nodes; node++ {
		if chain, _, _ := d.chainNode(node); chain != nil {
			continue
		}
		start, count := d.edgeRange(node)
		for edge := start; edge < start+count; edge++ {
			e := d.data[d.edgesOffset+edge*graphEdgeSize:]
			target := int(binary.LittleEndian.Uint32(e))
			chain, position, _ := d.chainNode(target)
			if chain == nil {
				continue
			}
			chainStart, chainCount := binary.LittleEndian.Uint32(chain), binary.LittleEndian.Uint32(chain[4:])
			end, segment, side := chainStart, chainStart, 0
			if position == chainCount-2 {
				end, segment, side = chainStart+chainCount-1, chainStart+chainCount-2, 1
			} else if position != 1 {
				return errors.New("graph edge enters chain interior")
			}
			v := d.data[d.verticesOffset+int(end)*graphChainVertexSize:]
			distance := d.data[d.verticesOffset+int(segment)*graphChainVertexSize+4:]
			if binary.LittleEndian.Uint32(v) != uint32(node) || binary.LittleEndian.Uint32(e[4:]) != binary.LittleEndian.Uint32(chain[8:]) || binary.LittleEndian.Uint64(e[8:]) != binary.LittleEndian.Uint64(distance) {
				return errors.New("invalid graph chain endpoint edge")
			}
			chainIndex := binary.LittleEndian.Uint32(d.nodeRecord(target)[24:])
			if entries[chainIndex][side] != 0 {
				return errors.New("duplicate graph chain entry")
			}
			entries[chainIndex][side]++
		}
	}
	for _, ends := range entries {
		if ends != [2]uint8{1, 1} {
			return errors.New("missing graph chain entry")
		}
	}
	return nil
}
