// Package comparison adapts result contracts without changing routing semantics.
package comparison

import (
	"encoding/json"
	"fmt"
	"github.com/ertyoii/transit-routing-go/csa"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/presentation"
	"github.com/ertyoii/transit-routing-go/validate"
	"reflect"
)

type RankedCaps struct {
	Candidates     []model.Validated   `json:"candidates"`
	Ranked         presentation.Result `json:"ranked"`
	StrictComplete bool                `json:"strict_complete"`
	Certificate    string              `json:"certificate"`
}

// RankCaps validates the complete raw cap union before applying score ranking.
// A certificate requires all at-most-boarding caps, each certified for >=k under
// arrival/transfer/walking/leg/identity order. See CROSS_FAMILY_PROTOCOL.md.
// Incomplete child results remain feasible candidates, never strict top-k truth.
func RankCaps(checker *validate.Checker, q model.Query, r csa.Result, k int, penalty int64) (out RankedCaps, err error) {
	out.Candidates = []model.Validated{}
	if checker == nil || k <= 0 || penalty < 0 || penalty > model.MaxServiceSecond {
		return out, fmt.Errorf("invalid ranked-cap adapter arguments")
	}
	binding, e := CheckBinding(checker, q, r)
	if e != nil {
		return out, e
	}
	if r.QueryID != q.ID || len(r.CapResults) != q.MaxBoardings+1 || r.CapTopK < k {
		return out, fmt.Errorf("ranked-cap certificate coverage mismatch")
	}
	union := map[string]model.Validated{}
	complete := r.CapComplete && r.Error == ""
	for b, child := range r.CapResults {
		id, _ := json.Marshal([]any{q.ID, b})
		cq := q
		cq.ID = string(id)
		cq.MaxBoardings = b
		if child.Result.QueryID != cq.ID || child.Result.Binding != model.BindQuery(binding.DatasetSHA256, cq) || child.MaxBoardings != b || child.Result.TopK != r.CapTopK {
			return out, fmt.Errorf("ranked-cap child identity/k mismatch")
		}
		complete = complete && child.Result.TopKComplete && child.Result.Error == "" && child.Result.Stats.HeuristicDiscarded == 0
		seen := map[string]bool{}
		for _, j := range child.Result.Journeys {
			v, e := checker.Check(cq, j)
			if e != nil {
				return out, e
			}
			if seen[v.Identity] {
				return out, fmt.Errorf("duplicate child journey")
			}
			seen[v.Identity] = true
			if old, ok := union[v.Identity]; ok && !reflect.DeepEqual(old, v) {
				return out, fmt.Errorf("cap union identity has conflicting witness")
			}
			union[v.Identity] = v
		}
		if child.Result.TopKComplete && len(seen) > child.Result.TopK {
			return out, fmt.Errorf("child exceeds certified k")
		}
	}
	seen := map[string]bool{}
	for _, j := range r.Journeys {
		v, e := checker.Check(q, j)
		if e != nil {
			return out, e
		}
		if seen[v.Identity] || !reflect.DeepEqual(union[v.Identity], v) {
			return out, fmt.Errorf("raw cap union differs from children")
		}
		seen[v.Identity] = true
		out.Candidates = append(out.Candidates, v)
	}
	if len(seen) != len(union) {
		return out, fmt.Errorf("raw cap union omits child identities")
	}
	out.Ranked, err = presentation.Rank(q, out.Candidates, k, penalty)
	if err != nil {
		return out, err
	}
	out.StrictComplete = complete
	out.Ranked.GlobalOptimalityClaim = complete
	if complete {
		out.Certificate = "complete_at_most_boarding_arrival_top_k_union_nonnegative_transfer_penalty_bound_v2"
	} else {
		out.Certificate = "uncertified_partial_cap_union"
	}
	return
}
