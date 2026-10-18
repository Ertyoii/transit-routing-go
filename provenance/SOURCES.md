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

Original project code uses MIT. Reference and source-data terms remain separate;
see `../THIRD_PARTY_NOTICES.md`. The repositories contain no measured real-data
performance report and make no parity claim about external production systems.
