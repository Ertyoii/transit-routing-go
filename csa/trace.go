package csa

type TraceLabel struct {
	Stop      string `json:"stop"`
	Time      int64  `json:"time"`
	Boardings int    `json:"boardings"`
	Depth     int    `json:"depth"`
	Walking   int64  `json:"walking"`
	LastRide  string `json:"last_ride,omitempty"`
}
type TraceEvent struct {
	Witness         int          `json:"witness"`
	Action          string       `json:"action"`
	Label           TraceLabel   `json:"label"`
	GeneratedLabels int          `json:"generated_labels"`
	PoolSize        int          `json:"pool_size"`
	Occupants       []TraceLabel `json:"occupants,omitempty"`
}

func (l *lane) traceLabel(p *label) TraceLabel {
	r := TraceLabel{Stop: l.index.data.Stops[p.stop].ID, Time: p.time, Boardings: p.boards, Depth: p.depth, Walking: p.walk}
	if p.lastRide >= 0 {
		r.LastRide = l.index.data.Connections[p.lastRide].ID
	}
	return r
}
func (l *lane) trace(p *label, action string) {
	if len(l.config.TraceWitnesses) == 0 {
		return
	}
	for index, witness := range l.config.TraceWitnesses {
		if p.depth > len(witness.Legs) {
			continue
		}
		if p.depth > 0 && p.leg != witness.Legs[p.depth-1] {
			continue
		}
		matches := true
		cursor := p
		for i := p.depth - 1; i >= 0; i-- {
			if cursor == nil || cursor.leg != witness.Legs[i] {
				matches = false
				break
			}
			cursor = cursor.parent
		}
		if !matches {
			continue
		}
		if len(l.result.Trace) >= 1024 {
			l.result.TraceTruncated = true
			return
		}
		event := TraceEvent{Witness: index, Action: action, Label: l.traceLabel(p), GeneratedLabels: l.result.Stats.Labels, PoolSize: len(l.pools[p.stop])}
		if action == "per_stop_cap" {
			for _, other := range l.pools[p.stop] {
				event.Occupants = append(event.Occupants, l.traceLabel(other))
			}
		}
		l.result.Trace = append(l.result.Trace, event)
	}
}
