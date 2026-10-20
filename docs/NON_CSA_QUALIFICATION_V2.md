# Non-CSA qualification API contracts

These separately named experimental APIs preserve the supplied static model and
the original query's supported semantics. They return unranked 2D
`(arrival, boardings)` representatives, not strict identity top-k, every tied
itinerary or a 3D walking Pareto frontier. No new full-model quality, preparation,
memory, query-latency or confidence-interval result is included here. Repository
CI/workflow coverage remains unmet. Bounded source tests and per-query gates do
not establish bug freedom or independent full-network optimality.

## Retained origin-domain Transfer Patterns

```go
type DestinationSourceDomainConfig struct {
    Retained DestinationRetainedCappedConfig
    Sources  []string
}
func BuildDestinationSourceDomainPatterns(
    context.Context, model.Dataset, DestinationSourceDomainConfig,
) (*DestinationSourceDomainIndex, error)
```

The returned index exposes `DomainComplete()`, `GlobalComplete()`, `Snapshot()`,
`CostSummary()` and `Search(ctx, query) (PatternResult, error)`. It deliberately
has no generic `Complete()` alias. `Sources` is an explicit nonempty list of
distinct existing non-destination origins; it is copied and canonically sorted.
The retained config's global `SourceOrder` override must be empty.

Every declared origin must finish every exact feasible access-readiness group
over the full supplied service window and its lower sentinel, with propagation,
emission and a drained queue. Complete ordinary line incidence and the final
context check are required before domain completion. The entire supplied
stop/trip/connection network is retained. The established absorbing-destination
conditioning removes outgoing destination walks before geometry; source-domain
selection does not trim the transit network, later trips or walking detours.

`DomainComplete()` is a separate certificate for that declared workload origin
domain. Both legacy wrapper/inner global completion bits remain false, even for
a large declared domain. Legacy incomplete `Search` behavior is unchanged. The
new query path uses an unexported owner-bound permit; it neither flips a global
bit nor exposes a general incomplete-index bypass. Failed or cancelled
construction is unanswerable, including a useful completed source prefix.

Queries must retain the prepared destination and all four walking-role caps,
use a declared origin, zero transfer buffer, no accessibility request and no
more than the prepared boarding bound. Their original departure/latest-arrival
must lie within the prepared service window. Aggregate walking and leg resources
must pass the unchanged nonbinding capability checks. Unsupported requests
decline before shortcuts; their destination/window/caps/resources are not
rewritten. Queries search the prepared graph and full incidence, with no fresh
ordinary-TB fallback. Compact owned evidence exposes domain/global completion,
readiness/sentinel completion and finalization without cloning the full graph.

## Fixed-transfer-cap ranked T-REX

```go
func BuildTREXFixedTransferCapRankedCandidate(
    context.Context, model.Dataset, TREXConfig, int64,
) (*TREXIndex, error)
func PrepareTREXFixedTransferCapCandidate(
    context.Context, *TREXIndex, TREXStaticCapVerifyLimits,
    TREXRawVerifierPreparationLimits,
) (*TREXFixedTransferCapCandidate, TREXRawVerifierPreparationStats, error)
```

The final constructor argument fixes a nonnegative transfer-walking cap before
ranked customization. The current qualification candidate uses 900 seconds;
another cap is another explicit preparation and qualification scope. Every
query must match its prepared transfer cap exactly. Original access, egress,
direct-walking, aggregate/leg, boarding, pickup/dropoff, static transfer-rule,
staying-aboard, absorbing-destination and identity constraints remain in force.
The existing static type-2/type-3 stop-pair rule profile and supported resource
capabilities still apply. No cap is silently relaxed to recover an answer.

The prepared object exposes `Search(ctx, query)
(TREXFixedTransferCapCandidateResult, error)`. Each call performs genuine ranked
search, replays every raw ranked witness against the original query and model,
and obtains a fresh complete independent raw enumeration and equal 2D frontier.
The verifier owns literal raw rows/input order and indexes without router
closure, transfers or rank data. It supplies no returned journey. Acceptance
requires `Complete`, `Certificate.Accepted` and `VerificationComplete` together.
Mismatch, unsupported semantics, cancellation or exhaustion clears output and
declines; no ordinary-TB or verifier-output fallback is returned as T-REX.

Per-query equality is a finite acceptance gate, not a completeness theorem for
ranked customization or a claim of general cap-aware rank preservation. The
unchanged unrestricted legacy ranked/full-scan certificate control remains
separately identifiable. A rejected candidate cannot certify unreachable input.

## Defined verification work and shadow reporting

`TREXRawVerifierPreparationLimits` separately requires positive `MaxRows`,
`MaxWork` and `MaxPayloadBytes`. Returned statistics identify row/work counts,
an ordered raw-input digest and defined UTF-8-string/integer/boolean payload
bytes. Payload excludes Go slice/map/allocator overhead. Physical live heap,
process peak memory and preparation time require separate measurement.

For this candidate, `TREXStaticCapVerifyLimits.MaxScans` bounds the defined
candidate-row, rule-row and metadata/index-lookup probes, including misses,
together with the existing charged frontier projection work. Enumeration
states and retained journeys have separate positive bounds. Ranked search and
whole-witness replay charge the ranked work budget. Native method/policy strings
containing `actual_probe` are opaque identifiers for these particular units.

Excluded work includes repeated onboard/prior/lastRide raw ordinal accesses,
scalar comparisons/reuse, hash-byte processing, allocator operations and
leg-copy work. Copied-leg counters are separate; copying is constrained by the
state/journey and original query-leg bounds rather than charged as probes.
This meter does not count every raw-memory access or CPU instruction. Its
budgets and cancellation boundaries are not equivalent to the legacy logical
full-scan meter or its historical four-million limit.

Shadow legacy scans are diagnostic. Checked arithmetic stores the mathematical
total saturated at MaxInt64, with a sticky saturation flag only when the total
is strictly greater. `WouldDecline` includes saturation, even at a MaxInt64
numerical ceiling. `LegacyComplete` requires full declared enumeration and
projection scope and an unsaturated exact total. Partial or saturated values
are lower bounds, never exact complete legacy counts. Shadow diagnostics do
not change the numerical actual-probe/state budgets, ranked/replay/frontier
gates, query caps or acceptance policy.

## Source identity and pending evaluation

`provenance/checkpoint-source-map.json` binds accepted clean project source over
the published baseline; `source-pins.json` records its source/patch manifests.
Artifact measurement drivers/observers and their observation calls are excluded.
An instrumented measurement binary has a distinct identity from the clean
public source. No byte-identical public-binary timing result is inherited.

The benchmark's vendor lock identifies an exact local routing source commit;
`v0.1.0-pre.3` remains an unreleased supported vendor pin, with no remote tag or
release implied. Final composed native checks, public result review and any
future performance/quality claim remain separate gates. Unofficial frequency
models, approximate walking and unverified physical accessibility retain their
existing limitations. Source hashes confer no dataset redistribution rights.
