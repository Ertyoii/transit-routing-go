# Transit routing in Go

Experimental public-transport routing kernels, explicit journey contracts and replaceable directed walking providers. Original code is [MIT licensed](LICENSE); third-party notices and data exclusions are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). This is research software, not a live journey-planning service or a complete GTFS implementation.

Requires Go 1.27.x. Runtime dependencies are the Go standard library.

```sh
GOTOOLCHAIN=local GOMAXPROCS=4 go test ./...
GOTOOLCHAIN=local GOMAXPROCS=4 go test -race ./...
GOTOOLCHAIN=local GOMAXPROCS=4 go vet ./...
```

The separate [benchmark project](https://github.com/ertyoii/transit-routing-bench) owns exhaustive tiny-instance references, independent regression checks, source binding and measurement tools. Passing tests cannot guarantee bug freedom.

## Packages and contracts

| Packages | Role |
|---|---|
| `model`, `gtfs`, `transfer`, `validate` | Finite timetable/query/journey contract, bounded import, static transfer rules and full-itinerary verification |
| `csa`, `eacsa`, `profilecsa` | Local connection-scan research implementations and distinct exact/heuristic modes |
| `raptor`, `profilequery` | Round-based, multicriteria and profile configurations; optional Early Pruning |
| `tripbased` | Trip-Based and constrained FLASH, transfer-pattern, scalable and T-REX implementations |
| `tad`, `corech`, `ultra`, `paper` | Transfer-aware search, accelerators and declared production preprocessing |
| `comparison`, `presentation`, `selection`, `recommend` | Result binding and explicit finite-candidate ranking |
| `walking` | Snapshot-bound witnessed paths, duration/batch/matrix APIs, caching and adapters |

Earliest arrival, Pareto objective vectors, strict ordered identity top-k and bounded candidate presentation are separate contracts. A label-beam limit is heuristic, not strict k-shortest enumeration. Grouping independent OD queries into a batch is a different operation from batching candidate stop pairs inside one passenger request. The local controls do not reproduce any external production implementation or establish a performance comparison with one.

Read the [finite identity contract](docs/CONTRACT.md), [static GTFS/resource extension](docs/CONTRACT_0.2.md), [walking API](docs/WALKING.md), [capability scope](docs/SCOPE.md) and [sources](provenance/SOURCES.md). Unsupported semantics and exhausted budgets must remain visible. Exactness applies only to an explicitly supported finite input model.

## V2 runtime integration

The [V2 integration notes](docs/V2_RUNTIME_INTEGRATION.md) describe the latest
compatible non-CSA source composition: reusable walking closure sessions, dense
RAPTOR/ULTRA state, range-RAPTOR scratch reuse, compact Trip-Based/T-REX storage,
bounded native preparation and separately named walking-tie and finite-candidate
selector variants. Default contracts remain separate from these opt-in policies.
This is a newly tested library integration, not the byte-identical binary used
for the benchmark campaign and not a new full-network timing or quality result.

## Walking integration

`walking/graph` provides immutable directed paths with source witnesses. `walking/transit.FromDataset` preserves normalized footpath IDs and costs. `walking/osm` loads an already audited, hash-pinned graph; it does not turn arbitrary OSM data into a verified pedestrian network.

External providers implement `walking.Provider` and may additionally advertise duration, batch or matrix capability. `transit.Acquire` obtains a bounded catalog before router preparation. `Catalog.ApplyLayer` replaces an explicit edge layer while retaining other walking and transfer rules. No network call is inserted into a routing kernel. The acquired finite graph must not be called a complete global walking network without supporting evidence.

For local API development with your own normalized dataset:

```sh
go run ./cmd/walkserve -dataset /path/to/dataset.json
curl http://127.0.0.1:8080/v1/snapshot
```

Use the returned snapshot ID with `/v1/route`, `/v1/duration`, `/v1/batch` or `/v1/matrix`. The service defaults to localhost and provides no authentication; no service starts automatically.

Read the [checkpoint changes](docs/RESEARCH_CHECKPOINT.md) and [capability limits](docs/SCOPE.md), especially unresolved T-REX candidate coverage and TP full-preparation limits. The benchmark project carries aggregate historical results. Source datasets and real query/itinerary payloads are excluded; the final composition is not a new full-model timing run.
