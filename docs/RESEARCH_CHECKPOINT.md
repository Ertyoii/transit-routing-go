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
- A distinct retained exact-event TP source-domain API, with complete declared-origin readiness/full incidence and separate domain/global completion contracts.
- A fixed-transfer-cap ranked T-REX candidate with separate raw-verifier preparation, original-query all-witness replay and fresh complete per-query frontier equality. The current qualification candidate fixes transfer walking at 900 seconds. Its defined-probe policy and checked saturating shadow diagnostics remain separate from the unchanged legacy full-scan control.

## New API qualification boundary

See [NON_CSA_QUALIFICATION_V2.md](NON_CSA_QUALIFICATION_V2.md) for exact APIs, capabilities and work units. The origin-domain TP constructor uses the entire supplied network and full service window for every declared source, then completes ordinary incidence. Its separate `DomainComplete()` certificate does not set either legacy global completeness bit. Queries retain their original scope and resources; unsupported or incomplete domains cannot serve.

Fixed-cap T-REX customizes the ranked transfer graph under its prepared cap. The online certificate returns only actual ranked journeys and requires every original-query witness to replay plus a fresh complete independent arrival/boarding frontier match. This gate is not a theorem that customization preserves every capped optimum. The native `actual_probe` names identify a defined meter that excludes repeated ordinal accesses, scalar/hash/allocator/copy work; they do not count all raw-memory reads. Checked shadow saturation and partial-scope reporting yield lower bounds, not exact complete legacy counts.

No new full-model quality, preparation/memory, query-latency or interval result is published for these APIs here. Final composed native checks and result review remain separate gates; repository CI/workflow coverage remains an unmet item. Historical results below retain their original source and experiment scope.

## Unresolved coverage and resource limits

T-REX cap rejection does not establish that no feasible capped route exists. Invalid unrestricted winners can hide required feasible capped objectives before replay; filtering cannot recover missing alternatives. Other cases reject conservatively despite a feasible equal-objective representative. Six literal fixtures demonstrate these distinctions. The original full-model panel certified zero of 384 slots. No incorrect accepted route was observed in the bounded diagnosis; general cap-aware ranked customization remains open.

The ten-source TP sample is a planning study. Its source-local estimate is about nine hours with a wide approximate small-sample interval. Finalization and full-index memory feasibility remain unqualified. No full all-source construction or query-latency qualification follows from that estimate.

## Source and measurement boundaries

The current source map records exact accepted non-CSA source/test/fixture deltas from published routing `89c5217d6d317b43d0060c641802cb154f75d404`. The previous map is retained byte-for-byte as `provenance/checkpoint-source-map-v5.json`, preserving the earlier namespace/gofmt and source attribution history. The new runtime code is copied byte-for-byte from accepted clean source units; artifact-only observers, driver tests and their production observation calls are excluded. The merged cost summary retains FLASH/TP accounting while counting T-REX compact edges and proofs.

The composition combines separately measured stages. Clean-copy tests check this source combination; they do not remeasure the historical load matrix. Immutable Trip-Based transfer storage and retained FLASH profile options must not inherit byte-identical whole-package timing claims from earlier measured sources. The named FLASH benchmark adapter selects the fresh-profile configuration.

Measurement binaries containing artifact-only observation hooks have their own source identity. The public code contains no such hooks. The recorded clean-source transformation does not imply a byte-identical public binary or an inherited timing result. Source hashes and local vendor pins identify artifacts; they do not authenticate a remote release. `v0.1.0-pre.3` remains an unreleased supported vendor pin, with no tag implied.

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
