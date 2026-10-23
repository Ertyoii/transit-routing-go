# V2 non-CSA runtime integration

This is a new standalone-library composition over routing commit
`b102816018b1db87304c751f2cd601a2abe4057a`. It integrates compatible algorithm
source and tests from the [published V2 source reconstruction](https://github.com/Ertyoii/transit-routing-bench/tree/3eba733eb182ca06ce81a678fd23f6a2b4a492ca/results/sg-public-model-2026-10-22-v2/source).
The [integration source map](../provenance/v2-runtime-integration.json) records
public-relative source attribution and the new file identities. Earlier
checkpoint source maps are retained unchanged.

## Composition and compatibility

The V2 branches are alternatives with explicit parents, not a stack of all 42
patches. The common no-arena branch supplies dense readiness, indexed walking,
reusable graph closure sessions and compact Trip-Based links. The integration
then combines the compatible range-RAPTOR implementation; bounded native
preparation, compact T-REX and walking-prefix changes; the additive Trip-Based
answer-tie API; and the additive ULTRA local-tie/scalar-summary APIs. Duplicate
sibling files are included only once. The slab-allocation sibling is not enabled.
`paper.NodeArena` is an available utility, not a change to default search node
allocation.

- Existing `Search` APIs retain their declared arrival/boarding or multicriteria
  contracts. Internal dense state, scratch reuse and compact storage do not add
  input-size narrowing: native-width escape representations preserve exceptional
  indices and path identities. `ExactStorage` remains a separate opt-in setting.
- `raptor.Index.SearchLocalWalkTies`, ULTRA `SearchLocalWalkTies` and
  `tripbased.Index.SearchCappedWalkingAnswerTies` are explicitly named variants.
  They refine only the documented local/answer representative ties. They do not
  establish every walking-minimal representative, a 3D frontier or strict
  identity top-k.
- Range-RAPTOR retains its native ready-time-profile and supported-resource contract;
  scratch reuse does not turn that contract into exhaustive identity enumeration.
- Bounded walking preparation is explicit. Legacy unrestricted constructors are
  not silently capped, and destination/domain/completeness/resource guards still
  apply. Unsupported, incomplete, cancelled and budget-exhausted outcomes are
  not promoted to complete answers.
- `walking/graph.ReachSession` belongs to one immutable graph. Copies share its
  serialized scratch state; returned witnesses remain independently owned.
  Separate sessions do not share mutable search storage.
  Dense graph indexing and retained O(V) scratch per active Walker can increase
  memory for sparse closures on large graphs. Small-fixture allocation reductions
  are not evidence of lower full-model RSS or query latency.

CSA, EACSA and profile-CSA package source remains unchanged. There is no CSA
optimization or new CSA measurement in this integration.

## Opt-in selector policies

The standalone `recommend` package now includes the bounded/lean selector APIs
and the finite-safe A and hybrid-fill B experiments from the final V2 source
branch. New configuration fields default to absent, leaving the existing
`Evaluate` and `EvaluateChoices` paths unchanged. `validate.Checker` adds owned
model-view/fingerprint/transfer accessors so lean evaluation can use the same
private validated snapshot without duplicating recovery enumeration.

- `SelectBounded` selects from supplied finite candidate features under an
  explicitly configured bounded affine preference domain. Exact, heuristic,
  resource-hybrid, nested and distinct-pattern options keep their own stated
  guarantees and defined resource counters.
- `NewBoundedFromChecker` constructs the lean snapshot-sharing path. It does not
  enumerate missing routes. Lean serving requires its explicit delivery policy.
- `SelectFiniteSafe` and `EvaluateFiniteSafe` implement the separately
  named finite20-safe policy. Its fixed twenty hypothetical preferences,
  configured rectangle, requested size and bounded-state guards are part of the
  API's validation, not tunable promises of global optimality. Its candidate
  ceiling is 64 and its state ceiling is 200,000.
- `SelectHybridFill` and `EvaluateHybridFill` implement the separately
  named hybrid-fill policy. A successful fill contains its unchanged base set,
  preserving base identity order, and adds only supplied native candidates up to
  the requested three/five-item cardinality. Failure clears the filled selection.
  Inclusion does not establish finite20 dominance or a new globally optimal set.

All selector statements concern the supplied finite candidate pool and declared
hypothetical scoring model. They do not certify complete journey coverage,
authentic passenger utility, reliability, global strict top-k or lower latency.
Budget and cancellation failures remain visible; no precomputation is silently
moved outside a request to inherit old timing claims.

## Verification and evidence boundary

Fresh verification uses Go 1.27.3 on Linux and the standard-library-only module.
The verification record in the integration source map distinguishes standalone
build/unit/race/vet checks from independently maintained synthetic regression
checks run against this composition. Tests are bounded evidence, not bug freedom
or a full-network oracle. The historical two-day campaign is not rerun here.

This composition has a new source and executable identity. Old latency, memory,
quality and confidence-interval results remain bound to their original measured
branches; they do not automatically apply to this merged library. Compact
payload counts alone do not establish lower process RSS or faster queries.
Repository-hosted CI is not added by this update.

Only library code, synthetic tests, documentation and provenance are integrated.
Benchmark harnesses, real datasets/models/workloads, witness/index payloads,
private paths, binary caches and credentials are not part of the publication.
MIT licensing and retained third-party notices remain in force; source identity
conveys no rights to redistribute excluded real data.
