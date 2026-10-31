package osmmini

// Directed ALT potentials over the original graph. Distances are stored as
// conservative uint16 intervals, not rounded point estimates. Two landmarks per
// metric (distance and free-flow time) need 16 bytes/node for both directions,
// independent of the number of profiles.
import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
)

const landmarkMagic = "OSMALT01"
const landmarkHeaderSize = 176
const landmarkCount = 4
const landmarkStride = landmarkCount * 4
const landmarkInfinity = math.MaxUint16

type landmarkIndex struct {
	data    []byte
	steps   [8]float64
	release func() error
}

func hashLandmarkData(ctx context.Context, data []byte) ([32]byte, error) {
	return hashLandmarkParts(ctx, data)
}

// VerifyGraphChecksum verifies the immutable mapped graph against a published
// SHA-256 checksum. The successful computation is reused when binding an ALT
// index to the same graph. Like LoadLandmarks, call before starting queries.
func (r *Router) VerifyGraphChecksum(ctx context.Context, expected string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(expected) != sha256.Size*2 {
		return errors.New("invalid graph checksum")
	}
	digest, err := r.graphChecksum(ctx)
	if err != nil {
		return err
	}
	if hex.EncodeToString(digest[:]) != expected {
		return errors.New("checksum mismatch: graph.bin")
	}
	return nil
}

func (r *Router) graphChecksum(ctx context.Context) ([32]byte, error) {
	if err := ctx.Err(); err != nil {
		return [32]byte{}, err
	}
	d := r.g.store
	if d == nil || d.release == nil {
		return [32]byte{}, errors.New("graph is not mapped")
	}
	if d.checksumReady {
		return d.checksum, nil
	}
	digest, err := hashLandmarkData(ctx, d.data)
	if err == nil {
		d.checksum = digest
		d.checksumReady = true
	}
	return digest, err
}

func hashLandmarkParts(ctx context.Context, parts ...[]byte) ([32]byte, error) {
	h := sha256.New()
	for _, data := range parts {
		for len(data) > 0 {
			if err := ctx.Err(); err != nil {
				return [32]byte{}, err
			}
			n := min(len(data), 1<<20)
			_, _ = h.Write(data[:n])
			data = data[n:]
		}
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, ctx.Err()
}

// WriteLandmarks prepares directed distance and free-flow-time lower bounds for
// a compact file-backed graph. The destination must be new. Cancellation removes
// incomplete output. This does not change the router or its graph.
func (r *Router) WriteLandmarks(ctx context.Context, path string) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	d := r.g.store
	if d == nil || !d.compact || d.release == nil || d.nodes == 0 {
		return errors.New("landmarks require a nonempty compact graph")
	}
	if uint64(d.nodes) > math.MaxInt32 || uint64(d.edgeCount) > math.MaxUint32 || d.nodes > int(^uint(0)>>1)/landmarkStride || d.edgeCount > int(^uint(0)>>1)/8 {
		return errors.New("graph exceeds landmark index limits; split the region")
	}
	reverse, seed, err := buildLandmarkReverse(ctx, d)
	if err != nil {
		return err
	}
	payload := make([]byte, d.nodes*landmarkStride)
	var header [landmarkHeaderSize]byte
	copy(header[:], landmarkMagic)
	binary.LittleEndian.PutUint64(header[8:], uint64(d.nodes))
	binary.LittleEndian.PutUint64(header[16:], landmarkCount)
	closest := make([]float64, d.nodes)
	for i := range closest {
		closest[i] = math.Inf(1)
	}
	selected := make(map[uint32]bool, landmarkCount)
	var eligible []bool
	var forward, backward []float64
	initialSeed := seed
	for landmark := 0; landmark < landmarkCount; landmark++ {
		if landmark == landmarkCount/2 {
			seed = initialSeed
			clear(selected)
			for i := range closest {
				closest[i] = math.Inf(1)
			}
		}
		timed := landmark >= landmarkCount/2
		binary.LittleEndian.PutUint32(header[24+landmark*4:], seed)
		selected[seed] = true
		forward, err = landmarkDistances(ctx, d, nil, seed, timed, forward)
		if err != nil {
			return err
		}
		backward, err = landmarkDistances(ctx, d, reverse, seed, timed, backward)
		if err != nil {
			return err
		}
		if eligible == nil {
			eligible = make([]bool, d.nodes)
			for i := range eligible {
				eligible[i] = !math.IsInf(forward[i], 1) && !math.IsInf(backward[i], 1)
			}
		}
		for direction, distances := range [][]float64{forward, backward} {
			table := landmark*2 + direction
			maxDistance := 0.0
			for _, distance := range distances {
				if !math.IsInf(distance, 1) {
					maxDistance = max(maxDistance, distance)
				}
			}
			step := 1.0
			if timed {
				step = 1.0 / 16
			}
			for maxDistance/step >= landmarkInfinity-1 {
				step *= 2
			}
			binary.LittleEndian.PutUint64(header[40+table*8:], math.Float64bits(step))
			for node, distance := range distances {
				if node%4096 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				value := uint16(landmarkInfinity)
				if !math.IsInf(distance, 1) {
					value = uint16(math.Floor(distance / step))
				}
				binary.LittleEndian.PutUint16(payload[node*landmarkStride+table*2:], value)
			}
		}
		// Farthest-first within the seed's strongly connected component. Keeping
		// directed reachability in both tables avoids wasting landmarks on sinks.
		best := -1.0
		for node := range closest {
			if !eligible[node] {
				continue
			}
			closest[node] = min(closest[node], min(forward[node], backward[node]))
			if !selected[uint32(node)] && closest[node] > best {
				best = closest[node]
				seed = uint32(node)
			}
		}
	}
	graphHash, err := hashLandmarkData(ctx, d.data)
	if err != nil {
		return err
	}
	copy(header[104:136], graphHash[:])
	// Quantization steps and landmark IDs affect admissibility just as much
	// as table bytes. Checksum the entire index except its checksum field.
	indexHash, err := hashLandmarkParts(ctx, header[:136], header[168:], payload)
	if err != nil {
		return err
	}
	copy(header[136:168], indexHash[:])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
	if _, err = f.Write(header[:]); err != nil {
		return err
	}
	for len(payload) > 0 {
		if err = ctx.Err(); err != nil {
			return err
		}
		n := min(len(payload), 1<<20)
		if _, err = f.Write(payload[:n]); err != nil {
			return err
		}
		payload = payload[n:]
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return ctx.Err()
}

type landmarkReverse struct {
	offsets, nodes []uint32
	distances      []float64
	attributes     []uint32
}

func buildLandmarkReverse(ctx context.Context, d *diskGraph) (*landmarkReverse, uint32, error) {
	offsets := make([]uint32, d.nodes+1)
	// Negative roots hold component sizes, all other entries hold parents.
	components := make([]int32, d.nodes)
	for i := range components {
		components[i] = -1
	}
	root := func(node uint32) uint32 {
		for components[node] >= 0 {
			node = uint32(components[node])
		}
		return node
	}
	for node := 0; node < d.nodes; node++ {
		if node%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
		for it := d.edgesAt(node); it.next(); {
			target, _, distance := it.compactFields()
			if distance < 0 || math.IsNaN(distance) || math.IsInf(distance, 0) {
				return nil, 0, errors.New("invalid landmark edge distance")
			}
			offsets[target+1]++
			a, b := root(uint32(node)), root(target)
			if a != b {
				if components[a] > components[b] {
					a, b = b, a
				}
				components[a] += components[b]
				components[b] = int32(a)
			}
		}
	}
	largest := uint32(0)
	for i, size := range components {
		if size < components[largest] {
			largest = uint32(i)
		}
	}
	seed := largest
	degree := -1
	for node := 0; node < d.nodes; node++ {
		if root(uint32(node)) == largest {
			_, count := d.edgeRange(node)
			if count > degree {
				seed = uint32(node)
				degree = count
			}
		}
	}
	for i := 1; i < len(offsets); i++ {
		offsets[i] += offsets[i-1]
	}
	reverse := &landmarkReverse{offsets: offsets, nodes: make([]uint32, d.edgeCount), distances: make([]float64, d.edgeCount), attributes: make([]uint32, d.edgeCount)}
	cursor := make([]uint32, d.nodes)
	copy(cursor, offsets)
	for node := 0; node < d.nodes; node++ {
		if node%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
		for it := d.edgesAt(node); it.next(); {
			target, attribute, distance := it.compactFields()
			at := cursor[target]
			cursor[target]++
			reverse.nodes[at] = uint32(node)
			reverse.distances[at] = distance
			reverse.attributes[at] = attribute
		}
	}
	return reverse, seed, nil
}

func landmarkDistances(ctx context.Context, d *diskGraph, reverse *landmarkReverse, seed uint32, timed bool, distances []float64) ([]float64, error) {
	if len(distances) != d.nodes {
		distances = make([]float64, d.nodes)
	}
	for i := range distances {
		distances[i] = math.Inf(1)
	}
	distances[seed] = 0
	queue := nodeValueHeap[uint32]{}
	queue.push(nodeCandidate[uint32]{id: seed})
	for pops := 0; len(queue) > 0; pops++ {
		if pops%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		current := queue.pop()
		if current.dist != distances[current.id] {
			continue
		}
		relax := func(target, attribute uint32, length float64) {
			if timed {
				speed := math.Float64frombits(binary.LittleEndian.Uint64(d.data[d.attributesOffset+int(attribute)*graphAttributeSize:]))
				if speed <= 0 {
					speed = 50
				}
				if math.IsNaN(speed) || math.IsInf(speed, 0) {
					speed = math.Inf(1)
				}
				length = length * 3.6 / max(3, speed)
			}
			distance := current.dist + length
			if distance < distances[target] {
				distances[target] = distance
				queue.push(nodeCandidate[uint32]{id: target, dist: distance, priority: distance})
			}
		}
		if reverse == nil {
			for it := d.edgesAt(int(current.id)); it.next(); {
				target, attribute, length := it.compactFields()
				relax(target, attribute, length)
			}
		} else {
			for i := reverse.offsets[current.id]; i < reverse.offsets[current.id+1]; i++ {
				relax(reverse.nodes[i], reverse.attributes[i], reverse.distances[i])
			}
		}
	}
	return distances, ctx.Err()
}

// LoadLandmarks verifies and maps a prepared index belonging to this graph.
// Like Close, call it only while no queries are running. A failed load leaves
// an existing index untouched. Queries never write to the mapped tables.
func (r *Router) LoadLandmarks(ctx context.Context, path string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d := r.g.store
	if d == nil || !d.compact || d.release == nil {
		return errors.New("landmarks require a compact graph")
	}
	data, release, err := mapGraphFile(path)
	if err != nil {
		return err
	}
	fail := func(err error) error { _ = release(); return err }
	if len(data) < landmarkHeaderSize || string(data[:8]) != landmarkMagic || binary.LittleEndian.Uint64(data[8:]) != uint64(d.nodes) || binary.LittleEndian.Uint64(data[16:]) != landmarkCount || uint64(len(data)-landmarkHeaderSize) != uint64(d.nodes)*landmarkStride {
		return fail(errors.New("invalid landmark index header"))
	}
	index := &landmarkIndex{data: data[landmarkHeaderSize:], release: release}
	for i := 0; i < landmarkCount; i++ {
		if binary.LittleEndian.Uint32(data[24+i*4:]) >= uint32(d.nodes) {
			return fail(errors.New("invalid landmark node"))
		}
	}
	for i := range index.steps {
		step := math.Float64frombits(binary.LittleEndian.Uint64(data[40+i*8:]))
		fraction, _ := math.Frexp(step)
		minimumStep := 1.0
		if i >= landmarkCount {
			minimumStep = 1.0 / 16
		}
		if step < minimumStep || step > math.MaxFloat64/(landmarkInfinity-1) || math.IsNaN(step) || math.IsInf(step, 0) || fraction != 0.5 {
			return fail(errors.New("invalid landmark quantization"))
		}
		index.steps[i] = step
	}
	for _, value := range data[168:176] {
		if value != 0 {
			return fail(errors.New("invalid landmark reserved bytes"))
		}
	}
	graphHash, err := r.graphChecksum(ctx)
	if err != nil {
		return fail(err)
	}
	indexHash, err := hashLandmarkParts(ctx, data[:136], data[168:])
	if err != nil {
		return fail(err)
	}
	if string(graphHash[:]) != string(data[104:136]) || string(indexHash[:]) != string(data[136:168]) {
		return fail(errors.New("landmark checksum or graph mismatch"))
	}
	if r.landmarks != nil {
		if err := r.landmarks.release(); err != nil {
			return fail(fmt.Errorf("release previous landmarks: %w", err))
		}
	}
	r.landmarks = index
	return nil
}

func (r *Router) queryHeuristic(targetID int64, target Coord, opt RouteOptions) routeHeuristic {
	h := newRouteHeuristic(target, opt)
	w := opt.Weights
	// Distance landmarks alone gave no reliable economy-query improvement.
	// Keep that objective's inexpensive geometric heuristic.
	if !h.disabled && opt.Objective != ObjectiveEconomy && r.landmarks != nil && w.LeftTurn >= 0 && w.RightTurn >= 0 && w.UTurn >= 0 && w.Crossing >= 0 && w.TrafficLightPenalty >= 0 {
		node := r.g.store.nodeIndex(targetID)
		if node >= 0 {
			h.landmarks = r.landmarks
			for i := range h.landmarkTarget {
				h.landmarkTarget[i] = binary.LittleEndian.Uint16(h.landmarks.data[node*landmarkStride+i*2:])
			}
			h.landmarkScale = 1
			switch opt.Objective {
			case ObjectiveDuration:
				h.landmarkScale = 3.6 / opt.Weights.MaxSpeedKph
				h.landmarkTime = true
			}
		}
	}
	return h
}

func (h *routeHeuristic) estimateIndex(d *diskGraph, node uint32) float64 {
	if h.disabled {
		return 0
	}
	if h.landmarks == nil {
		return h.estimateCoordinate(d.coordAt(int(node)))
	}
	values := h.landmarks.data[int(node)*landmarkStride:][:landmarkStride]
	lower := [2]float64{}
	count := landmarkCount / 2
	if h.landmarkTime {
		count = landmarkCount
	}
	for i := 0; i < count; i++ {
		metric := i / (landmarkCount / 2)
		fromLandmark := binary.LittleEndian.Uint16(values[i*4:])
		toLandmark := binary.LittleEndian.Uint16(values[i*4+2:])
		targetFrom, targetTo := h.landmarkTarget[i*2], h.landmarkTarget[i*2+1]
		if fromLandmark != landmarkInfinity && targetFrom != landmarkInfinity {
			lower[metric] = max(lower[metric], float64(int(targetFrom)-int(fromLandmark)-1)*h.landmarks.steps[i*2])
		}
		if toLandmark != landmarkInfinity && targetTo != landmarkInfinity {
			lower[metric] = max(lower[metric], float64(int(toLandmark)-int(targetTo)-1)*h.landmarks.steps[i*2+1])
		}
	}
	// Leave an additional relative margin for floating-point accumulation in the
	// preprocessing and query searches; never subtract quantized point values
	// without their interval error allowance.
	bound := max(lower[0]*(1-1e-12)*h.landmarkScale, lower[1]*(1-1e-12))
	coordinate := d.coordAt(int(node))
	// Walking a meridian, then the target's parallel, upper-bounds the
	// great-circle distance. If ALT already dominates that cheap upper bound,
	// the expensive trigonometric lower bound cannot improve the result.
	longitude := math.Abs(h.target.Lon - coordinate.Lon)
	if longitude > 180 {
		longitude = 360 - longitude
	}
	upper := 6371000.0 * deg2rad(math.Abs(h.target.Lat-coordinate.Lat)+h.cosLatitude*longitude)
	if h.objective == ObjectiveDuration {
		upper = upper * 3.6 / h.maxSpeedKph
	}
	if bound > upper*(1+1e-12) {
		return bound
	}
	return max(h.estimateCoordinate(coordinate), bound)
}
