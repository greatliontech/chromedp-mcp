# `get_network_requests` projection allocates per call

## Summary

Two performance items surfaced during issue 009's review. Both are
correct-by-design today; the question is throughput on the
many-entries-per-call path that `fields` was added to address.

### A. `networkEntryFieldSet()` recomputed per call

`internal/tools/network.go::networkEntryFieldSet()` builds the set of
valid JSON keys by marshaling a probe `NetworkEntry` and unmarshaling
into a map. This runs every time `get_network_requests` is called with
a non-empty `fields` slice. Pure and deterministic — no reason to
recompute. Cache as a package-level `var` (or `sync.OnceValue`)
initialized at package init so a successful build proves the probe
serialization works.

### B. `projectEntries` marshal+unmarshal per entry

`projectEntries` serializes each `NetworkEntry` to JSON and unmarshals
into `map[string]any`, then deletes keys not in the requested set. For
1000 entries × 3 requested fields, that's 1000 marshal+unmarshal
cycles to drop most of the work. Acceptable for the common case (small
N), but the pathological case is exactly the case `fields` was added
to address.

Faster path: build a `map[string]reflect.StructField` keyed by JSON tag
once (alongside the field-set cache), then for each entry walk only the
requested tags via reflection. Or precompute small per-field
extractor closures.

## Why filed not fixed

Both are pure perf wins, no correctness or contract change. The shape
get_network_requests caller sees is identical with or without these
optimizations. Filing rather than blocking issue 009 on micro-perf.

## Re-open trigger

If a real workload hits get_network_requests with thousands of entries
projected to a few fields and wall-clock latency becomes user-visible,
do A first (trivial), then measure B before reflecting.
