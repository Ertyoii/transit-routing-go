package csa

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
)

type CapResult struct {
	MaxBoardings int    `json:"max_boardings"`
	Result       Result `json:"result"`
}

func capQuery(q model.Query, b int) model.Query {
	id, _ := json.Marshal([]any{q.ID, b})
	q.ID = string(id)
	q.MaxBoardings = b
	return q
}
func (idx *Index) combineCaps(q model.Query, results []Result, k int) (Result, error) {
	out := Result{Binding: model.BindQuery(idx.datasetSHA256, q), QueryID: q.ID, Journeys: []model.Journey{}, CapTopK: k, CapComplete: true}
	if len(results) != q.MaxBoardings+1 {
		return Result{}, fmt.Errorf("cap result count mismatch")
	}
	seen := map[string]bool{}
	for b, r := range results {
		expected := capQuery(q, b)
		if r.QueryID != expected.ID || r.Binding != model.BindQuery(idx.datasetSHA256, expected) || r.TopK != k {
			return Result{}, fmt.Errorf("cap result query/network/k binding mismatch")
		}
		out.CapResults = append(out.CapResults, CapResult{b, r})
		out.CapComplete = out.CapComplete && r.TopKComplete
		if r.Error != "" {
			out.Error += fmt.Sprintf("boarding cap %d: %s; ", b, r.Error)
		}
		out.Stats.Labels += r.Stats.Labels
		out.Stats.ConnectionAttempts += r.Stats.ConnectionAttempts
		out.Stats.FootpathAttempts += r.Stats.FootpathAttempts
		out.Stats.EquivalentPruned += r.Stats.EquivalentPruned
		out.Stats.ArrivalPruned += r.Stats.ArrivalPruned
		out.Stats.TerminalCandidates += r.Stats.TerminalCandidates
		out.Stats.EquivalentStates += r.Stats.EquivalentStates
		out.Stats.DeadEndPruned += r.Stats.DeadEndPruned
		for _, j := range r.Journeys {
			id := model.Identity(j)
			if !seen[id] {
				seen[id] = true
				out.Journeys = append(out.Journeys, j)
			}
		}
	}
	sort.Slice(out.Journeys, func(i, j int) bool { return model.Identity(out.Journeys[i]) < model.Identity(out.Journeys[j]) })
	return out, nil
}

// StratifiedSingle unions separately certified top-k sets for boarding caps
// 0..MaxBoardings. Each lane has its own label budget; the caller's context is a
// common wall-time budget. The union is NOT itself a global strict top-k list.
func (idx *Index) StratifiedSingle(ctx context.Context, q model.Query, cfg Config) (Result, error) {
	if err := q.Validate(idx.data); err != nil {
		return Result{}, err
	}
	if cfg.ExactTopK <= 0 {
		return Result{}, fmt.Errorf("stratified search requires ExactTopK")
	}
	results := []Result{}
	var first error
	for b := 0; b <= q.MaxBoardings; b++ {
		r, e := idx.Single(ctx, capQuery(q, b), cfg)
		if e != nil && r.Error == "" {
			r.Error = e.Error()
		}
		results = append(results, r)
		if first == nil {
			first = e
		}
	}
	out, e := idx.combineCaps(q, results, cfg.ExactTopK)
	if e != nil {
		return out, e
	}
	return out, first
}

// StratifiedShared expands request/cap lanes into one genuinely shared scan.
// It does not loop over Single. The independent per-cap budgets preserve lane
// ordering/partition invariance; total query allowance is explicitly multiplied.
func (idx *Index) StratifiedShared(ctx context.Context, qs []model.Query, cfg Config) (Batch, error) {
	if cfg.ExactTopK <= 0 {
		return Batch{}, fmt.Errorf("stratified search requires ExactTopK")
	}
	expanded := []model.Query{}
	ids := map[string]bool{}
	for _, q := range qs {
		if err := q.Validate(idx.data); err != nil {
			return Batch{}, err
		}
		if ids[q.ID] {
			return Batch{}, fmt.Errorf("duplicate query ID")
		}
		ids[q.ID] = true
		for b := 0; b <= q.MaxBoardings; b++ {
			expanded = append(expanded, capQuery(q, b))
		}
	}
	batch, e := idx.Shared(ctx, expanded, cfg)
	if len(batch.Results) != len(expanded) {
		return Batch{}, e
	}
	out := Batch{MakespanNS: batch.MakespanNS, SharedConnectionVisits: batch.SharedConnectionVisits, Results: []Result{}}
	offset := 0
	for _, q := range qs {
		end := offset + q.MaxBoardings + 1
		combined, err := idx.combineCaps(q, batch.Results[offset:end], cfg.ExactTopK)
		if err != nil {
			return Batch{}, err
		}
		out.Results = append(out.Results, combined)
		offset = end
	}
	return out, e
}
