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
	// Nodes must still be processed if any node callback is set, even if no
	// address callback is used. Otherwise router building (which relies on
	// cb.Node) and POI extraction (which relies on cb.TaggedNode) silently
	// miss all nodes.
	wantNode := cb.Node != nil || cb.AddressNode != nil || cb.TaggedNode != nil

	wantAddrWay := cb.AddressWay != nil
	wantTaggedWay := cb.TaggedWay != nil
	wantAddrRel := cb.AddressRelation != nil
	wantTaggedRel := cb.TaggedRelation != nil

	br := bufio.NewReaderSize(r, 1<<20)
	var decoder pbfBlobDecoder
	defer decoder.close()

	for {
		if opts.Context != nil {
			if err := opts.Context.Err(); err != nil {
				return err
			}
		}
		typ, raw, err := readNextBlock(br, &decoder)
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
			if err := processOSMData(raw, wantHighway, wantNode, wantAddrWay, wantTaggedWay, wantAddrRel, wantTaggedRel, opts, cb); err != nil {
				return err
			}
		default:
			// unknown block type -> ignore
			continue
		}
	}
}

// ---- Top-Level PBF Block I/O ----

func readNextBlock(r *bufio.Reader, decoder *pbfBlobDecoder) (typ string, raw []byte, err error) {
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
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
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
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return "", nil, err
	}

	raw, err = decoder.decode(blobBytes)
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

// Each sequential scan owns its decoder. Reusing zlib's sliding window avoids
// allocating a new decompressor for every block without retaining it globally.
type pbfBlobDecoder struct {
	reader io.ReadCloser
}

func (d *pbfBlobDecoder) close() {
	if d.reader != nil {
		_ = d.reader.Close()
	}
}

func parseAndDecompressBlob(b []byte) ([]byte, error) {
	var decoder pbfBlobDecoder
	defer decoder.close()
	return decoder.decode(b)
}

func (d *pbfBlobDecoder) decode(b []byte) ([]byte, error) {
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
		compressed := bytes.NewReader(zlibData)
		if d.reader == nil {
			var err error
			d.reader, err = zlib.NewReader(compressed)
			if err != nil {
				return nil, err
			}
		} else if err := d.reader.(zlib.Resetter).Reset(compressed, nil); err != nil {
			return nil, err
		}
		zr := d.reader

		var out []byte
		var err error
		if rawSize > 0 && rawSize <= 32<<20 {
			// PBF supplies the decoded size. Leave ReadFrom one read's spare
			// capacity so checking EOF does not grow an exactly full buffer.
			// Larger or missing hints retain the streaming fallback.
			buf := bytes.NewBuffer(make([]byte, 0, rawSize+bytes.MinRead))
			_, err = buf.ReadFrom(zr)
			out = buf.Bytes()
		} else {
			out, err = io.ReadAll(zr)
		}
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

func processOSMData(raw []byte, wantHighway, wantNode, wantAddrWay, wantTaggedWay, wantAddrRel, wantTaggedRel bool, opts Options, cb Callbacks) error {
	ctx := blockCtx{
		granularity: 100,
		latOffset:   0,
		lonOffset:   0,
	}

	var groupBuf [1][]byte
	groups := groupBuf[:0]
	wantStrings := wantHighway || wantAddrWay || wantTaggedWay || wantAddrRel || wantTaggedRel || cb.AddressNode != nil || cb.TaggedNode != nil

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
			st, err := parseStringTable(msg, wantStrings)
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
		if err := processPrimitiveGroup(g, ctx, wantHighway, wantNode, wantAddrWay, wantTaggedWay, wantAddrRel, wantTaggedRel, opts, cb); err != nil {
			return err
		}
	}
	return nil
}

func parseStringTable(raw []byte, retain bool) ([]string, error) {
	// message StringTable { repeated bytes s = 1; }
	var st []string
	if retain {
		st = make([]string, 0, 256)
	}

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
			// Coordinate-only passes still validate every field, but have no
			// consumer for the string copies or their directory.
			if retain {
				st = append(st, string(b))
			}

		default:
			if err := skipField(wire, raw, &i); err != nil {
				return nil, err
			}
		}
	}

	if !retain {
		return nil, nil
	}
	if len(st) == 0 {
		return []string{""}, nil
	}
	st[0] = ""
	return st, nil
}

func processPrimitiveGroup(raw []byte, ctx blockCtx, wantHighway, wantNode, wantAddrWay, wantTaggedWay, wantAddrRel, wantTaggedRel bool, opts Options, cb Callbacks) error {
	i := 0
	for i < len(raw) {
		if opts.Context != nil {
			if err := opts.Context.Err(); err != nil {
				return err
			}
		}
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
			if wantHighway || wantAddrWay || wantTaggedWay {
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
			if wantAddrRel || wantTaggedRel {
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
	var keysBuf, valsBuf [1][]byte
	keysSegs, valsSegs := keysBuf[:0], valsBuf[:0]

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

	if cb.AddressNode == nil && cb.TaggedNode == nil {
		return nil
	}

	isAddr := false
	if cb.AddressNode != nil {
		var err error
		isAddr, err = scanKeys(keysSegs, ctx.st, false, true)
		if err != nil {
			return err
		}
	}
	taggedCandidate := cb.TaggedNode != nil
	if taggedCandidate && opts.TaggedNodeKey != nil {
		var err error
		taggedCandidate, err = hasMatchingKey(keysSegs, ctx.st, opts.TaggedNodeKey)
		if err != nil {
			return err
		}
	}
	if !isAddr && !taggedCandidate {
		return nil
	}

	tags, err := buildTags(keysSegs, valsSegs, ctx.st, opts.KeepTag)
	if err != nil {
		return err
	}

	n := Node{
		ID:   id,
		Lat:  latDeg,
		Lon:  lonDeg,
		Tags: tags,
	}
	if isAddr && cb.AddressNode != nil {
		if err := cb.AddressNode(n); err != nil {
			return err
		}
	}
	if taggedCandidate && len(tags) > 0 {
		if err := cb.TaggedNode(n); err != nil {
			return err
		}
	}
	return nil
}

func processDenseNodes(raw []byte, ctx blockCtx, opts Options, cb Callbacks) error {
	// Keep packed data in the immutable block and decode each delta directly.
	// Most files have one packed field per stream; the inline buffers avoid
	// allocating even the segment directory in that common case.
	var idBuf, latBuf, lonBuf, kvBuf [1][]byte
	idSegs, latSegs, lonSegs, kvSegs := idBuf[:0], latBuf[:0], lonBuf[:0], kvBuf[:0]
	wantTags := cb.AddressNode != nil || cb.TaggedNode != nil
	i := 0
	for i < len(raw) {
		tag, err := readUvarint(raw, &i)
		if err != nil {
			return err
		}
		field, wire := int(tag>>3), int(tag&7)
		switch field {
		case 1, 8, 9, 10:
			if wire != 2 {
				return fmt.Errorf("DenseNodes field %d: wrong wire=%d", field, wire)
			}
			b, err := readBytes(raw, &i)
			if err != nil {
				return err
			}
			switch field {
			case 1:
				idSegs = append(idSegs, b)
			case 8:
				latSegs = append(latSegs, b)
			case 9:
				lonSegs = append(lonSegs, b)
			case 10:
				kvSegs = append(kvSegs, b)
			}
		default:
			if err := skipField(wire, raw, &i); err != nil {
				return err
			}
		}
	}

	// Validate coordinates before invoking callbacks, as the materialized
	// decoder did. Counting requires no decoded arrays and no retained memory.
	n, err := packedValueCount(idSegs)
	if err != nil {
		return err
	}
	latCount, err := packedValueCount(latSegs)
	if err != nil {
		return err
	}
	lonCount, err := packedValueCount(lonSegs)
	if err != nil {
		return err
	}
	if _, err := packedValueCount(kvSegs); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if latCount != n || lonCount != n {
		return fmt.Errorf("DenseNodes length mismatch: ids=%d lat=%d lon=%d", n, latCount, lonCount)
	}

	ids, lats, lons := packedCursor{segs: idSegs}, packedCursor{segs: latSegs}, packedCursor{segs: lonSegs}
	kv := packedCursor{segs: kvSegs}
	var idAcc, latAcc, lonAcc int64
	for idx := 0; idx < n; idx++ {
		if idx&1023 == 0 && opts.Context != nil {
			if err := opts.Context.Err(); err != nil {
				return err
			}
		}
		id, _, _ := ids.next() // Coordinate streams were validated above.
		lat, _, _ := lats.next()
		lon, _, _ := lons.next()
		idAcc += zigzagDecode64(id)
		latAcc += zigzagDecode64(lat)
		lonAcc += zigzagDecode64(lon)
		latDeg, lonDeg := decodeCoord(latAcc, lonAcc, ctx)
		if cb.Node != nil {
			if err := cb.Node(idAcc, latDeg, lonDeg); err != nil {
				return err
			}
		}
		if !wantTags {
			continue
		}

		// Scan tags once for callback selection. Replaying the compact cursor
		// below avoids allocating a temporary slice of every key/value pair.
		tagStart := kv
		pairCount := 0
		isAddr := false
		taggedCandidate := cb.TaggedNode != nil && opts.TaggedNodeKey == nil
		for {
			key, ok, err := kv.next()
			if err != nil {
				return err
			}
			if !ok {
				if pairCount != 0 {
					return fmt.Errorf("DenseNodes.keys_vals: unexpected end")
				}
				break // An omitted keys_vals stream means no tags.
			}
			k := int32(key)
			if k == 0 {
				break
			}
			if _, ok, err = kv.next(); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("DenseNodes.keys_vals: unexpected end after key")
			}
			pairCount++
			if k < 0 || int(k) >= len(ctx.st) {
				continue
			}
			keyName := ctx.st[k]
			if cb.AddressNode != nil && !isAddr && strings.HasPrefix(keyName, "addr:") {
				isAddr = true
			}
			if cb.TaggedNode != nil && !taggedCandidate && opts.TaggedNodeKey != nil && opts.TaggedNodeKey(keyName) {
				taggedCandidate = true
			}
		}
		if pairCount == 0 || (!isAddr && !taggedCandidate) {
			continue
		}

		tags := make(Tags, pairCount)
		for pair := 0; pair < pairCount; pair++ {
			key, _, _ := tagStart.next()
			val, _, _ := tagStart.next()
			ki, vi := int32(key), int32(val)
			if ki < 0 || int(ki) >= len(ctx.st) || vi < 0 || int(vi) >= len(ctx.st) {
				continue
			}
			k := ctx.st[ki]
			if opts.KeepTag == nil || opts.KeepTag(k) {
				tags[k] = ctx.st[vi]
			}
		}
		node := Node{ID: idAcc, Lat: latDeg, Lon: lonDeg, Tags: tags}
		if isAddr && cb.AddressNode != nil {
			if err := cb.AddressNode(node); err != nil {
				return err
			}
		}
		if taggedCandidate && len(tags) > 0 {
			if err := cb.TaggedNode(node); err != nil {
				return err
			}
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
	var keysBuf, valsBuf, refsBuf [1][]byte
	keysSegs, valsSegs, refsSegs := keysBuf[:0], valsBuf[:0], refsBuf[:0]

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

	taggedCandidate := cb.TaggedWay != nil
	if taggedCandidate && opts.TaggedWayKey != nil {
		var err error
		taggedCandidate, err = hasMatchingKey(keysSegs, ctx.st, opts.TaggedWayKey)
		if err != nil {
			return err
		}
	}
	if !isHighway && !isAddr && !taggedCandidate {
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
	// If the way carries any tags that the caller asked to keep, expose it
	// to TaggedWay so higher-level code can index POIs or area ways.
	if taggedCandidate && len(tags) > 0 {
		if err := cb.TaggedWay(w); err != nil {
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
	var keysBuf, valsBuf, rolesBuf, memidsBuf, typesBuf [1][]byte
	keysSegs, valsSegs := keysBuf[:0], valsBuf[:0]
	rolesSegs, memidsSegs, typesSegs := rolesBuf[:0], memidsBuf[:0], typesBuf[:0]

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

	// Allow emitting relations either when the caller wants address relations
	// or when the caller registered TaggedRelation to receive arbitrary
	// relations that match KeepTag.
	if len(keysSegs) == 0 {
		return nil
	}

	// Determine whether the relation has address tags for the legacy callback.
	// TaggedRelation is independent of address tags and receives relations with
	// at least one tag accepted by Options.KeepTag.
	isAddr := false
	if cb.AddressRelation != nil {
		var err error
		isAddr, err = scanKeys(keysSegs, ctx.st, false, true)
		if err != nil {
			return err
		}
	}
	taggedCandidate := cb.TaggedRelation != nil
	if taggedCandidate && opts.TaggedRelationKey != nil {
		var err error
		taggedCandidate, err = hasMatchingKey(keysSegs, ctx.st, opts.TaggedRelationKey)
		if err != nil {
			return err
		}
	}
	if !isAddr && !taggedCandidate {
		return nil
	}

	tags, err := buildTags(keysSegs, valsSegs, ctx.st, opts.KeepTag)
	if err != nil {
		return err
	}

	if !isAddr && len(tags) == 0 {
		return nil
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

	r := Relation{
		ID:      id,
		Members: members,
		Tags:    tags,
	}
	if isAddr && cb.AddressRelation != nil {
		if err := cb.AddressRelation(r); err != nil {
			return err
		}
	}
	if taggedCandidate && len(tags) > 0 {
		if err := cb.TaggedRelation(r); err != nil {
			return err
		}
	}
	return nil
}

// ---- Tag / Key helpers ----

func scanKeys(keysSegs [][]byte, st []string, wantHighway, wantAddr bool) (bool, error) {
	_, addr, err := scanWayKeys(keysSegs, st, wantHighway, wantAddr)
	return addr, err
}

// hasMatchingKey checks compact PBF key segments without building a full tag
// map. It lets callers avoid decoding node references or values for entities
// that cannot match their tagged-entity index.
func hasMatchingKey(keysSegs [][]byte, st []string, match func(string) bool) (bool, error) {
	if match == nil {
		return true, nil
	}
	for _, seg := range keysSegs {
		j := 0
		for j < len(seg) {
			v, err := readUvarint(seg, &j)
			if err != nil {
				return false, err
			}
			idx := int(v)
			if idx >= 0 && idx < len(st) && match(st[idx]) {
				return true, nil
			}
		}
	}
	return false, nil
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
	keys, vals := packedCursor{segs: keysSegs}, packedCursor{segs: valsSegs}
	tags := make(Tags)
	for {
		key, keyOK, err := keys.next()
		if err != nil {
			return nil, err
		}
		val, valOK, err := vals.next()
		if err != nil {
			return nil, err
		}
		if !keyOK || !valOK {
			// Preserve validation of unmatched trailing keys and values.
			for _, cursor := range []*packedCursor{&keys, &vals} {
				for {
					_, ok, err := cursor.next()
					if err != nil {
						return nil, err
					}
					if !ok {
						break
					}
				}
			}
			return tags, nil
		}
		ki, vi := int(uint32(key)), int(uint32(val))
		if ki < 0 || ki >= len(st) || vi < 0 || vi >= len(st) {
			continue
		}
		k := st[ki]
		if keep == nil || keep(k) {
			tags[k] = st[vi]
		}
	}
}

// ---- Refs / packed decoding ----

// packedCursor walks repeated packed protobuf fields without materializing
// their integer values. Copies are independent cursors into immutable input.
// Varints must finish within their own packed field, including on replay.
type packedCursor struct {
	segs [][]byte
	seg  int
	pos  int
}

func (c *packedCursor) next() (uint64, bool, error) {
	for c.seg < len(c.segs) {
		seg := c.segs[c.seg]
		if c.pos == len(seg) {
			c.seg++
			c.pos = 0
			continue
		}
		if v := seg[c.pos]; v < 0x80 {
			c.pos++
			return uint64(v), true, nil
		}
		v, err := readUvarint(seg, &c.pos)
		return v, err == nil, err
	}
	return 0, false, nil
}

func packedValueCount(segs [][]byte) (int, error) {
	n := 0
	for _, seg := range segs {
		var shift uint
		for _, c := range seg {
			if c < 0x80 {
				if shift == 63 && c > 1 {
					return 0, fmt.Errorf("varint overflow")
				}
				n++
				shift = 0
				continue
			}
			shift += 7
			if shift >= 64 {
				return 0, fmt.Errorf("varint overflow")
			}
		}
		if shift != 0 {
			return 0, io.ErrUnexpectedEOF
		}
	}
	return n, nil
}

func decodeWayRefs(refsSegs [][]byte) ([]int64, error) {
	n, err := packedValueCount(refsSegs)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, n)
	var acc int64
	for _, seg := range refsSegs {
		for i := 0; i < len(seg); {
			v, _ := readUvarint(seg, &i) // Validated while sizing the result.
			acc += zigzagDecode64(v)
			out = append(out, acc)
		}
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
		for j := 0; j < len(seg); {
			v, err := readUvarint(seg, &j)
			if err != nil {
				return nil, err
			}
			out = append(out, zigzagDecode64(v))
		}
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
			if s >= 64 || (s == 63 && c > 1) {
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
