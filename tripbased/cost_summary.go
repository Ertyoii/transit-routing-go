package tripbased

import "encoding/json"

// CostSummary copies compact diagnostic counters without duplicating the full
// witness/index graph. Call after construction or between queries, not during a
// concurrent mutation. Full independently auditable witnesses remain available
// through Snapshot for correctness corpora.
func (x *FlashIndex) CostSummary() json.RawMessage {
	if x == nil {
		return json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{"complete": x.snap.Complete, "error": x.snap.Error, "stats": x.snap.Stats, "phases_ns": x.snap.PhasesNS, "lines": len(x.snap.Lines), "transfers": len(x.snap.Transfers), "witnesses": len(x.snap.Witnesses), "closure_diameter": x.snap.ClosureDiameter})
	return b
}
func (x *PatternIndex) CostSummary() json.RawMessage {
	if x == nil {
		return json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{"complete": x.snap.Complete, "error": x.snap.Error, "stats": x.snap.Stats, "phases_ns": x.snap.PhasesNS, "dags": len(x.dags), "witnesses": len(x.snap.Witnesses), "backend": x.base.CostSummary()})
	return b
}
func (x *ScalableIndex) CostSummary() json.RawMessage {
	if x == nil {
		return json.RawMessage("null")
	}
	s := x.snap.Stats
	if x.directCache != nil {
		s.DirectCache = x.directCache.stats
	}
	var shared any
	if x.shared != nil {
		shared = x.shared.stats
	}
	b, _ := json.Marshal(map[string]any{"complete": x.snap.Complete, "error": x.snap.Error, "stats": s, "cluster_stats": shared, "phases_ns": x.snap.PhasesNS, "phase_costs": x.snap.PhaseCosts, "local": x.local.CostSummary(), "border": x.border.CostSummary(), "long_distance": x.long.CostSummary()})
	return b
}
func (x *TREXIndex) CostSummary() json.RawMessage {
	if x == nil {
		return json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{"complete": x.snap.Complete, "error": x.snap.Error, "stats": x.snap.Stats, "phases": x.snap.Phases, "lines": len(x.snap.Lines), "transfers": len(x.snap.Transfers), "witnesses": len(x.snap.Witnesses), "closure_diameter": x.snap.ClosureDiameter})
	return b
}
