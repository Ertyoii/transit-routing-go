package tripbased

// TREXDiagnostic is bounded preparation metadata. Transfer and witness payloads
// remain in Snapshot; this view never expands or copies those large collections.
// Call only after preparation returns, as with Snapshot.
type TREXDiagnostic struct {
	Profile           string               `json:"profile"`
	Complete          bool                 `json:"complete"`
	Error             string               `json:"error,omitempty"`
	Config            TREXConfig           `json:"config"`
	Stats             TREXStats            `json:"stats"`
	Phases            map[string]TREXPhase `json:"phases"`
	ClosureDiameter   int64                `json:"closure_diameter"`
	RetainedTransfers int                  `json:"retained_transfers"`
	RetainedWitnesses int                  `json:"retained_witnesses"`
	PartitionStops    int                  `json:"partition_stops"`
	WalkingComponents int                  `json:"walking_components"`
}

func (x *TREXIndex) Diagnostic() TREXDiagnostic {
	s := x.snap
	out := TREXDiagnostic{Profile: s.Profile, Complete: s.Complete, Error: s.Error,
		Config: s.Config, Stats: s.Stats, ClosureDiameter: s.ClosureDiameter,
		RetainedTransfers: len(x.edges), RetainedWitnesses: x.proofs.count,
		PartitionStops: len(s.Partition.Cells), WalkingComponents: s.Partition.Components,
		Phases: make(map[string]TREXPhase, len(s.Phases))}
	out.Config.Partition = make(map[string]int, len(s.Config.Partition))
	for k, v := range s.Config.Partition {
		out.Config.Partition[k] = v
	}
	out.Stats.Levels = append([]TREXLevelWork(nil), s.Stats.Levels...)
	out.Stats.Ranks = append([]int(nil), s.Stats.Ranks...)
	for k, v := range s.Phases {
		out.Phases[k] = v
	}
	return out
}
