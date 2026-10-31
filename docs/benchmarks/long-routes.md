# Long-route routing benchmarks

Measured on 2026-10-24 with Go 1.27.3 on an Apple M2 Max (darwin/arm64).
The baseline is commit `16ad9147f886c5614d796f3636e9a2e6eba00922`, with only
`router_long_bench_test.go` and the two `router_bench_cpu_*_test.go` helpers
copied into that checkout. Its routing implementation is unchanged.

## Fixture and method

The local Niederbayern OSM extract produces a compact graph with 2,085,735 nodes
and 4,285,799 directed edges. Its SHA-256 is
`c1e203e3bf9944c35847b04894257dd1caadfa225aedfe1d3c94eb5a40b28a23`.
Three endpoint pairs exercise the car and delivery profiles on routes of
110–174 km. This does not establish performance for the full DACH graph.

Each table entry is the median of three runs, with three timed queries per run.
Before/after binaries ran in adjacent pairs for each route and profile, with
order reversed in the second round and `GOMAXPROCS=1`. Import, graph opening,
endpoint snapping and one warm-up query are excluded. Queries include the full
path, coordinates and route metrics; no route-result cache or landmarks are used.

Other workloads were active. The comparison uses process CPU time (`getrusage`,
user + system time, including GC) to exclude scheduler waits. CPU frequency and
cache contention can still affect results. Wall-clock samples are in the raw
logs; these are not isolated-machine latency guarantees. MB/op is decimal Go
heap allocation, not process RSS or the memory-mapped graph.

## Results against the previous commit

| Route / profile | km | CPU before → after | Reduction | MB/op before → after | Allocs/op before → after |
| --- | ---: | ---: | ---: | ---: | ---: |
| Landshut–Passau/car | 118.6 | 133.6 → 116.2 ms | 13.0% | 28.1 → 16.9 | 2082 → 36 |
| Landshut–Passau/delivery | 110.5 | 1421.0 → 518.6 ms | 63.5% | 148.1 → 51.3 | 8644 → 43 |
| Kelheim–Passau/car | 173.6 | 181.9 → 156.0 ms | 14.2% | 56.0 → 33.8 | 4119 → 39 |
| Kelheim–Passau/delivery | 149.7 | 1363.0 → 520.3 ms | 61.8% | 173.8 → 51.3 | 10149 → 43 |
| Landshut–Freyung/car | 140.4 | 262.5 → 237.5 ms | 9.5% | 56.1 → 33.8 | 4133 → 39 |
| Landshut–Freyung/delivery | 134.7 | 2324.0 → 917.2 ms | 60.5% | 281.3 → 101.7 | 16445 → 45 |

Measurements: [before](long-routes-before.txt), [after](long-routes-after.txt).
Logs omit repeated process headers; every benchmark row is retained unchanged.

Compact turn searches use node indices throughout and pack each incoming/current
pair into a uint64. Path queries store costs and predecessors in a pointer-free
table that grows with visited states. Cost-only queries retain distance maps.
A bounded, query-local cache reuses exact directed-road bearings. Spatial-index
iteration also reads compact coordinates directly; snapping is outside these
query timings.

A CPU profile then identified angular normalization as another hot path. Bearing
and turn-angle normalization now use bounded subtraction instead of general
modulo, preserving the original floating-point operation order. In a separate
paired comparison with all other changes already present, delivery CPU medians
fell by 17.4%, 12.9% and 4.8%, respectively. Car controls changed by at most 0.8%;
allocation counts were unchanged. [Angle baseline](angles-before.txt) and
[angle result](angles-after.txt) record that experiment. Reproduce this isolated
comparison by restoring only the two normalization formulas to `math.Mod`.

## Reproduction and correctness

```sh
GOMAXPROCS=1 OSMMINI_LONG_GRAPH=/path/to/niederbayern.graph \
  go test -run '^$' -bench '^BenchmarkLongRoutes$' -benchmem -benchtime=3x -count=3 ./core
```

Alternatively, `OSMMINI_LONG_PBF=/path/to/niederbayern.osm.pbf` imports a temporary
graph before timing. Endpoints farther than 1.5 km from the fixture fail the run.
For paired comparisons, compile each revision with `go test -c` and alternate
binaries per route/profile. Copy the three benchmark files named above into the
baseline checkout; they are not present in that revision.

All six routes have exactly equal costs, distance, duration and node paths before
and after the complete patch. [Saved fingerprints](long-route-results.jsonl)
include SHA-256 of the path's int64 IDs encoded as consecutive little-endian
uint64 values. Tests cover table growth and collisions, signed OSM IDs, equal-cost
predecessors, cost-only queries, cancellation, concurrent queries, legacy graphs,
ALT and territory-cost fallbacks. Angular tests compare the original formulas
bit for bit at turn thresholds, poles, the dateline and randomized coordinates.

Fixture-free regression benchmarks are `BenchmarkLongRouteGrid` (600 × 600),
`BenchmarkIndexedShortQueries`, `BenchmarkIndexedTurnSearch` and
`BenchmarkNodeSnapping`. Validation also includes `make check` and
`go test -race ./core ./mobile`.
