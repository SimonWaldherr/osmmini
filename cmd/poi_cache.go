package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

type poiCachePayload struct {
	Version     int
	Nodes       map[int64]osmmini.Coord
	TaggedNodes map[int64]osmmini.Node
	Ways        map[int64]osmmini.Way
	Rels        map[int64]osmmini.Relation
}

// Decode individual entities so the JSON decoder never buffers the full
// regional cache. The resulting maps are installed directly, without copies.
func readPOICacheMap[T any](dec *json.Decoder) (map[int64]T, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, nil
	}
	if token != json.Delim('{') {
		return nil, fmt.Errorf("POI cache: expected entity map")
	}
	result := make(map[int64]T)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("POI cache: invalid key")
		}
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return nil, err
		}
		var value T
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		result[id] = value
	}
	_, err = dec.Token()
	return result, err
}

// Ignore additional metadata without materializing an unknown JSON subtree.
func skipPOICacheValue(dec *json.Decoder) error {
	depth := 0
	for {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			if delimiter == '{' || delimiter == '[' {
				depth++
			} else {
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

func readPOICache(path string) (poiCachePayload, error) {
	var payload poiCachePayload
	file, err := os.Open(path)
	if err != nil {
		return payload, err
	}
	defer file.Close()
	dec := json.NewDecoder(bufio.NewReader(file))
	token, err := dec.Token()
	if err != nil {
		return payload, err
	}
	if token != json.Delim('{') {
		return payload, fmt.Errorf("POI cache: expected object")
	}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return payload, err
		}
		switch token {
		case "version":
			err = dec.Decode(&payload.Version)
		case "nodes":
			payload.Nodes, err = readPOICacheMap[osmmini.Coord](dec)
		case "tagged_nodes":
			payload.TaggedNodes, err = readPOICacheMap[osmmini.Node](dec)
		case "ways":
			payload.Ways, err = readPOICacheMap[osmmini.Way](dec)
		case "rels":
			payload.Rels, err = readPOICacheMap[osmmini.Relation](dec)
		default:
			err = skipPOICacheValue(dec)
		}
		if err != nil {
			return payload, err
		}
	}
	if _, err = dec.Token(); err != nil {
		return payload, err
	}
	if _, err = dec.Token(); err != io.EOF {
		return payload, fmt.Errorf("POI cache: trailing content")
	}
	if payload.Version != poiCacheVersion {
		return payload, fmt.Errorf("unsupported POI cache version %d", payload.Version)
	}
	return payload, nil
}

// Compatibility helper for callers supplying their own maps. Production
// loading uses readPOICache directly to avoid keeping two map tables alive.
func (s *server) loadPOICache(path string, nodes map[int64]osmmini.Coord, tagged map[int64]osmmini.Node, ways map[int64]osmmini.Way, rels map[int64]osmmini.Relation) error {
	payload, err := readPOICache(path)
	if err != nil {
		return err
	}
	for id, value := range payload.Nodes {
		nodes[id] = value
	}
	for id, value := range payload.TaggedNodes {
		tagged[id] = value
	}
	for id, value := range payload.Ways {
		ways[id] = value
	}
	for id, value := range payload.Rels {
		rels[id] = value
	}
	return nil
}

func writePOICacheMap[T any](w io.Writer, values map[int64]T) error {
	if values == nil {
		_, err := io.WriteString(w, "null")
		return err
	}
	if _, err := io.WriteString(w, "{"); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	first := true
	for id, value := range values {
		if !first {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		first = false
		if _, err := fmt.Fprintf(w, "\"%d\":", id); err != nil {
			return err
		}
		if err := enc.Encode(value); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}")
	return err
}

// Encode one entity at a time into an atomic replacement, not a second
// in-memory copy of the entire POI index.
func (s *server) savePOICache(path string, nodes map[int64]osmmini.Coord, tagged map[int64]osmmini.Node, ways map[int64]osmmini.Way, rels map[int64]osmmini.Relation) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".poi-cache-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0644); err != nil {
		return err
	}
	w := bufio.NewWriter(file)
	if _, err := fmt.Fprintf(w, "{\"version\":%d,\"nodes\":", poiCacheVersion); err != nil {
		return err
	}
	if err := writePOICacheMap(w, nodes); err != nil {
		return err
	}
	if _, err := io.WriteString(w, ",\"tagged_nodes\":"); err != nil {
		return err
	}
	if err := writePOICacheMap(w, tagged); err != nil {
		return err
	}
	if _, err := io.WriteString(w, ",\"ways\":"); err != nil {
		return err
	}
	if err := writePOICacheMap(w, ways); err != nil {
		return err
	}
	if _, err := io.WriteString(w, ",\"rels\":"); err != nil {
		return err
	}
	if err := writePOICacheMap(w, rels); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "}\n"); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
