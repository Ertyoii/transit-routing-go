# Composed research checkpoint

This module composes reviewed stages after v0.1.0-pre.2. The benchmark project supplies aggregate results and portable synthetic reproductions. Real feeds, derived walking graphs, real query rosters and full-model itineraries are excluded.

## Implementation changes

- Dense RAPTOR/McRAPTOR round queues with frozen-reference and sparse-work regressions.
- Reusable immutable query validation and indexed static transfer-rule candidates, retaining rule order and specificity. Benchmark output still requires full itinerary validation.
- Shared ordinary capped Trip-Based indexing, query-local absorbing-destination repair and immutable transfer allocation.
- A capped ULTRA transfer-complete control and explicitly named stay-witness safe subset.
- FLASH static rules, suffix flags and walking-role caps; separately experimental retained exact-event profile options.
- Destination fixed-cap TP with visible readiness/profile guards and partial diagnostics. Incomplete construction cannot answer queries.
- T-REX compact reconstructible proofs and opt-in count-derived exact storage. Extra count/fill work is charged. Static-cap output requires original-query replay of every unrestricted representative plus complete bounded independent enumeration and 2D frontier agreement. Cap rejection, budget exhaustion and mismatch remain distinct outcomes.

## Unresolved coverage and resource limits

T-REX cap rejection does not establish that no feasible capped route exists. Invalid unrestricted winners can hide required feasible capped objectives before replay; filtering cannot recover missing alternatives. Other cases reject conservatively despite a feasible equal-objective representative. Six literal fixtures demonstrate these distinctions. The original full-model panel certified zero of 384 slots. No incorrect accepted route was observed in the bounded diagnosis; general cap-aware ranked customization remains open.

The ten-source TP sample is a planning study. Its source-local estimate is about nine hours with a wide approximate small-sample interval. Finalization and full-index memory feasibility remain unqualified. No full all-source construction or query-latency qualification follows from that estimate.

## Source and measurement boundaries

The source map records admitted file hashes and transformations. Namespace conversion and gofmt are mechanical. The merged cost summary retains FLASH/TP accounting while counting T-REX compact edges and proofs.

The composition combines separately measured stages. Clean-copy tests check this source combination; they do not remeasure the historical load matrix. Immutable Trip-Based transfer storage and retained FLASH profile options must not inherit byte-identical whole-package timing claims from earlier measured sources. The named FLASH benchmark adapter selects the fresh-profile configuration.

Original code/tests/docs retain MIT licensing and reference notices. Invented fixture data has a local CC0 dedication. Papers and C++ reference implementations are identified but not bundled or relicensed. No test suite guarantees bug freedom.

## Portable checks

```sh
GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off GOMAXPROCS=2 go test ./...
GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off GOMAXPROCS=2 go test -race ./...
GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off GOMAXPROCS=2 go vet ./...
GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off GOMAXPROCS=2 go test -race ./tripbased -run '^TestTREXCap(Certification|Kernel)' -count=1
```

The final command exercises literal hidden alternatives, equal-objective rejection, full-witness replay, independent bounded enumeration and distinct ranked/enumeration/projection budget failures. See the fixture rationale in tripbased/testdata/trex_cap_certification.

The benchmark project provides scripts/reproduce-checkpoint.sh using its vendored routing lock. Its tiny synthetic smoke run does not recreate unpublished full-model inputs or establish SLA performance.
