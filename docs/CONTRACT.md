# Routing contract v0.1 — synthetic finite profile

Historical profile, still supported. See [v0.2](CONTRACT_0.2.md) for implemented GTFS static rules and separated walking resources.

This is the foundational finite synthetic harness contract. Code implements this finite static profile only. A finite oracle reference is exact **within this declared journey universe**, never proof of unrestricted network optimality.

## Data and clock

The dataset is an explicit normalized single-service-date snapshot with a fixed IANA timezone. Fixtures freeze service date `2026-10-17`, timezone `Asia/Singapore`, and integer service seconds after local service midnight. Times can exceed 86,400; `25:10:00` means service-second 90,600 on the same service date. The parser accepts at most seven service days of seconds. No wall-clock `now`, local timezone, implicit DST conversion or current feed updates affect a query. This Singapore-only profile has no DST; a future multi-day/GTFS adapter must expand calendars, exceptions, overlapping service-day trip instances and DST using the source specification, independently tested. The current schema is not a GTFS parser.

Every connection is one consecutive pair of stop events with a unique ID, trip-instance ID, contiguous zero-based sequence, departure/arrival, pickup and dropoff booleans. Trip instances are scoped to this dataset's explicit service date; future multi-date IDs must include the service date. A trip's stop chain is continuous and nondecreasing in time. Stops, trips and directed walking edges have stable unique IDs. Input ordering has no semantic meaning. Cancellation, realtime uncertainty, frequency-based service and through-service across different trip IDs are unsupported.

Walking edges have strictly positive integer durations, explicit direction and accessibility. No implicit reverse edge or transitive closure is assumed. Same-stop transfers need no artificial edge. Walking counts toward a total seconds budget; the query also bounds total legs, boardings and latest arrival. All constraints define feasible journeys, not hidden approximation knobs. Zero boarding/walking budgets are valid. Finite leg limits also bound zero-duration transit cycles. Runtime enumeration budgets are separate and cannot silently change the feasible universe.

## Journey and transfers

A journey is a sequence of exact connection references or walking-edge references, with explicit departure/arrival seconds for each leg. Waiting is implicit before boarding. Walks depart immediately at the current arrival; arbitrary additional waiting before walking is canonically removed. This static profile assumes no time-dependent walking restrictions or maximum waiting constraint, so such waiting is unnecessary. Transit legs use the timetable's exact times. The first board requires no transfer buffer. Each later board requires departure >= both the current time and last alighting time + query transfer buffer; walking consumes that buffer rather than adding another buffer after the walk.

Consecutive adjacent connections on the same trip instance mean staying on the vehicle. They require neither pickup/dropoff permission at the intermediate stop nor a transfer buffer, and count as one boarding. Alighting/reboarding the same adjacent vehicle is not a distinct journey. Any other transit transition is a new boarding and requires alighting permission on the prior ride, pickup at the new ride and the transfer buffer. Walking after a ride requires alighting. The final ride requires dropoff permission. Reboarding a later nonadjacent event of the same trip counts as another boarding. Different trip instances always count separately; block/interlining semantics await a future profile.

Transfer count is max(boardings − 1, 0); walks are not transfers. Journey metrics are arrival, boardings, transfers, walking seconds and leg count, reconstructed from the timetable. The origin and destination are stops. A journey terminates at its first destination event at which it can legally alight; it never leaves that eligible destination again. Origin=destination has only the empty journey at query departure. Cycles elsewhere remain legal within the public bounds, so the oracle does not apply an undocumented simple-path filter.

Accessibility has three values: `yes`, `no`, `unknown`. With `accessible_only`, origin/destination and boarding/alighting stops, boarded trip instances and walking paths/endpoints must explicitly be `yes`; unknown is excluded. Staying onboard may pass an inaccessible stop. This is a conservative data filter, **not a claim of verified step-free travel**: elevators, pathways, platform edges, outages, assistance and personal mobility constraints are not modeled.

## Identity, ties and independent verification

Identity is the JSON encoding of the ordered typed references (`ride` connection IDs / `walk` edge IDs). A delimiter inside an ID cannot cause a collision. Distinct trip instances at equal times remain distinct. Waiting is not identity. The same journey under different query IDs has the same identity. Deterministic total order is `(arrival, transfers, walking_seconds, leg_count, identity_lexicographic)`, ascending. The exact byte order of identity is part of v0.1; input/storage order is not a tie breaker. Identity, metrics and times emitted by a router are treated as untrusted claims.

The `validate` package walks an output itinerary from first principles using only the static input and queries. It checks every reference, time, endpoint, continuity, wait/boarding/alighting rule, resource budget, accessibility event, destination termination and reconstructed metric. It does not call the oracle, router search, pruning or label code. The oracle separately scans all connections and footpaths at every expansion. They share data types and input validation, but no feasibility-transition helper. Selection uses only validated metrics; hand-authored expected values guard against shared representation mistakes.

## Four separate result contracts

| Mode | Exact result requirement |
|---|---|
| `earliest_arrival` | One feasible journey minimizing arrival; secondary total-order fields deterministically choose among ties. Empty only when no feasible journey exists. |
| `pareto` | Every feasible journey whose explicitly declared criteria vector is nondominated. Supported criteria are arrival plus one or more of boardings, transfers and walking seconds. The smoke runner uses `(arrival, boardings)`. Equal vectors do not dominate one another; all distinct identities are retained. Sorted by the total order. |
| `strict_top_k` | Exactly the first min(k, number of feasible distinct journeys) under the total order; positive k. Requires a completeness argument or complete oracle universe. |
| `bounded_candidates` | At most a positive cap of unique feasible journeys supplied by a router; optional presentation ordering. No completeness, top-k or Pareto guarantee. Any truncation/cap and raw results must be retained. |

`selection` rejects duplicate identities and invalid K/cap. Exact modes require an explicitly complete universe in the foundational path; the future algorithm adapter contract must provide a separate optimality/correctness certificate/status rather than requiring materialization of every feasible journey. `bounded_candidates` always reports `exact=false`, even if the input happens to be complete. An empty incomplete candidate set is not evidence of unreachability.

A label cap, search timeout, state budget or heuristic prune is never automatically strict top-k. Enumeration limits return a typed truncation error and an incomplete status; the CLI refuses to produce successful exact results. Query feasibility bounds and computational budgets are separately serialized. The batch contract is a deterministic map from query IDs to the same per-query results as singleton execution, unaffected by input permutation, partition size or unrelated queries. Results are keyed by unique query ID; implementations must document any shared preprocessing, cache or scheduling effects separately.

Paper baselines may return one representative per nondominated vector. Compare vector completeness in that track; this fixture selector retains all equal-vector identities for auditing and does not impose that stronger identity requirement on a paper that promises only representatives.
