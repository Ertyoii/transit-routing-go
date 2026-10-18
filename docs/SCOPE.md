# Implementation scope

Algorithm names describe implementations and adaptations, not universal feature support or verified equivalence to a reference executable.

- CSA research controls distinguish strict bounded identity-k, cap-stratified results and explicitly incomplete heuristic label caps. Candidate k and display k are separate. Shared scanning over independent requests is not a production candidate-station-selection pipeline.
- RAPTOR and McRAPTOR return representatives of their declared nondominated objective vectors, rather than every equal-vector identity. Optional Early Pruning is default-off and supports 2D arrival/boardings and 3D arrival/boardings/walking. It prunes duration-ordered walking closure candidates; it does not prune the street-graph Dijkstra itself or establish strict identity top-k.
- The paper profile requires positive event durations, static stop-pair transfer rules, explicit walking role limits, a nonbinding total-walking resource and a certified sufficient leg budget. Trip/route-specific transfer state needs a different supported implementation.
- Ordinary Trip-Based and accelerated variants retain narrower assumptions. Restricted pickups/dropoffs, arbitrary transfer rules and finite walking caps can invalidate their preparation contract. Keep rejection reasons instead of weakening input semantics.
- `BuildBoundedWalkingTB` is a separate destination-conditioned, unranked 2D construction. Its cache key includes the dataset, destination, walking ceiling and preparation configuration. It rejects restrictions outside its profile. It is not ranked T-REX, a 3D walking frontier or a multi-destination shared search.
- T-REX, FLASH, transfer patterns and scalable transfer patterns are bounded static adaptations. Their optional reductions, partitioning and update behavior do not automatically match native reference engines. Core-CH is a transfer accelerator, not a complete timetable router. ULTRA's preprocessing and query scope must be evaluated independently.
- Walking accessibility conservatively requires explicit permission; unknown is excluded when requested. Stop/vehicle accessibility is not a verified step-free travel guarantee. Fares, disruptions, crowding, paid areas and physical station navigation need additional data and contracts.

These variants are not all independent algorithm families. Tiny exhaustive tests, independent witness checks, differential tests, mutations, fuzzing and race checks provide bounded evidence. No suite proves bug freedom, and cross-implementation agreement alone is not a full-network oracle.
