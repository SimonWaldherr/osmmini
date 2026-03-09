package osmmini

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type blockCtx struct {
	st          []string
	granularity int64
	latOffset   int64
	lonOffset   int64
}

func ExtractFile(path string, opts Options, cb Callbacks) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return Extract(f, opts, cb)
}

func Extract(r io.Reader, opts Options, cb Callbacks) error {
	wantHighway := cb.HighwayWay != nil

	// IMPORTANT:
	// Nodes must still be processed if cb.Node is set, even if no address callback is used.
	// Otherwise Router building (which relies on cb.Node) silently builds an empty graph.
	wantNode := cb.Node != nil || cb.AddressNode != nil

	wantAddrWay := cb.AddressWay != nil
	wantAddrRel := cb.AddressRelation != nil

	br := bufio.NewReaderSize(r, 1<<20)

	for {
		typ, raw, err := readNextBlock(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		switch typ {
		case "OSMHeader":
			// HeaderBlock is not needed for our extraction/filtering.
			continue
		case "OSMData":
			if err := processOSMData(raw, wantHighway, wantNode, wantAddrWay, wantAddrRel, opts, cb); err != nil {
				return err
			}
		default:
			// unknown block type -> ignore
			continue
		}
	}
}

// ---- Top-Level PBF Block I/O ----

func readNextBlock(r *bufio.Reader) (typ string, raw []byte, err error) {
	var lenBuf [4]byte
	_, err = io.ReadFull(r, lenBuf[:])
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil, io.EOF
		}
		return "", nil, err
	}

	hdrLen := binary.BigEndian.Uint32(lenBuf[:])
	if hdrLen == 0 || hdrLen > 64*1024 {
		return "", nil, fmt.Errorf("invalid BlobHeader size: %d", hdrLen)
	}

	hdrBytes := make([]byte, hdrLen)
	if _, err := io.ReadFull(r, hdrBytes); err != nil {
		return "", nil, err
	}

	typ, dataSize, err := parseBlobHeader(hdrBytes)
	if err != nil {
		return "", nil, err
	}
	if dataSize < 0 {
		return "", nil, fmt.Errorf("negative datasize: %d", dataSize)
	}
	if dataSize == 0 {
		return "", nil, fmt.Errorf("zero datasize")
	}

	blobBytes := make([]byte, dataSize)
	if _, err := io.ReadFull(r, blobBytes); err != nil {
		return "", nil, err
	}

	raw, err = parseAndDecompressBlob(blobBytes)
	if err != nil {
		return "", nil, err
	}
	return typ, raw, nil
}

func parseBlobHeader(b []byte) (typ string, datasize int, err error) {
	// message BlobHeader {
	//   required string type = 1;
	//   optional bytes indexdata = 2;
	//   required int32 datasize = 3;
	// }
	var gotType bool
	var gotSize bool

	i := 0
	for i < len(b) {
		tag, err := readUvarint(b, &i)
		if err != nil {
			return "", 0, err
		}
		field := int(tag >> 3)
		wire := int(tag & 7)

		switch field {
		case 1: // type (string)
			if wire != 2 {
				return "", 0, fmt.Errorf("BlobHeader.type: wrong wire=%d", wire)
			}
			s, err := readString(b, &i)
			if err != nil {
				return "", 0, err
			}
			typ = s
			gotType = true

		case 3: // datasize (int32)
			if wire != 0 {
				return "", 0, fmt.Errorf("BlobHeader.datasize: wrong wire=%d", wire)
			}
			v, err := readUvarint(b, &i)
			if err != nil {
				return "", 0, err
			}
			if v > uint64(^uint32(0)) {
				return "", 0, fmt.Errorf("BlobHeader.datasize too large: %d", v)
			}
			datasize = int(int32(v))
			gotSize = true

		default:
			if err := skipField(wire, b, &i); err != nil {
				return "", 0, err
			}
		}
	}

	if !gotType || typ == "" {
		return "", 0, fmt.Errorf("BlobHeader missing type")
	}
	if !gotSize {
		return "", 0, fmt.Errorf("BlobHeader missing datasize")
	}
	return typ, datasize, nil
}

func parseAndDecompressBlob(b []byte) ([]byte, error) {
	// message Blob {
	//   optional bytes raw = 1;
	//   optional int32 raw_size = 2;
	//   optional bytes zlib_data = 3;
	//   optional bytes lzma_data = 4;
	//   optional bytes OBSOLETE_bzip2_data = 5 [deprecated=true];
	// }
	var raw []byte
	var zlibData []byte
	var lzmaData []byte
	var bzip2Data []byte
	var rawSize int

	i := 0
	for i < len(b) {
		tag, err := readUvarint(b, &i)
		if err != nil {
			return nil, err
		}
		field := int(tag >> 3)
		wire := int(tag & 7)

		switch field {
		case 1: // raw
			if wire != 2 {
				return nil, fmt.Errorf("Blob.raw: wrong wire=%d", wire)
			}
			v, err := readBytes(b, &i)
			if err != nil {
				return nil, err
			}
			raw = v

		case 2: // raw_size
			if wire != 0 {
				return nil, fmt.Errorf("Blob.raw_size: wrong wire=%d", wire)
			}
			v, err := readUvarint(b, &i)
			if err != nil {
				return nil, err
			}
			rawSize = int(int32(v))

		case 3: // zlib_data
			if wire != 2 {
				return nil, fmt.Errorf("Blob.zlib_data: wrong wire=%d", wire)
			}
			v, err := readBytes(b, &i)
			if err != nil {
				return nil, err
			}
			zlibData = v

		case 4: // lzma_data (unsupported)
			if wire != 2 {
				return nil, fmt.Errorf("Blob.lzma_data: wrong wire=%d", wire)
			}
			v, err := readBytes(b, &i)
			if err != nil {
				return nil, err
			}
			lzmaData = v

		case 5: // obsolete bzip2 (unsupported)
			if wire != 2 {
				return nil, fmt.Errorf("Blob.OBSOLETE_bzip2_data: wrong wire=%d", wire)
			}
			v, err := readBytes(b, &i)
			if err != nil {
				return nil, err
			}
			bzip2Data = v

		default:
			if err := skipField(wire, b, &i); err != nil {
				return nil, err
			}
		}
	}

	if raw != nil {
		return raw, nil
	}

	if zlibData != nil {
		zr, err := zlib.NewReader(bytes.NewReader(zlibData))
		if err != nil {
			return nil, err
		}
		defer zr.Close()

		out, err := io.ReadAll(zr)
		if err != nil {
			return nil, err
		}

		if rawSize > 0 && len(out) != rawSize {
			return nil, fmt.Errorf("zlib blob size mismatch: got=%d want=%d", len(out), rawSize)
		}
		return out, nil
	}

	if lzmaData != nil {
		return nil, fmt.Errorf("unsupported blob compression: lzma_data present")
	}
	if bzip2Data != nil {
		return nil, fmt.Errorf("unsupported blob compression: OBSOLETE_bzip2_data present")
	}

	return nil, fmt.Errorf("unsupported blob: neither raw nor zlib_data present")
}

// ---- OSMData: PrimitiveBlock / PrimitiveGroup ----

func processOSMData(raw []byte, wantHighway, wantNode, wantAddrWay, wantAddrRel bool, opts Options, cb Callbacks) error {
	ctx := blockCtx{
		granularity: 100,
		latOffset:   0,
		lonOffset:   0,
	}

	var groups [][]byte

	i := 0
	for i < len(raw) {
		tag, err := readUvarint(raw, &i)
		if err != nil {
			return err
		}
		field := int(tag >> 3)
		wire := int(tag & 7)

		switch field {
		case 1: // stringtable
			if wire != 2 {
				return fmt.Errorf("PrimitiveBlock.stringtable: wrong wire=%d", wire)
			}
			msg, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			st, err := parseStringTable(msg)
			if err != nil {
				return err
			}
			ctx.st = st

		case 2: // primitivegroup (repeated)
			if wire != 2 {
				return fmt.Errorf("PrimitiveBlock.primitivegroup: wrong wire=%d", wire)
			}
			msg, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			groups = append(groups, msg)

		case 17: // granularity (int32)
			if wire != 0 {
				return fmt.Errorf("PrimitiveBlock.granularity: wrong wire=%d", wire)
			}
			v, err := readUvarint(raw, &i)
			if err != nil {
				return err
			}
			ctx.granularity = int64(int32(v))

		case 19: // lat_offset (int64)
			if wire != 0 {
				return fmt.Errorf("PrimitiveBlock.lat_offset: wrong wire=%d", wire)
			}
			v, err := readUvarint(raw, &i)
			if err != nil {
				return err
			}
			ctx.latOffset = int64(v)

		case 20: // lon_offset (int64)
			if wire != 0 {
				return fmt.Errorf("PrimitiveBlock.lon_offset: wrong wire=%d", wire)
			}
			v, err := readUvarint(raw, &i)
			if err != nil {
				return err
			}
			ctx.lonOffset = int64(v)

		default:
			if err := skipField(wire, raw, &i); err != nil {
				return err
			}
		}
	}

	if ctx.st == nil || len(ctx.st) == 0 {
		ctx.st = []string{""}
	} else {
		// Index 0 is reserved as delimiter.
		ctx.st[0] = ""
	}

	for _, g := range groups {
		if err := processPrimitiveGroup(g, ctx, wantHighway, wantNode, wantAddrWay, wantAddrRel, opts, cb); err != nil {
			return err
		}
	}
	return nil
}

func parseStringTable(raw []byte) ([]string, error) {
	// message StringTable { repeated bytes s = 1; }
	st := make([]string, 0, 256)

	i := 0
	for i < len(raw) {
		tag, err := readUvarint(raw, &i)
		if err != nil {
			return nil, err
		}
		field := int(tag >> 3)
		wire := int(tag & 7)

		switch field {
		case 1:
			if wire != 2 {
				return nil, fmt.Errorf("StringTable.s: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return nil, err
			}
			st = append(st, string(b))

		default:
			if err := skipField(wire, raw, &i); err != nil {
				return nil, err
			}
		}
	}

	if len(st) == 0 {
		return []string{""}, nil
	}
	st[0] = ""
	return st, nil
}

func processPrimitiveGroup(raw []byte, ctx blockCtx, wantHighway, wantNode, wantAddrWay, wantAddrRel bool, opts Options, cb Callbacks) error {
	i := 0
	for i < len(raw) {
		tag, err := readUvarint(raw, &i)
		if err != nil {
			return err
		}
		field := int(tag >> 3)
		wire := int(tag & 7)

		switch field {
		case 1: // nodes
			if wire != 2 {
				return fmt.Errorf("PrimitiveGroup.nodes: wrong wire=%d", wire)
			}
			msg, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			if wantNode {
				if err := processNode(msg, ctx, opts, cb); err != nil {
					return err
				}
			}

		case 2: // dense
			if wire != 2 {
				return fmt.Errorf("PrimitiveGroup.dense: wrong wire=%d", wire)
			}
			msg, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			if wantNode {
				if err := processDenseNodes(msg, ctx, opts, cb); err != nil {
					return err
				}
			}

		case 3: // ways
			if wire != 2 {
				return fmt.Errorf("PrimitiveGroup.ways: wrong wire=%d", wire)
			}
			msg, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			if wantHighway || wantAddrWay {
				if err := processWay(msg, ctx, wantHighway, wantAddrWay, opts, cb); err != nil {
					return err
				}
			}

		case 4: // relations
			if wire != 2 {
				return fmt.Errorf("PrimitiveGroup.relations: wrong wire=%d", wire)
			}
			msg, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			if wantAddrRel {
				if err := processRelation(msg, ctx, opts, cb); err != nil {
					return err
				}
			}

		default:
			if err := skipField(wire, raw, &i); err != nil {
				return err
			}
		}
	}
	return nil
}

func processNode(raw []byte, ctx blockCtx, opts Options, cb Callbacks) error {
	// message Node {
	//   required sint64 id = 1;
	//   repeated uint32 keys = 2 [packed = true];
	//   repeated uint32 vals = 3 [packed = true];
	//   required sint64 lat = 8;
	//   required sint64 lon = 9;
	// }
	var id int64
	var lat, lon int64
	var keysSegs [][]byte
	var valsSegs [][]byte

	i := 0
	for i < len(raw) {
		tag, err := readUvarint(raw, &i)
		if err != nil {
			return err
		}
		field := int(tag >> 3)
		wire := int(tag & 7)

		switch field {
		case 1: // id (sint64)
			if wire != 0 {
				return fmt.Errorf("Node.id: wrong wire=%d", wire)
			}
			v, err := readUvarint(raw, &i)
			if err != nil {
				return err
			}
			id = zigzagDecode64(v)

		case 2: // keys (packed)
			if wire != 2 {
				return fmt.Errorf("Node.keys: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			keysSegs = append(keysSegs, b)

		case 3: // vals (packed)
			if wire != 2 {
				return fmt.Errorf("Node.vals: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			valsSegs = append(valsSegs, b)

		case 8: // lat (sint64)
			if wire != 0 {
				return fmt.Errorf("Node.lat: wrong wire=%d", wire)
			}
			v, err := readUvarint(raw, &i)
			if err != nil {
				return err
			}
			lat = zigzagDecode64(v)

		case 9: // lon (sint64)
			if wire != 0 {
				return fmt.Errorf("Node.lon: wrong wire=%d", wire)
			}
			v, err := readUvarint(raw, &i)
			if err != nil {
				return err
			}
			lon = zigzagDecode64(v)

		default:
			if err := skipField(wire, raw, &i); err != nil {
				return err
			}
		}
	}

	latDeg, lonDeg := decodeCoord(lat, lon, ctx)

	// Node callback is independent of AddressNode.
	if cb.Node != nil {
		if err := cb.Node(id, latDeg, lonDeg); err != nil {
			return err
		}
	}

	if cb.AddressNode == nil {
		return nil
	}

	isAddr, err := scanKeys(keysSegs, ctx.st, false, true)
	if err != nil {
		return err
	}
	if !isAddr {
		return nil
	}

	tags, err := buildTags(keysSegs, valsSegs, ctx.st, opts.KeepTag)
	if err != nil {
		return err
	}

	return cb.AddressNode(Node{
		ID:   id,
		Lat:  latDeg,
		Lon:  lonDeg,
		Tags: tags,
	})
}

func processDenseNodes(raw []byte, ctx blockCtx, opts Options, cb Callbacks) error {
	// message DenseNodes {
	//   repeated sint64 id = 1 [packed=true]; // DELTA
	//   repeated sint64 lat = 8 [packed=true]; // DELTA
	//   repeated sint64 lon = 9 [packed=true]; // DELTA
	//   repeated int32 keys_vals = 10 [packed=true];
	// }
	var idDeltas []int64
	var latDeltas []int64
	var lonDeltas []int64
	var keysVals []int32

	i := 0
	for i < len(raw) {
		tag, err := readUvarint(raw, &i)
		if err != nil {
			return err
		}
		field := int(tag >> 3)
		wire := int(tag & 7)

		switch field {
		case 1: // id deltas
			if wire != 2 {
				return fmt.Errorf("DenseNodes.id: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			vals, err := decodePackedSint64(b)
			if err != nil {
				return err
			}
			idDeltas = append(idDeltas, vals...)

		case 8: // lat deltas
			if wire != 2 {
				return fmt.Errorf("DenseNodes.lat: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			vals, err := decodePackedSint64(b)
			if err != nil {
				return err
			}
			latDeltas = append(latDeltas, vals...)

		case 9: // lon deltas
			if wire != 2 {
				return fmt.Errorf("DenseNodes.lon: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			vals, err := decodePackedSint64(b)
			if err != nil {
				return err
			}
			lonDeltas = append(lonDeltas, vals...)

		case 10: // keys_vals
			if wire != 2 {
				return fmt.Errorf("DenseNodes.keys_vals: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			vals, err := decodePackedInt32(b)
			if err != nil {
				return err
			}
			keysVals = append(keysVals, vals...)

		default:
			if err := skipField(wire, raw, &i); err != nil {
				return err
			}
		}
	}

	n := len(idDeltas)
	if n == 0 {
		return nil
	}
	if len(latDeltas) != n || len(lonDeltas) != n {
		return fmt.Errorf("DenseNodes length mismatch: ids=%d lat=%d lon=%d", n, len(latDeltas), len(lonDeltas))
	}

	kvIdx := 0
	var idAcc, latAcc, lonAcc int64

	for idx := 0; idx < n; idx++ {
		idAcc += idDeltas[idx]
		latAcc += latDeltas[idx]
		lonAcc += lonDeltas[idx]

		latDeg, lonDeg := decodeCoord(latAcc, lonAcc, ctx)

		// Node callback is independent of AddressNode.
		if cb.Node != nil {
			if err := cb.Node(idAcc, latDeg, lonDeg); err != nil {
				return err
			}
		}

		if cb.AddressNode == nil {
			continue
		}
		if kvIdx >= len(keysVals) {
			continue
		}

		if keysVals[kvIdx] == 0 {
			kvIdx++
			continue
		}

		pairs := make([][2]int32, 0, 8)
		isAddr := false

		for {
			if kvIdx >= len(keysVals) {
				return fmt.Errorf("DenseNodes.keys_vals: unexpected end")
			}
			k := keysVals[kvIdx]
			kvIdx++
			if k == 0 {
				break
			}
			if kvIdx >= len(keysVals) {
				return fmt.Errorf("DenseNodes.keys_vals: unexpected end after key")
			}
			v := keysVals[kvIdx]
			kvIdx++

			pairs = append(pairs, [2]int32{k, v})

			if !isAddr && int(k) >= 0 && int(k) < len(ctx.st) {
				if strings.HasPrefix(ctx.st[k], "addr:") {
					isAddr = true
				}
			}
		}

		if !isAddr {
			continue
		}

		tags := make(Tags, len(pairs))
		for _, kv := range pairs {
			ki := int(kv[0])
			vi := int(kv[1])
			if ki < 0 || ki >= len(ctx.st) || vi < 0 || vi >= len(ctx.st) {
				continue
			}
			k := ctx.st[ki]
			if opts.KeepTag != nil && !opts.KeepTag(k) {
				continue
			}
			tags[k] = ctx.st[vi]
		}

		if err := cb.AddressNode(Node{
			ID:   idAcc,
			Lat:  latDeg,
			Lon:  lonDeg,
			Tags: tags,
		}); err != nil {
			return err
		}
	}

	return nil
}

func processWay(raw []byte, ctx blockCtx, wantHighway, wantAddrWay bool, opts Options, cb Callbacks) error {
	// message Way {
	//   required int64 id = 1;
	//   repeated uint32 keys = 2 [packed=true];
	//   repeated uint32 vals = 3 [packed=true];
	//   repeated sint64 refs = 8 [packed=true]; // DELTA
	// }
	var id int64
	var keysSegs [][]byte
	var valsSegs [][]byte
	var refsSegs [][]byte

	i := 0
	for i < len(raw) {
		tag, err := readUvarint(raw, &i)
		if err != nil {
			return err
		}
		field := int(tag >> 3)
		wire := int(tag & 7)

		switch field {
		case 1: // id (int64)
			if wire != 0 {
				return fmt.Errorf("Way.id: wrong wire=%d", wire)
			}
			v, err := readUvarint(raw, &i)
			if err != nil {
				return err
			}
			id = int64(v)

		case 2: // keys
			if wire != 2 {
				return fmt.Errorf("Way.keys: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			keysSegs = append(keysSegs, b)

		case 3: // vals
			if wire != 2 {
				return fmt.Errorf("Way.vals: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			valsSegs = append(valsSegs, b)

		case 8: // refs
			if wire != 2 {
				return fmt.Errorf("Way.refs: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			refsSegs = append(refsSegs, b)

		default:
			if err := skipField(wire, raw, &i); err != nil {
				return err
			}
		}
	}

	if len(keysSegs) == 0 {
		return nil
	}

	isHighway := false
	isAddr := false

	if wantHighway || wantAddrWay {
		h, a, err := scanWayKeys(keysSegs, ctx.st, wantHighway, wantAddrWay)
		if err != nil {
			return err
		}
		isHighway, isAddr = h, a
	}

	if !isHighway && !isAddr {
		return nil
	}

	tags, err := buildTags(keysSegs, valsSegs, ctx.st, opts.KeepTag)
	if err != nil {
		return err
	}

	var nodeIDs []int64
	if opts.EmitWayNodeIDs {
		nodeIDs, err = decodeWayRefs(refsSegs)
		if err != nil {
			return err
		}
	}

	w := Way{ID: id, NodeIDs: nodeIDs, Tags: tags}

	if isHighway && cb.HighwayWay != nil {
		if err := cb.HighwayWay(w); err != nil {
			return err
		}
	}
	if isAddr && cb.AddressWay != nil {
		if err := cb.AddressWay(w); err != nil {
			return err
		}
	}
	return nil
}

func processRelation(raw []byte, ctx blockCtx, opts Options, cb Callbacks) error {
	// message Relation {
	//   required int64 id = 1;
	//   repeated uint32 keys = 2 [packed=true];
	//   repeated uint32 vals = 3 [packed=true];
	//   repeated int32 roles_sid = 8 [packed=true];
	//   repeated sint64 memids = 9 [packed=true]; // DELTA
	//   repeated MemberType types = 10 [packed=true];
	// }
	var id int64
	var keysSegs [][]byte
	var valsSegs [][]byte
	var rolesSegs [][]byte
	var memidsSegs [][]byte
	var typesSegs [][]byte

	i := 0
	for i < len(raw) {
		tag, err := readUvarint(raw, &i)
		if err != nil {
			return err
		}
		field := int(tag >> 3)
		wire := int(tag & 7)

		switch field {
		case 1: // id
			if wire != 0 {
				return fmt.Errorf("Relation.id: wrong wire=%d", wire)
			}
			v, err := readUvarint(raw, &i)
			if err != nil {
				return err
			}
			id = int64(v)

		case 2: // keys
			if wire != 2 {
				return fmt.Errorf("Relation.keys: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			keysSegs = append(keysSegs, b)

		case 3: // vals
			if wire != 2 {
				return fmt.Errorf("Relation.vals: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			valsSegs = append(valsSegs, b)

		case 8: // roles_sid
			if wire != 2 {
				return fmt.Errorf("Relation.roles_sid: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			rolesSegs = append(rolesSegs, b)

		case 9: // memids
			if wire != 2 {
				return fmt.Errorf("Relation.memids: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			memidsSegs = append(memidsSegs, b)

		case 10: // types
			if wire != 2 {
				return fmt.Errorf("Relation.types: wrong wire=%d", wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			typesSegs = append(typesSegs, b)

		default:
			if err := skipField(wire, raw, &i); err != nil {
				return err
			}
		}
	}

	if cb.AddressRelation == nil || len(keysSegs) == 0 {
		return nil
	}

	isAddr, err := scanKeys(keysSegs, ctx.st, false, true)
	if err != nil {
		return err
	}
	if !isAddr {
		return nil
	}

	tags, err := buildTags(keysSegs, valsSegs, ctx.st, opts.KeepTag)
	if err != nil {
		return err
	}

	var members []Member
	if opts.EmitRelationMembers {
		roles, err := decodePackedInt32Segments(rolesSegs)
		if err != nil {
			return err
		}
		memDeltas, err := decodePackedSint64Segments(memidsSegs)
		if err != nil {
			return err
		}
		types, err := decodePackedU32Segments(typesSegs)
		if err != nil {
			return err
		}

		n := min(len(roles), len(memDeltas), len(types))
		members = make([]Member, 0, n)

		var memAcc int64
		for i := 0; i < n; i++ {
			memAcc += memDeltas[i]
			role := ""
			if roles[i] >= 0 && int(roles[i]) < len(ctx.st) {
				role = ctx.st[roles[i]]
			}

			var mt MemberType
			switch types[i] {
			case 0:
				mt = MemberNode
			case 1:
				mt = MemberWay
			case 2:
				mt = MemberRelation
			default:
				continue
			}

			members = append(members, Member{
				Type: mt,
				ID:   memAcc,
				Role: role,
			})
		}
	}

	return cb.AddressRelation(Relation{
		ID:      id,
		Members: members,
		Tags:    tags,
	})
}

// ---- Tag / Key helpers ----

func scanKeys(keysSegs [][]byte, st []string, wantHighway, wantAddr bool) (bool, error) {
	_, addr, err := scanWayKeys(keysSegs, st, wantHighway, wantAddr)
	return addr, err
}

func scanWayKeys(keysSegs [][]byte, st []string, wantHighway, wantAddr bool) (highway bool, addr bool, err error) {
	for _, seg := range keysSegs {
		j := 0
		for j < len(seg) {
			v, err := readUvarint(seg, &j)
			if err != nil {
				return false, false, err
			}
			idx := int(v)
			if idx < 0 || idx >= len(st) {
				continue
			}
			k := st[idx]
			if wantHighway && !highway && k == "highway" {
				highway = true
			}
			if wantAddr && !addr && strings.HasPrefix(k, "addr:") {
				addr = true
			}
			if (!wantHighway || highway) && (!wantAddr || addr) {
				return highway, addr, nil
			}
		}
	}
	return highway, addr, nil
}

func buildTags(keysSegs, valsSegs [][]byte, st []string, keep func(string) bool) (Tags, error) {
	keys, err := decodePackedU32Segments(keysSegs)
	if err != nil {
		return nil, err
	}
	vals, err := decodePackedU32Segments(valsSegs)
	if err != nil {
		return nil, err
	}

	n := len(keys)
	if len(vals) < n {
		n = len(vals)
	}
	tags := make(Tags, n)

	for i := 0; i < n; i++ {
		ki := int(keys[i])
		vi := int(vals[i])
		if ki < 0 || ki >= len(st) || vi < 0 || vi >= len(st) {
			continue
		}
		k := st[ki]
		if keep != nil && !keep(k) {
			continue
		}
		tags[k] = st[vi]
	}
	return tags, nil
}

// ---- Refs / packed decoding ----

func decodeWayRefs(refsSegs [][]byte) ([]int64, error) {
	deltas, err := decodePackedSint64Segments(refsSegs)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(deltas))
	var acc int64
	for _, d := range deltas {
		acc += d
		out = append(out, acc)
	}
	return out, nil
}

func decodePackedU32Segments(segs [][]byte) ([]uint32, error) {
	out := make([]uint32, 0, 64)
	for _, seg := range segs {
		j := 0
		for j < len(seg) {
			v, err := readUvarint(seg, &j)
			if err != nil {
				return nil, err
			}
			out = append(out, uint32(v))
		}
	}
	return out, nil
}

func decodePackedInt32Segments(segs [][]byte) ([]int32, error) {
	out := make([]int32, 0, 64)
	for _, seg := range segs {
		j := 0
		for j < len(seg) {
			v, err := readUvarint(seg, &j)
			if err != nil {
				return nil, err
			}
			out = append(out, int32(v))
		}
	}
	return out, nil
}

func decodePackedSint64Segments(segs [][]byte) ([]int64, error) {
	out := make([]int64, 0, 128)
	for _, seg := range segs {
		vals, err := decodePackedSint64(seg)
		if err != nil {
			return nil, err
		}
		out = append(out, vals...)
	}
	return out, nil
}

func decodePackedSint64(seg []byte) ([]int64, error) {
	out := make([]int64, 0, 128)
	j := 0
	for j < len(seg) {
		v, err := readUvarint(seg, &j)
		if err != nil {
			return nil, err
		}
		out = append(out, zigzagDecode64(v))
	}
	return out, nil
}

func decodePackedInt32(seg []byte) ([]int32, error) {
	out := make([]int32, 0, 128)
	j := 0
	for j < len(seg) {
		v, err := readUvarint(seg, &j)
		if err != nil {
			return nil, err
		}
		out = append(out, int32(v))
	}
	return out, nil
}

// ---- Coordinate decoding ----

func decodeCoord(lat, lon int64, ctx blockCtx) (float64, float64) {
	latNano := ctx.latOffset + lat*ctx.granularity
	lonNano := ctx.lonOffset + lon*ctx.granularity
	return float64(latNano) / 1e9, float64(lonNano) / 1e9
}

// ---- Protobuf wire helpers (minimal) ----

func readUvarint(b []byte, i *int) (uint64, error) {
	var x uint64
	var s uint
	for {
		if *i >= len(b) {
			return 0, io.ErrUnexpectedEOF
		}
		c := b[*i]
		*i++
		if c < 0x80 {
			if s >= 64 {
				return 0, fmt.Errorf("varint overflow")
			}
			return x | uint64(c)<<s, nil
		}
		x |= uint64(c&0x7f) << s
		s += 7
		if s >= 64 {
			return 0, fmt.Errorf("varint overflow")
		}
	}
}

func zigzagDecode64(u uint64) int64 {
	return int64((u >> 1) ^ uint64(-(int64(u & 1))))
}

func readBytes(b []byte, i *int) ([]byte, error) {
	n, err := readUvarint(b, i)
	if err != nil {
		return nil, err
	}
	if n > uint64(len(b)-*i) {
		return nil, io.ErrUnexpectedEOF
	}
	start := *i
	*i += int(n)
	return b[start:*i], nil
}

func readString(b []byte, i *int) (string, error) {
	v, err := readBytes(b, i)
	if err != nil {
		return "", err
	}
	return string(v), nil
}

func skipField(wire int, b []byte, i *int) error {
	switch wire {
	case 0: // varint
		_, err := readUvarint(b, i)
		return err
	case 1: // 64-bit
		if *i+8 > len(b) {
			return io.ErrUnexpectedEOF
		}
		*i += 8
		return nil
	case 2: // len-delimited
		_, err := readBytes(b, i)
		return err
	case 5: // 32-bit
		if *i+4 > len(b) {
			return io.ErrUnexpectedEOF
		}
		*i += 4
		return nil
	default:
		return fmt.Errorf("unsupported wire type: %d", wire)
	}
}
