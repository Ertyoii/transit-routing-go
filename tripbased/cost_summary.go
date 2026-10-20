package tripbased

import (
	"encoding/json"
	"github.com/ertyoii/transit-routing-go/model"
	"unsafe"
)

// CostSummary copies compact diagnostic counters without duplicating the full
// witness/index graph. Call after construction or between queries, not during a
// concurrent mutation. Full independently auditable witnesses remain available
// through Snapshot for correctness corpora.
func (x *FlashIndex) CostSummary() json.RawMessage {
	if x == nil {
		return json.RawMessage("null")
	}
	v := map[string]any{"complete": x.snap.Complete, "error": x.snap.Error, "stats": x.snap.Stats, "phases_ns": x.snap.PhasesNS, "lines": len(x.snap.Lines), "transfers": len(x.snap.Transfers), "witnesses": len(x.snap.Witnesses), "closure_diameter": x.snap.ClosureDiameter}
	if x.cfg.Semantics != "" {
		v["profile"] = x.snap.Profile
		v["transfer_construction"] = x.snap.TransferConstruction
		v["flag_construction"] = x.snap.FlagConstruction
		v["semantic_transfer_count"] = x.ruleTransferCount
		v["transfer_diagnostics"] = x.ruleTransferDiagnostics()
		v["transfers"] = x.ruleTransferCount
		v["snapshot_transfer_record_limit"] = x.cfg.Limits.MaxSnapshotTransfers
	}
	if x.cfg.ProfilePolicy != "" {
		v["profile_policy"] = x.cfg.ProfilePolicy
		v["retained_limits"] = map[string]int{"readiness_points": x.cfg.Limits.MaxRetainedReadiness, "propagation_runs": x.cfg.Limits.MaxRetainedRuns, "sources": x.cfg.Limits.MaxRetainedSources, "pending_segments": x.cfg.Limits.MaxRetainedQueued}
	}
	b, _ := json.Marshal(v)
	return b
}
func (x *PatternIndex) CostSummary() json.RawMessage {
	if x == nil {
		return json.RawMessage("null")
	}
	v := map[string]any{"complete": x.snap.Complete, "error": x.snap.Error, "stats": x.snap.Stats, "phases_ns": x.snap.PhasesNS, "dags": len(x.dags), "witnesses": len(x.snap.Witnesses), "backend": x.base.CostSummary()}
	if x.cfg.Profile.ProfilePolicy != "" {
		v["profile"] = x.snap.Profile
		rawLegs, legsBytes, edgeBytes, nodeBytes, arcBytes := 0, 0, 0, 0, 0
		for _, w := range x.snap.Witnesses {
			rawLegs += len(w.Journey.Legs)
			legsBytes += cap(w.Journey.Legs) * int(unsafe.Sizeof(model.Leg{}))
			edgeBytes += cap(w.Edges) * int(unsafe.Sizeof(PatternEdge{}))
		}
		for _, d := range x.dags {
			nodeBytes += cap(d.Nodes) * int(unsafe.Sizeof(PatternNode{}))
			for _, arcs := range d.Targets {
				arcBytes += cap(arcs) * int(unsafe.Sizeof(PatternArc{}))
			}
		}
		v["retained_pattern_payload"] = map[string]any{"raw_witness_legs": rawLegs, "witness_record_capacity_bytes": cap(x.snap.Witnesses) * int(unsafe.Sizeof(PatternWitness{})), "raw_leg_capacity_bytes": legsBytes, "witness_edge_capacity_bytes": edgeBytes, "dag_node_capacity_bytes": nodeBytes, "target_arc_capacity_bytes": arcBytes, "excludes": "maps, strings, source/base graph, allocator overhead and transient build state; not RSS"}
	}
	b, _ := json.Marshal(v)
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
	b, _ := json.Marshal(map[string]any{"complete": x.snap.Complete, "error": x.snap.Error, "stats": x.snap.Stats, "phases": x.snap.Phases, "lines": len(x.snap.Lines), "transfers": len(x.edges), "witnesses": x.proofs.count, "closure_diameter": x.snap.ClosureDiameter})
	return b
}
