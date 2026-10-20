# Research sources and implementation boundaries

The code is an original Go research implementation informed by primary papers
and selected reference source inspection. No downloaded paper, figure, C++ source
or native reference executable is included. Runtime equivalence to those native
implementations is not claimed. Adaptations and capability restrictions remain
part of the public API and evaluation protocol.

Core references include:

- Connection Scan Algorithm: https://arxiv.org/abs/1703.05997
- RAPTOR: https://www.microsoft.com/en-us/research/publication/round-based-public-transit-routing/
- Trip-Based Public Transit Routing: https://arxiv.org/abs/1504.07149
- ULTRA: https://arxiv.org/abs/1906.04832
- Early Pruning: https://arxiv.org/abs/2603.12592v1
- T-REX: https://drops.dagstuhl.de/entities/document/10.4230/LIPIcs.ESA.2026.9
- GTFS Schedule semantics: https://gtfs.org/documentation/schedule/reference/

`source-pins.json` retains recorded public URLs and file hashes for inspected
research sources, including transfer patterns, scalable transfer patterns,
FLASH, transfer-aware Dijkstra and Core-CH. Hashes identify the inspected copies;
these copies are not distributed, and paper distribution rights are not inferred
from algorithm citations. No external implementation source was copied into the
Go runtime. Retained MIT reference notices are in `notices/`.

The non-CSA qualification additions are original project Go source. The current
`checkpoint-source-map.json` maps every changed implementation/test/synthetic
fixture file from published routing `89c5217d6d317b43d0060c641802cb154f75d404`
to its accepted clean TP or T-REX source unit and exact byte hash. The earlier
source map is retained unchanged as `checkpoint-source-map-v5.json` so prior
reference attribution and mechanical transformations remain inspectable.
`source-pins.json` separately identifies the published baseline and the accepted
local source/patch manifests. Local artifact paths and hashes are provenance
records; they are not fabricated remote URLs, bundled raw experiment evidence
or promises that unpublished inputs can be downloaded. No new external source
was fetched for these additions.

The clean public composition excludes measurement drivers, TP/T-REX artifact
observers and their production observation calls. It includes no private CSA,
screenshot-derived implementation, real-data query roster or measured raw
itinerary output. Synthetic preprocessing/proof/rank-loss fixtures retain their
CC0 notices; project source/tests/docs retain MIT. Every public CSA family source
file remains byte-identical to the published baseline.

Original project code uses MIT. Reference and source-data terms remain separate;
see `../THIRD_PARTY_NOTICES.md`. This routing module includes source and synthetic
fixtures. Full-model inputs and raw itinerary outputs are excluded; benchmark
aggregate reports retain their separate measurement identities. No equivalence
to an external production system is claimed.
