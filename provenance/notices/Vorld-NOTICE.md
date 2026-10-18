# Data attributions

This feed is derived from the following sources. The code in this repository is
MIT-licensed ([LICENSE](LICENSE)); the data below remains subject to its
providers' terms.

## Land Transport Authority (LTA)

Contains information from the Land Transport Authority accessed via
[LTA DataMall](https://datamall.lta.gov.sg) on 2026-07-06 (BusStops, BusRoutes,
BusServices APIs; Train Station and Train Station Exit geospatial datasets) and
from the LTA train network map pages, which is made available under the terms
of the [Singapore Open Data Licence v1.0](https://datamall.lta.gov.sg/content/datamall/en/SingaporeOpenDataLicence.html).

## SimplyGo / TransitLink

Station-to-station estimated travel times are factual data retrieved from the
public [SimplyGo Travel Guide](https://travelguide.simplygo.com.sg/MRT)
(SimplyGo Pte Ltd) on 2026-07-06/07. Used as calibration ground truth for rail
run times.

## SBS Transit

Sengkang and Punggol LRT adjacent-station travel times are transcribed from the
travel-time charts published by SBS Transit at
[sbstransit.com.sg/travel-time](https://www.sbstransit.com.sg/travel-time)
(transcribed 2026-07-07; per-row source URLs in
`data/raw/sbs-transit/lrt_travel_times.csv`).

## Community-measured interchange transfer times

Interchange walk times are vendored from a
[public community-maintained spreadsheet](https://docs.google.com/spreadsheets/d/1e-Tuf6rHBFsgsuFN7XqbFL8ec_vdRjQw)
of measured station transfer timings (retrieved 2026-07-06). Grateful
acknowledgement to its anonymous maintainers — this is careful field work.
The original workbook is preserved unmodified at
`data/raw/transfers/transfer_times.xlsx`.

## Disclaimer

This is an unofficial, community-built feed. It is not endorsed by, affiliated
with, or supported by the Land Transport Authority, SMRT, SBS Transit, SimplyGo
Pte Ltd, TransitLink, or any transport operator. Travel times are modeled
estimates for a typical weekday; do not rely on them for time-critical journeys.
