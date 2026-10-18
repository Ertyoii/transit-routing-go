# Static routing contract v0.2

Extends [v0.1](CONTRACT.md); old 0.1 synthetic fixtures remain readable. This profile supports stop-based endpoints and static scheduled or explicitly modeled events, not arbitrary GTFS capabilities.

## Walking and ranking

`Query.walking_limits` explicitly separates maximum contiguous walking blocks: `access_seconds` before the first boarding; `transfer_seconds` between boardings; `egress_seconds` after the last alighting; `direct_seconds` for an entirely walking journey. The total `max_walk_seconds`, maximum legs/boardings and time horizon remain independent feasibility resources. Missing `walking_limits` means the historical total-budget-only profile, never an implicit 600-second default.

Every workload must explicitly freeze its walking bounds. No numeric default is inferred from another application or treated as a universal passenger preference. Walking coverage is limited to the supplied graph.

While search does not yet know whether a walking block ends in another boarding or at the destination, it may expand up to the larger applicable limit. The eventual boarding/destination enforces its specific limit. This prevents incorrectly applying the 600-second egress limit to a valid 901-second intermediate walk. Metrics expose access, intermediate, egress and direct walking separately.

A configured **ranking penalty is presentation only**, added once per transfer to actual duration. It never modifies connection times, arrival, waiting or hard boarding feasibility. Raw earliest-arrival-order candidates, candidate cap and displayed k are distinct. Search caps and display counts must be recorded separately.

## Transfer rules

Static types **0, 2 and 3** are supported. Type 0 recommends a connection without an additional minimum; type 2 imposes its minimum elapsed time from the previous alighting event; type 3 prohibits it. The physical walking path and chronology still apply. An explicit rule overrides the query's fallback minimum, while walking consumes rather than adds to that minimum. Each query declares its fallback minimum. Timed transfers (1) and linked/in-seat transfers (4/5) are rejected; they are not approximated as ordinary transfers.

The [GTFS reference](https://gtfs.org/documentation/schedule/reference/#transferstxt) defines specificity by trip and route selectors. Implementation chooses the applicable maximum: both trips; one trip plus the opposite route; one trip; both routes; one route; stop pair. A route selector accompanying a trip on the same side does not increase specificity. Station rules apply to their child stops. Equally maximal matching rules are an error; overlapping lower-priority rules can be shadowed by a unique more-specific rule.

Normalized trip instances retain source trip ID and service date. Rules referring to a GTFS template trip apply to its instantiated runs. Prior trip, actual alighting stop/time and pending walking block remain available until the next boarding; traversing synthetic walking edges cannot erase a transfer prohibition. Staying aboard adjacent connections of the same instance bypasses boarding/transfer rules. Different trip IDs are never implicitly interlined.

## Import scope and clocks

`gtfs.Import` currently accepts the Asia/Singapore static profile. It expands calendars and exceptions across the neighboring service dates needed by an explicit event window, preserves >24h events, and scopes instance IDs by source trip/date/departure. Connections outside the supported query window are removed; queries extending beyond the window are rejected. Mid-trip onboard-origin requests are unsupported. The profile does not pretend to handle DST.

Frequency rows with absent/zero `exact_times` fail under the default policy. Explicit `deterministic_model` plus `modeled_snapshot` expands phase-zero departures at `start + n*headway`, end exclusive. Those events are invented model realizations, not published exact departures or reliability estimates. `exact_times=1` may expand schedule-based frequency compression. Frequency distinctions and row counts remain in the audit.

The optional `minimum_time_proxy` graph uses directed GTFS minimum transfer times as walking proxies. They may include buffer time and are tagged `inferred_gtfs_minimum_transfer_time_proxy`; unknown accessibility remains unknown. No straight-line or nearby-stop fallback is created. The stricter selected transfer rule is still checked at boarding. This proxy graph is an explicit modeling choice, not a recovered physical pedestrian network.

Unsupported input semantics fail visibly: timed/linked/block-based transfers, flex/booking/continuous service, interpolated blank event times, coordinated pickup/dropoff types, pathways and non-stop/station location types. Ignored metadata, shape and legacy-fare tables are listed in the audit; fares are outside the objective. Unknown nonempty tables fail. Import budgets fail without returning a partial snapshot. This importer is deliberately narrower than a complete GTFS implementation.

## Identity and finite graph-walk universe

Identity and total order remain v0.1's typed connection/footpath reference sequence and `(arrival, transfers, walking_seconds, leg_count, identity)`. Equal Pareto vectors retain all identities in the audit selector; paper representative-only tracks will compare vectors instead.

The historical finite graph-walk profile permits repeating a zero-duration scheduled connection within leg/boarding limits, as well as revisiting physical stops. This is an explicit compatibility limit: event-simple physical-journey semantics must be versioned and tested before comparing strict top-k identities with paper engines. This finite profile alone is not a physical-world correctness certificate.

Runtime label/journey budgets are separate from feasibility bounds. Exhaustion sets incomplete status and returns an error. An optional per-stop label cap is explicitly heuristic; any discard makes the result incomplete. Neither candidate display k nor an arbitrary per-stop cap proves top-k exactness.
