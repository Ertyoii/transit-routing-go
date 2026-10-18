package ultra

import "sort"

type occurrence struct{ route, position int }

// Collection preserves canonical route order and the earliest feasible marked
// position. Range calls pass current-run marks; fresh calls consider all labels.
func (x *Index) collectRoutes(prev []label, maximum int64, destination int, marked []bool, w *work) ([]occurrence, error) {
	if !x.s.Config.ActiveRoutes && marked == nil {
		return x.allRoutes, nil
	}
	earliest := map[int]int{}
	for v, lab := range prev {
		if v == destination || lab.time > maximum || marked != nil && !marked[v] {
			continue
		}
		if e := w.check(); e != nil {
			return nil, e
		}
		for _, o := range x.at[v] {
			if e := w.connection(); e != nil {
				return nil, e
			}
			w.s.RouteOccurrenceChecks++
			r := x.routes[o.route]
			if r.trips[len(r.trips)-1].cs[o.position].Departure < lab.time {
				continue
			}
			if old, ok := earliest[o.route]; !ok || o.position < old {
				earliest[o.route] = o.position
			}
		}
	}
	ids := []int{}
	for r := range earliest {
		ids = append(ids, r)
	}
	sort.Ints(ids)
	out := make([]occurrence, 0, len(ids))
	for _, r := range ids {
		out = append(out, occurrence{r, earliest[r]})
	}
	return out, nil
}
