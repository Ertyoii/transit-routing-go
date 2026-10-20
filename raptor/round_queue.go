package raptor

import "sort"

// collectRoutes takes the same minimum route position as the map-based queue.
// A zero entry means absent; actual positions are encoded plus one. Sorting
// touched routes keeps scan/tie order independent of stop insertion order.
// Both tables belong to one round of one query; the immutable index is read-only.
func (x *Index) collectRoutes(stops []int) (positions, routes []int) {
	positions = make([]int, len(x.routes))
	for _, s := range stops {
		for _, o := range x.atStop[s] {
			p := o.position + 1
			if positions[o.route] == 0 {
				routes = append(routes, o.route)
				positions[o.route] = p
			} else if p < positions[o.route] {
				positions[o.route] = p
			}
		}
	}
	sort.Ints(routes)
	return
}
