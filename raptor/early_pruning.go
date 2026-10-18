package raptor

import (
	"context"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

// EarlyPruningStats counts transfer-list work, excluding metric closure search.
// Skipped includes the tested entry that triggered the break.
type EarlyPruningStats struct {
	Checks          int `json:"bound_checks"`
	DominanceChecks int `json:"target_dominance_checks"`
	Breaks          int `json:"breaks"`
	Skipped         int `json:"transfer_entries_skipped"`
}

func (s *EarlyPruningStats) skip(n int) { s.Breaks++; s.Skipped += n }

// The existing walking provider emits metric-closure paths (including the
// zero-length stay). Sort each query-local list once, never mutate the index or
// the provider's cached destination-ordered list. Static edge duration equals
// walking cost, so all three lower-bound components are monotone in this order.
type orderedWalks struct {
	walker  *paper.Walker
	enabled bool
	cache   map[string][]paper.Path
}

func (w *orderedWalks) paths(ctx context.Context, from string) ([]paper.Path, error) {
	if !w.enabled {
		return w.walker.Paths(ctx, from)
	}
	if p, ok := w.cache[from]; ok {
		return p, ctx.Err()
	}
	p, err := w.walker.Paths(ctx, from)
	if err != nil {
		return nil, err
	}
	p = append([]paper.Path(nil), p...)
	sort.Slice(p, func(i, j int) bool {
		if p[i].Duration != p[j].Duration {
			return p[i].Duration < p[j].Duration
		}
		return p[i].To < p[j].To
	})
	w.cache[from] = p
	return p, nil
}
