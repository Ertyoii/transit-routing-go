# Supplied-pool recommendation policies

This package validates and compares a caller-supplied, finite set of complete
journeys. It does not find journeys, certify complete search, establish global
journey top-k, infer a rider's utility, or guarantee production request latency.
An output limit is a cap, not a guarantee of exactly that many selected journeys,
except for the explicitly named hybrid-fill policy below.

## Defaults are unchanged

`New(dataset).Evaluate(query, journeys)` still emits the
`pattern_anchors_v1` features, recovery diagnostics, and six envelope controls
(limits 3 and 5 at envelopes 120, 300 and 600).

`EvaluateChoices(ctx, query, journeys, config)` with only the original
`MaxCandidates`, `MaxSubsets` and `Groups` fields still emits the legacy features
and controls, plus finite explicit-preference minimax selections at limits 3 and
5. No new selector is implicitly enabled. Legacy minimax optimality is relative
to the supplied candidate score vectors and the explicit preference group.

## Explicit alternatives

- `LeanDelivery: true` with `DeliveryPolicy: "finite_preferences_v2"` preserves
  the requested finite-grid minimax output but omits recovery and unrequested
  controls. `"pattern_anchors_v1"` instead requests envelope-300 pattern anchors.
  `DeliveryLimits` defaults to `[3]` on this legacy lean path.
- A non-nil `Bounded` configuration requests continuous hypothetical-weight
  regret selection. Only its explicitly listed `Limits` are returned. The
  objective is `"absolute"` or `"normalized"`; normalized regret divides by
  `1 + minimum supplied-pool score`. `Exact` enumerates feasible subsets;
  `ExactWhenFeasible` chooses exact search using a conservative resource preflight
  and otherwise uses deterministic greedy/best-swap local search. Only completed
  exact selections set `SubsetOptimal`. Pattern and nesting constraints are
  explicit and narrow the guarantee accordingly.
- `FiniteSafe` with outer `LeanDelivery: true` and
  `DeliveryPolicy: "finite20_safe_v1"` requests the fixed limit-3 safe policy.
  It computes the exact historical twenty-hypothesis finite baseline, then
  exhaustively selects a subset of at most three original identities that is
  pointwise nonworse on those hypotheses, optimizing continuous-domain regret
  under that constraint. The fixed domain is transfer weight `[0,900]` and extra
  walk weight `[0,4]`. It does not promise non-regression against another policy
  or outside those twenty constraints.
- `HybridFill` with outer `LeanDelivery: true` and
  `DeliveryPolicy: "bounded_absolute_hybrid_fill_v1"` preserves its unchanged
  absolute resource-hybrid base as the output prefix and deterministically fills
  to `min(requested limit, native candidate count)`. The limit is 3 or 5. Filling
  runs finite20, pattern-anchor, then default-order priority streams only if
  needed. Set inclusion prevents the minimum affine score worsening versus its
  own base at any weight. It does not claim finite20 dominance or filled-subset
  optimality; the emitted regret values remain base-derived upper bounds.

Fixed safe/fill policies deliberately validate the exact named configuration,
explicit twenty coefficients, domain and guard values. Read their configuration
`Validate` methods before constructing them. Outer requested limits and guards
must agree with the nested configuration. Mixed/recursive configurations fail.

## Shared immutable validation snapshot

For a lean request, create a `validate.Checker` once and call
`NewBoundedFromChecker(checker)`. Despite its historical name, this constructor
supports all the explicit alternatives above, but rejects legacy recovery
`Evaluate`. It shares the checker's owned, cloned dataset snapshot and builds no
second recovery index. The checker exposes only its fingerprint and value-copy
connection/trip lookups; its independent transfer decision is metadata, not a
replacement for full witness validation. Safe and hybrid-fill request adapters
require this shared-checker constructor.

`SelectBounded`, `SelectFiniteSafe` and `SelectHybridFill` are lower-level replay
APIs. Their caller must supply already-validated features. They check identity
uniqueness and score bounds, but cannot authenticate a feature against a journey
or dataset. Use `EvaluateChoices` to validate complete journey witnesses first.

## Budgets, cancellation and interpretation

Inspect both the error and the policy result's `Complete` flag. A budget or
cancellation error is not permission to serve a partial answer or to silently
fall back to a diagnostic baseline/base. Candidate, subset, critical-point,
work and matrix guards constrain the relevant policy. Matrix limits account for
score-matrix entries, not total process RSS; work counters are deterministic
algorithmic guards, not wall-clock or CPU guarantees. Candidate guards count
unique validated identities, not repeated copies in the input slice.

Continuous geometry uses IEEE-754 arithmetic, with score tolerances `1e-8`
and normalized tolerances `1e-12`. These are tested numerical guarantees over
the declared finite pool/domain, not a formal exact-real-arithmetic proof.
`GlobalOptimalityClaim` is never enabled by these policies. The source campaign's
benchmark-specific adapters, datasets, matrices and oracles are not required by
this standalone package and are not included here.
