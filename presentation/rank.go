// Package presentation ranks a supplied candidate set; it makes no global optimality claim.
package presentation

import (
	"cmp"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/selection"
	"slices"
)

type Option struct {
	Journey             model.Validated `json:"journey"`
	ActualDuration      int64           `json:"actual_duration_seconds"`
	TransferPenaltyCost int64           `json:"transfer_penalty_cost_seconds"`
	GeneralizedScore    int64           `json:"generalized_score_seconds"`
}
type Result struct {
	K                     int      `json:"k"`
	TransferPenalty       int64    `json:"ranking_transfer_penalty_seconds"`
	CandidateCount        int      `json:"candidate_count"`
	GlobalOptimalityClaim bool     `json:"global_optimality_claim"`
	Options               []Option `json:"options"`
}

func Rank(q model.Query, candidates []model.Validated, k int, penalty int64) (Result, error) {
	r := Result{K: k, TransferPenalty: penalty, CandidateCount: len(candidates), Options: []Option{}}
	if k <= 0 || penalty < 0 || penalty > model.MaxServiceSecond {
		return r, fmt.Errorf("invalid display k/penalty")
	}
	seen := map[string]bool{}
	for _, j := range candidates {
		if seen[j.Identity] || j.Metrics.Arrival < q.Departure || j.Metrics.Transfers < 0 || j.Metrics.Transfers > q.MaxBoardings {
			return r, fmt.Errorf("invalid/duplicate candidate")
		}
		seen[j.Identity] = true
		duration := j.Metrics.Arrival - q.Departure
		cost := int64(j.Metrics.Transfers) * penalty
		r.Options = append(r.Options, Option{Journey: j, ActualDuration: duration, TransferPenaltyCost: cost, GeneralizedScore: duration + cost})
	}
	slices.SortFunc(r.Options, func(a, b Option) int {
		if c := cmp.Compare(a.GeneralizedScore, b.GeneralizedScore); c != 0 {
			return c
		}
		return selection.Compare(a.Journey, b.Journey)
	})
	if len(r.Options) > k {
		r.Options = r.Options[:k]
	}
	return r, nil
}
