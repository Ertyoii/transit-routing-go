package raptor

import (
	"context"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

// pathState is query-local metadata parallel to one immutable walking closure.
// A transfer's ready delay depends on its stop pair, walking duration and the
// query's fixed buffer, not on the arrival time or the route that reached it.
// Resolve lazily at the original call site, preserving rule-error ordering.
type pathState struct {
	stop    int
	delay   int64
	ready   bool
	allowed bool
}

type indexedClosure struct {
	paths  []paper.Path
	states []pathState
}

type indexedWalks struct {
	index  *Index
	query  model.Query
	walker *paper.Walker
	early  bool
	rows   []indexedClosure
}

func (x *Index) indexedWalks(walker *paper.Walker, q model.Query, early bool) indexedWalks {
	return indexedWalks{
		index:  x,
		query:  q,
		walker: walker,
		early:  early,
		rows:   make([]indexedClosure, len(x.atStop)),
	}
}

func (w *indexedWalks) paths(ctx context.Context, from string, source int) ([]paper.Path, []pathState, error) {
	// A cached native path lookup polls Context.Err exactly once. Dense rows
	// preserve that poll and the first-lookup reconstruction/sort order without
	// repeating either walking-cache string-map lookup on every route label.
	row := &w.rows[source]
	if row.paths != nil {
		return row.paths, row.states, ctx.Err()
	}
	paths, err := w.walker.Paths(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	if w.early {
		paths = append([]paper.Path(nil), paths...)
		sort.Slice(paths, func(i, j int) bool {
			if paths[i].Duration != paths[j].Duration {
				return paths[i].Duration < paths[j].Duration
			}
			return paths[i].To < paths[j].To
		})
	}
	states := make([]pathState, len(paths))
	for i, p := range paths {
		states[i].stop = w.index.stopIDs[p.To]
	}
	row.paths, row.states = paths, states
	return paths, states, nil
}

func (w *indexedWalks) readyTime(state *pathState, from string, path paper.Path, arrival int64) (int64, bool, error) {
	if !state.ready {
		delay, allowed, err := w.index.p.ReadyTime(w.query, from, path.To, 0, path.Duration)
		if err != nil {
			return 0, false, err
		}
		state.delay, state.allowed, state.ready = delay, allowed, true
	}
	return arrival + state.delay, state.allowed, nil
}
