package capverify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"

	"github.com/ertyoii/transit-routing-go/model"
)

const ActualProbePolicy = "independent_raw_input_order_actual_probes_v1"

// Preparation limits cover row visits and a defined raw payload count, not Go
// allocator/map overhead. Physical retained/peak memory is measured separately.
type PreparationLimits struct {
	MaxRows         int   `json:"max_rows"`
	MaxWork         int   `json:"max_work"`
	MaxPayloadBytes int64 `json:"max_payload_bytes"`
}
type PreparationStats struct {
	Rows          int    `json:"rows"`
	Work          int    `json:"work"`
	PayloadBytes  int64  `json:"payload_bytes"`
	OrderedSHA256 string `json:"ordered_sha256"`
}
type rawStop struct {
	row     model.Stop
	station string
}
type rawTrip struct {
	row    model.Trip
	source string
}

// Prepared owns raw rows and ordinals, never router closure/transfer/rank data.
// Its internal caller supplies an already model-validated original Dataset.
// Complete construction is the only way to obtain a usable object.
type Prepared struct {
	d     model.Dataset
	stops map[string]rawStop
	trips map[string]rawTrip
	rides map[string][]int
	walks map[string][]int
	rules map[string][]int
	query *model.QueryValidator
	stats PreparationStats
}

func (p *Prepared) Stats() PreparationStats {
	if p == nil {
		return PreparationStats{}
	}
	return p.stats
}

func PrepareRaw(ctx context.Context, d model.Dataset, limits PreparationLimits) (*Prepared, PreparationStats, error) {
	var stats PreparationStats
	if err := ctx.Err(); err != nil {
		return nil, stats, err
	}
	if limits.MaxRows <= 0 || limits.MaxWork <= 0 || limits.MaxPayloadBytes <= 0 {
		return nil, stats, fmt.Errorf("raw verifier preparation limits must be positive")
	}
	rows := len(d.Stops) + len(d.Trips) + len(d.Connections) + len(d.Footpaths) + len(d.Transfers)
	if rows > limits.MaxRows {
		return nil, stats, &TruncatedError{"preparation rows"}
	}
	p := &Prepared{stops: map[string]rawStop{}, trips: map[string]rawTrip{}, rides: map[string][]int{}, walks: map[string][]int{}, rules: map[string][]int{}}
	p.d = model.Dataset{SchemaVersion: d.SchemaVersion, ID: d.ID, TimeZone: d.TimeZone, ServiceDate: d.ServiceDate, DataKind: d.DataKind}
	if d.Window != nil {
		w := *d.Window
		p.d.Window = &w
	}
	// Payload units are UTF-8 string bytes plus eight bytes per integer and one
	// per boolean, including the header; slice/map/ordinal overhead is excluded.
	headerBytes := int64(len(d.SchemaVersion) + len(d.ID) + len(d.TimeZone) + len(d.ServiceDate) + len(d.DataKind))
	if d.Window != nil {
		headerBytes += 16
	}
	stats.PayloadBytes = headerBytes
	if stats.PayloadBytes > limits.MaxPayloadBytes {
		return nil, stats, &TruncatedError{"preparation payload"}
	}
	h := sha256.New()
	if err := rawBind(h, "header", p.d); err != nil {
		return nil, stats, err
	}
	work := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if stats.Work >= limits.MaxWork {
			return &TruncatedError{"preparation work"}
		}
		stats.Work++
		return nil
	}
	row := func(kind string, value any, payload int64) error {
		if err := work(); err != nil {
			return err
		}
		if payload > limits.MaxPayloadBytes-stats.PayloadBytes {
			return &TruncatedError{"preparation payload"}
		}
		stats.PayloadBytes += payload
		stats.Rows++
		return rawBind(h, kind, value)
	}
	for _, s := range d.Stops {
		if err := row("stop", s, int64(len(s.ID)+len(s.ParentStation)+len(s.Accessible))+8); err != nil {
			return nil, stats, err
		}
		p.d.Stops = append(p.d.Stops, s)
		p.stops[s.ID] = rawStop{row: s}
	}
	for _, s := range d.Stops {
		if err := work(); err != nil {
			return nil, stats, err
		}
		v := p.stops[s.ID]
		if parent, ok := p.stops[s.ParentStation]; ok && parent.row.LocationType == 1 && s.ParentStation != s.ID {
			v.station = s.ParentStation
		}
		p.stops[s.ID] = v
	}
	for _, t := range d.Trips {
		if err := row("trip", t, int64(len(t.ID)+len(t.RouteID)+len(t.SourceID)+len(t.ServiceDate)+len(t.Accessible))); err != nil {
			return nil, stats, err
		}
		p.d.Trips = append(p.d.Trips, t)
		source := t.SourceID
		if source == "" {
			source = t.ID
		}
		p.trips[t.ID] = rawTrip{t, source}
	}
	for i, c := range d.Connections {
		if err := row("connection", c, int64(len(c.ID)+len(c.TripID)+len(c.From)+len(c.To))+26); err != nil {
			return nil, stats, err
		}
		p.d.Connections = append(p.d.Connections, c)
		p.rides[c.From] = append(p.rides[c.From], i)
	}
	for i, w := range d.Footpaths {
		if err := row("footpath", w, int64(len(w.ID)+len(w.From)+len(w.To)+len(w.Accessible)+len(w.Provenance))+8); err != nil {
			return nil, stats, err
		}
		p.d.Footpaths = append(p.d.Footpaths, w)
		p.walks[w.From] = append(p.walks[w.From], i)
	}
	for i, r := range d.Transfers {
		if err := row("rule", r, int64(len(r.ID)+len(r.FromStop)+len(r.ToStop)+len(r.FromTrip)+len(r.ToTrip)+len(r.FromRoute)+len(r.ToRoute))+16); err != nil {
			return nil, stats, err
		}
		p.d.Transfers = append(p.d.Transfers, r)
		p.rules[r.FromStop] = append(p.rules[r.FromStop], i)
	}
	if err := ctx.Err(); err != nil {
		return nil, stats, err
	}
	for range p.d.Stops {
		if err := work(); err != nil {
			return nil, stats, err
		}
	}
	stats.OrderedSHA256 = hex.EncodeToString(h.Sum(nil))
	p.stats = stats
	p.query = model.NewQueryValidator(p.d)
	if err := ctx.Err(); err != nil {
		return nil, stats, err
	}
	return p, stats, nil
}

func rawBind(h hash.Hash, kind string, value any) error {
	if _, err := h.Write([]byte(kind + "\n")); err != nil {
		return err
	}
	return json.NewEncoder(h).Encode(value)
}

type ProbeStats struct {
	Policy             string `json:"policy"`
	Probes             int    `json:"probes"`
	ConnectionRows     int    `json:"connection_rows"`
	FootpathRows       int    `json:"footpath_rows"`
	RuleRows           int    `json:"rule_rows"`
	MetadataProbes     int    `json:"metadata_probes"`
	CopiedLegs         int    `json:"copied_legs"`
	LegacyScans        int64  `json:"shadow_legacy_scans"`
	LegacyWouldDecline bool   `json:"shadow_legacy_would_decline"`
	LegacyComplete     bool   `json:"shadow_legacy_complete"`
	LegacySaturated    bool   `json:"shadow_legacy_saturated"`
}
type PreparedResult struct {
	Result
	ProbeStats ProbeStats `json:"probe_stats"`
}

// rule independently interprets literal raw selectors. Source buckets are
// merged by original ordinal; distinct maximal rules remain ambiguous.
func (p *Prepared) rule(arriving, departing model.Connection, fallback int64, probe func(string) error) (bool, int64, error) {
	for i := 0; i < 4; i++ {
		if err := probe("metadata"); err != nil {
			return false, 0, err
		}
	}
	a, b := p.trips[arriving.TripID], p.trips[departing.TripID]
	from, to := p.stops[arriving.To], p.stops[departing.From]
	if err := probe("metadata"); err != nil {
		return false, 0, err
	}
	first := p.rules[arriving.To]
	var second []int
	if from.station != "" {
		if err := probe("metadata"); err != nil {
			return false, 0, err
		}
		second = p.rules[from.station]
	}
	i, j, level, count := 0, 0, 0, 0
	var chosen model.TransferRule
	for i < len(first) || j < len(second) {
		ordinal := 0
		if j == len(second) || i < len(first) && first[i] < second[j] {
			ordinal = first[i]
			i++
		} else if i == len(first) || second[j] < first[i] {
			ordinal = second[j]
			j++
		} else {
			ordinal = first[i]
			i++
			j++
		}
		if err := probe("rule"); err != nil {
			return false, 0, err
		}
		r := p.d.Transfers[ordinal]
		if r.ToStop != departing.From && (to.station == "" || r.ToStop != to.station) {
			continue
		}
		if r.FromTrip != "" && r.FromTrip != a.source || r.ToTrip != "" && r.ToTrip != b.source || r.FromRoute != "" && r.FromRoute != a.row.RouteID || r.ToRoute != "" && r.ToRoute != b.row.RouteID {
			continue
		}
		specificity := 1
		if r.FromRoute != "" || r.ToRoute != "" {
			specificity = 2
		}
		if r.FromRoute != "" && r.ToRoute != "" {
			specificity = 3
		}
		if r.FromTrip != "" || r.ToTrip != "" {
			specificity = 4
		}
		if r.FromTrip != "" && r.ToRoute != "" || r.ToTrip != "" && r.FromRoute != "" {
			specificity = 5
		}
		if r.FromTrip != "" && r.ToTrip != "" {
			specificity = 6
		}
		if specificity > level {
			level, count, chosen = specificity, 1, r
		} else if specificity == level {
			count++
		}
	}
	if count > 1 {
		return false, 0, fmt.Errorf("oracle: ambiguous maximal transfer rules")
	}
	if count == 0 {
		return true, fallback, nil
	}
	minimum := int64(0)
	if chosen.Type == 2 {
		minimum = chosen.MinSeconds
	}
	return chosen.Type != 3, minimum, nil
}
