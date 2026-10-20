package ultra

import (
	"context"
	"fmt"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

// SparseConfig selects a proved pruning subset, not general capped canonical
// ULTRA customization. Buffer is fixed because waiting may overlap walking but
// cannot overlap the on-vehicle part of a replacement witness.
type SparseConfig struct {
	Capped         CappedConfig `json:"capped"`
	TransferBuffer int64        `json:"transfer_buffer"`
	MaxProofChecks int          `json:"max_proof_checks"`
}

type StayProof struct {
	Incoming     string `json:"incoming_connection"`
	Witness      string `json:"witness_connection,omitempty"`
	Ready        int64  `json:"original_ready"`
	WitnessReady int64  `json:"witness_ready,omitempty"`
	TooLate      bool   `json:"later_than_last_departure,omitempty"`
}

type PrunedTransfer struct {
	From            string      `json:"from"`
	To              string      `json:"to"`
	Duration        int64       `json:"shortest_walk_seconds"`
	Reason          string      `json:"reason"`
	Minimum         int64       `json:"pair_minimum"`
	SelfMinimum     int64       `json:"self_minimum"`
	EarliestArrival int64       `json:"earliest_arrival,omitempty"`
	LastDeparture   int64       `json:"last_departure,omitempty"`
	Proofs          []StayProof `json:"proofs,omitempty"`
}

type SparsePreparation struct {
	Complete      bool              `json:"complete"`
	Error         string            `json:"error,omitempty"`
	Profile       string            `json:"profile"`
	Config        SparseConfig      `json:"config"`
	Control       CappedPreparation `json:"transfer_complete_preparation"`
	RetainedPairs int               `json:"retained_nonempty_pairs"`
	PrunedPairs   int               `json:"pruned_nonempty_pairs"`
	ProofChecks   int               `json:"proof_checks"`
	Certificates  []PrunedTransfer  `json:"certificates"`
}

type SparseIndex struct {
	index *CappedIndex
	prep  SparsePreparation
}

func (x *SparseIndex) Complete() bool { return x != nil && x.prep.Complete }
func (x *SparseIndex) Preparation() SparsePreparation {
	s := x.prep
	s.Certificates = append([]PrunedTransfer{}, s.Certificates...)
	for i := range s.Certificates {
		s.Certificates[i].Proofs = append([]StayProof{}, s.Certificates[i].Proofs...)
	}
	return s
}

// BuildSparse first prepares the complete control, then removes only forbidden,
// structurally/time-infeasible pairs and pairs with a stay-on-incoming-trip
// witness for EVERY possible alighting event. Empty self transfers are NEVER
// removed: all stay witnesses use only these, so pruning has no dependency cycle.
// Preparation still pays the complete-control cost. Certificates add audit memory;
// no peak-memory or paper-level shortcut-sparsity guarantee is made.
func BuildSparse(ctx context.Context, d model.Dataset, cfg SparseConfig) (out *SparseIndex, err error) {
	out = &SparseIndex{prep: SparsePreparation{Profile: "ultra_cap_safe_stay_witness_subset_v1", Config: cfg, Certificates: []PrunedTransfer{}}}
	defer func() {
		out.prep.Complete = err == nil
		if err != nil {
			out.prep.Error = err.Error()
		}
	}()
	if cfg.MaxProofChecks <= 0 || cfg.TransferBuffer < 0 || cfg.TransferBuffer > model.MaxServiceSecond {
		return out, fmt.Errorf("ULTRA sparse: invalid proof/buffer configuration")
	}
	out.index, err = BuildCapped(ctx, d, cfg.Capped)
	out.prep.Control = out.index.Preparation()
	if err != nil {
		return
	}
	check := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if out.prep.ProofChecks == cfg.MaxProofChecks {
			return fmt.Errorf("ULTRA sparse incomplete: proof budget exhausted")
		}
		out.prep.ProofChecks++
		return nil
	}
	p := out.index.x.p
	incoming := map[string][]model.Connection{}
	future := map[string]map[string][]model.Connection{}
	lastDeparture := map[string]int64{}
	for _, c := range p.Data.Connections {
		if err = check(); err != nil {
			return
		}
		if cfg.Capped.AccessibleOnly && p.Trips[c.TripID].Accessible != model.Yes {
			continue
		}
		if c.Pickup && (!cfg.Capped.AccessibleOnly || p.Stops[c.From].Accessible == model.Yes) {
			old, ok := lastDeparture[c.From]
			if !ok || c.Departure > old {
				lastDeparture[c.From] = c.Departure
			}
		}
		if !c.Dropoff || cfg.Capped.AccessibleOnly && p.Stops[c.To].Accessible != model.Yes {
			continue
		}
		incoming[c.To] = append(incoming[c.To], c)
		if future[c.TripID] == nil {
			future[c.TripID] = map[string][]model.Connection{}
		}
		future[c.TripID][c.To] = append(future[c.TripID][c.To], c)
	}
	// Sorting is input-sized indexing, outside certificate checks. Check context
	// between lists; comparison queries below are individually budgeted.
	for _, byStop := range future {
		for stop, cs := range byStop {
			if err = ctx.Err(); err != nil {
				return
			}
			sort.Slice(cs, func(i, j int) bool { return cs[i].Sequence < cs[j].Sequence })
			byStop[stop] = cs
		}
	}
	for stop, cs := range incoming {
		if err = ctx.Err(); err != nil {
			return
		}
		sort.Slice(cs, func(i, j int) bool { return cs[i].ID < cs[j].ID })
		incoming[stop] = cs
	}
	sources := []string{}
	for from := range out.index.rows {
		sources = append(sources, from)
	}
	sort.Strings(sources)
	for _, from := range sources {
		kept := []paper.Path{}
		for _, path := range out.index.rows[from] {
			if err = check(); err != nil {
				return
			}
			if path.To == from {
				kept = append(kept, path)
				continue
			}
			cert := PrunedTransfer{From: from, To: path.To, Duration: path.Duration}
			canPrune := false
			arrivals := incoming[from]
			last, hasDeparture := lastDeparture[path.To]
			switch {
			case len(arrivals) == 0:
				cert.Reason = "no_alighting"
				canPrune = true
			case !hasDeparture:
				cert.Reason = "no_boarding"
				canPrune = true
			default:
				// Common profile rejects trip/route selectors, so this decision is static
				// for the stop pair. Fixing Buffer keeps its default-minimum semantics.
				decision, e := p.Rules.Resolve(model.Connection{To: from}, model.Connection{From: path.To}, cfg.TransferBuffer)
				if e != nil {
					return out, e
				}
				cert.Minimum = decision.Minimum
				cert.LastDeparture = last
				if !decision.Allowed {
					cert.Reason = "prohibited"
					canPrune = true
					break
				}
				earliest := paper.Infinity
				for _, c := range arrivals {
					if err = check(); err != nil {
						return
					}
					earliest = min(earliest, c.Arrival)
				}
				cert.EarliestArrival = earliest
				if earliest+max(path.Duration, decision.Minimum) > last {
					cert.Reason = "schedule"
					canPrune = true
					break
				}
				self, e := p.Rules.Resolve(model.Connection{To: path.To}, model.Connection{From: path.To}, cfg.TransferBuffer)
				if e != nil {
					return out, e
				}
				if !self.Allowed {
					break
				}
				cert.SelfMinimum = self.Minimum
				cert.Reason = "stay_on_trip"
				canPrune = true
				for _, c := range arrivals {
					if err = check(); err != nil {
						return
					}
					ready := c.Arrival + max(path.Duration, decision.Minimum)
					proof := StayProof{Incoming: c.ID, Ready: ready}
					if ready > last {
						proof.TooLate = true
						cert.Proofs = append(cert.Proofs, proof)
						continue
					}
					cs := future[c.TripID][path.To]
					lo, hi := 0, len(cs)
					for lo < hi {
						if err = check(); err != nil {
							return
						}
						mid := lo + (hi-lo)/2
						if cs[mid].Sequence <= c.Sequence {
							lo = mid + 1
						} else {
							hi = mid
						}
					}
					if lo == len(cs) || cs[lo].Arrival+self.Minimum > ready {
						canPrune = false
						break
					}
					proof.Witness = cs[lo].ID
					proof.WitnessReady = cs[lo].Arrival + self.Minimum
					cert.Proofs = append(cert.Proofs, proof)
				}
			}
			if canPrune {
				out.prep.Certificates = append(out.prep.Certificates, cert)
				out.prep.PrunedPairs++
			} else {
				kept = append(kept, path)
				out.prep.RetainedPairs++
			}
		}
		out.index.rows[from] = kept
	}
	return
}

func (x *SparseIndex) Search(ctx context.Context, q model.Query, l Limits) (CappedResult, error) {
	fail := func(e error) (CappedResult, error) {
		return CappedResult{Journeys: []model.Journey{}, Error: e.Error()}, e
	}
	if !x.Complete() {
		return fail(fmt.Errorf("ULTRA sparse: incomplete index"))
	}
	if q.TransferBuffer != x.prep.Config.TransferBuffer {
		return fail(fmt.Errorf("ULTRA sparse capability: transfer buffer differs from proof"))
	}
	return x.index.Search(ctx, q, l)
}
