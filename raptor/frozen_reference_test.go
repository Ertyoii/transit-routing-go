// Test-only frozen accepted v1 search bodies; differential evidence, not an independent oracle.
package raptor

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

func (x *Index) frozenSearchV1(ctx context.Context, q model.Query, early bool) (out paper.Result, pruning EarlyPruningStats, err error) {
	out.Journeys = []model.Journey{}
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}()
	if err = x.p.CheckContext(ctx, q); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if !x.p.Accessible(q, q.Origin) || !x.p.Accessible(q, q.Destination) {
		return
	}
	if q.Origin == q.Destination {
		out.Journeys = append(out.Journeys, paper.Journey(nil))
		return
	}
	walker := x.p.Walker(q)
	walks := orderedWalks{walker: walker, enabled: early, cache: map[string][]paper.Path{}}
	defer func() { out.Stats.WalkClosures = walker.Count }()
	previous := map[string]paper.Label{}
	bestArrival := paper.Infinity
	paths, e := walks.paths(ctx, q.Origin)
	if e != nil {
		err = e
		return
	}
	for i, w := range paths {
		at := q.Departure + w.Duration
		if early {
			pruning.Checks++
			if at >= bestArrival {
				pruning.skip(len(paths) - i)
				break
			}
		}
		if at > q.LatestArrival {
			continue
		}
		n := w.Append(nil, q.Departure)
		if w.To == q.Destination {
			if w.Duration <= q.Walking.Direct {
				bestArrival = at
				out.Journeys = append(out.Journeys, paper.Journey(n))
			}
			continue
		}
		if w.Duration <= q.Walking.Access {
			previous[w.To] = paper.Label{Time: at, Parent: n}
		}
	}
	// Exact-round readiness is kept separate from raw alighting and access
	// labels: role caps and stop-pair rules make ordinary stop-local pruning unsafe.
	for b := 1; b <= q.MaxBoardings && len(previous) > 0; b++ {
		if err = ctx.Err(); err != nil {
			return
		}
		out.Stats.Rounds++
		queue := map[int]int{}
		for stop := range previous {
			for _, o := range x.at[stop] {
				p, ok := queue[o.route]
				if !ok || o.position < p {
					queue[o.route] = o.position
				}
			}
		}
		routes := make([]int, 0, len(queue))
		for r := range queue {
			routes = append(routes, r)
		}
		sort.Ints(routes)
		next := map[string]paper.Label{}
		best := paper.Label{Time: paper.Infinity}
		for _, r := range routes {
			if err = ctx.Err(); err != nil {
				return
			}
			out.Stats.Routes++
			route := x.routes[r]
			current := -1
			var parent *paper.Node
			for i := queue[r]; i < len(route.trips[0].cs); i++ {
				if err = ctx.Err(); err != nil {
					return
				}
				prototype := route.trips[0].cs[i]
				if label, ok := previous[prototype.From]; ok && prototype.Pickup && x.p.Accessible(q, prototype.From) {
					t := sort.Search(len(route.trips), func(j int) bool { return route.trips[j].cs[i].Departure >= label.Time })
					if t < len(route.trips) && (current < 0 || t < current) && x.p.TripAllowed(q, route.trips[t].id) {
						current = t
						parent = label.Parent
					}
				}
				if current < 0 {
					continue
				}
				c := route.trips[current].cs[i]
				out.Stats.Connections++
				if c.Arrival > q.LatestArrival {
					current = -1
					continue
				}
				parent = paper.Ride(parent, c)
				if !c.Dropoff || !x.p.Accessible(q, c.To) {
					continue
				}
				if c.To == q.Destination {
					if c.Arrival < best.Time {
						best = paper.Label{Time: c.Arrival, Parent: parent}
					}
					current = -1
					continue
				}
				paths, e := walks.paths(ctx, c.To)
				if e != nil {
					err = e
					return
				}
				for i, w := range paths {
					at := c.Arrival + w.Duration
					if early {
						pruning.Checks++
						if at >= min(bestArrival, best.Time) {
							pruning.skip(len(paths) - i)
							break
						}
					}
					if at > q.LatestArrival {
						continue
					}
					if w.To == q.Destination {
						if w.Duration <= q.Walking.Egress && at < best.Time {
							best = paper.Label{Time: at, Parent: w.Append(parent, c.Arrival)}
						}
						continue
					}
					if b == q.MaxBoardings || w.Duration > q.Walking.Transfer {
						continue
					}
					at, allowed, e := x.p.ReadyTime(q, c.To, w.To, c.Arrival, w.Duration)
					if e != nil {
						err = e
						return
					}
					if !allowed || at > q.LatestArrival {
						continue
					}
					old, ok := next[w.To]
					if !ok || at < old.Time {
						next[w.To] = paper.Label{Time: at, Parent: w.Append(parent, c.Arrival)}
					}
				}
			}
		}
		if best.Time < bestArrival {
			bestArrival = best.Time
			out.Journeys = append(out.Journeys, paper.Journey(best.Parent))
		}
		previous = next
	}
	return
}

func (x *Index) frozenMultiV1(ctx context.Context, q model.Query, cfg MultiConfig) (out MultiResult, err error) {
	answers := []multiAnswer{}
	defer func() {
		out.Journeys = []model.Journey{}
		sort.Slice(answers, func(i, j int) bool {
			a, b := answers[i], answers[j]
			if a.boards != b.boards {
				return a.boards < b.boards
			}
			if a.time != b.time {
				return a.time < b.time
			}
			return a.walk < b.walk
		})
		for _, a := range answers {
			out.Journeys = append(out.Journeys, paper.Journey(a.parent))
		}
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}()
	if cfg.Criterion != "walking_seconds" || cfg.MaxLabels <= 0 || cfg.MaxConnectionVisits <= 0 {
		err = fmt.Errorf("McRAPTOR capability: walking_seconds criterion and positive budgets required")
		return
	}
	if err = x.p.CheckContext(ctx, q); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if !x.p.Accessible(q, q.Origin) || !x.p.Accessible(q, q.Destination) {
		return
	}
	if q.Origin == q.Destination {
		answers = append(answers, multiAnswer{time: q.Departure})
		return
	}
	spend := func() error {
		if out.Stats.Labels == cfg.MaxLabels {
			return fmt.Errorf("McRAPTOR incomplete: label budget exhausted")
		}
		out.Stats.Labels++
		return nil
	}
	addStop := func(bag []multiLabel, c multiLabel, walk paper.Path, start int64) ([]multiLabel, bool, error) {
		for _, old := range bag {
			out.Stats.DominanceChecks++
			if old.time <= c.time && old.walk <= c.walk {
				return bag, false, nil
			}
		}
		if e := spend(); e != nil {
			return bag, false, e
		}
		kept := bag[:0]
		for _, old := range bag {
			if !(c.time <= old.time && c.walk <= old.walk) {
				kept = append(kept, old)
			}
		}
		// Rejected labels need no walking witness; admitted labels retain the
		// exact same path, clocks and insertion order.
		c.parent = walk.Append(c.parent, start)
		return append(kept, c), true, nil
	}
	addRoute := func(bag []routeLabel, c routeLabel) ([]routeLabel, error) {
		for _, old := range bag {
			out.Stats.DominanceChecks++
			if old.trip <= c.trip && old.walk <= c.walk {
				return bag, nil
			}
		}
		if e := spend(); e != nil {
			return bag, e
		}
		kept := bag[:0]
		for _, old := range bag {
			if !(c.trip <= old.trip && c.walk <= old.walk) {
				kept = append(kept, old)
			}
		}
		return append(kept, c), nil
	}
	addAnswer := func(c multiAnswer, walk paper.Path, start int64) error {
		if c.time > q.LatestArrival {
			return nil
		}
		for _, old := range answers {
			out.Stats.DominanceChecks++
			if old.time <= c.time && old.boards <= c.boards && old.walk <= c.walk {
				return nil
			}
		}
		if e := spend(); e != nil {
			return e
		}
		kept := answers[:0]
		for _, old := range answers {
			if !(c.time <= old.time && c.boards <= old.boards && c.walk <= old.walk) {
				kept = append(kept, old)
			}
		}
		c.parent = walk.Append(c.parent, start)
		answers = append(kept, c)
		return nil
	}
	walker := x.p.Walker(q)
	walks := orderedWalks{walker: walker, enabled: cfg.EarlyPruning, cache: map[string][]paper.Path{}}
	dominated := func(at, walk int64, boards int) bool {
		for _, a := range answers {
			out.Stats.EarlyPruning.DominanceChecks++
			if a.time <= at && a.walk <= walk && a.boards <= boards {
				return true
			}
		}
		return false
	}
	defer func() { out.Stats.WalkClosures = walker.Count }()
	paths, e := walks.paths(ctx, q.Origin)
	if e != nil {
		err = e
		return
	}
	previous := map[string][]multiLabel{}
	for i, w := range paths {
		at := q.Departure + w.Duration
		if cfg.EarlyPruning {
			out.Stats.EarlyPruning.Checks++
			if dominated(at, w.Duration, 0) {
				out.Stats.EarlyPruning.skip(len(paths) - i)
				break
			}
		}
		if at > q.LatestArrival {
			continue
		}
		if w.To == q.Destination {
			if w.Duration <= q.Walking.Direct {
				if err = addAnswer(multiAnswer{at, w.Duration, 0, nil}, w, q.Departure); err != nil {
					return
				}
			}
			continue
		}
		if w.Duration <= q.Walking.Access {
			var e error
			previous[w.To], _, e = addStop(previous[w.To], multiLabel{at, w.Duration, nil}, w, q.Departure)
			if e != nil {
				err = e
				return
			}
		}
	}
	for boards := 1; boards <= q.MaxBoardings && len(previous) > 0; boards++ {
		if err = ctx.Err(); err != nil {
			return
		}
		out.Stats.Rounds++
		queue := map[int]int{}
		for s := range previous {
			for _, o := range x.at[s] {
				p, ok := queue[o.route]
				if !ok || o.position < p {
					queue[o.route] = o.position
				}
			}
		}
		routes := []int{}
		for r := range queue {
			routes = append(routes, r)
		}
		sort.Ints(routes)
		next := map[string][]multiLabel{}
		for _, r := range routes {
			out.Stats.Routes++
			route := x.routes[r]
			bag := []routeLabel{}
			for i := queue[r]; i < len(route.trips[0].cs); i++ {
				if err = ctx.Err(); err != nil {
					return
				}
				prototype := route.trips[0].cs[i]
				if prototype.Pickup && x.p.Accessible(q, prototype.From) {
					for _, label := range previous[prototype.From] {
						t := sort.Search(len(route.trips), func(j int) bool { return route.trips[j].cs[i].Departure >= label.time })
						if t == len(route.trips) || !x.p.TripAllowed(q, route.trips[t].id) {
							continue
						}
						if i > 0 && label.parent != nil && label.parent.Leg.Kind == "ride" && label.parent.Leg.Ref == route.trips[t].cs[i-1].ID {
							continue
						}
						bag, e = addRoute(bag, routeLabel{t, label.walk, label.parent})
						if e != nil {
							err = e
							return
						}
					}
				}
				// Compact in place: the write position never exceeds the read
				// position, and labels are copied by value before appending.
				continuing := bag[:0]
				for _, l := range bag {
					if err = ctx.Err(); err != nil {
						return
					}
					if out.Stats.Connections == cfg.MaxConnectionVisits {
						err = fmt.Errorf("McRAPTOR incomplete: connection budget exhausted")
						return
					}
					out.Stats.Connections++
					c := route.trips[l.trip].cs[i]
					if c.Arrival > q.LatestArrival {
						continue
					}
					l.parent = paper.Ride(l.parent, c)
					if !c.Dropoff || !x.p.Accessible(q, c.To) {
						continuing = append(continuing, l)
						continue
					}
					if c.To == q.Destination {
						if err = addAnswer(multiAnswer{c.Arrival, l.walk, boards, l.parent}, paper.Path{}, 0); err != nil {
							return
						}
						continue
					}
					continuing = append(continuing, l)
					paths, e := walks.paths(ctx, c.To)
					if e != nil {
						err = e
						return
					}
					for i, w := range paths {
						at := c.Arrival + w.Duration
						if cfg.EarlyPruning {
							out.Stats.EarlyPruning.Checks++
							if dominated(at, l.walk+w.Duration, boards) {
								out.Stats.EarlyPruning.skip(len(paths) - i)
								break
							}
						}
						if at > q.LatestArrival {
							continue
						}
						if w.To == q.Destination {
							if w.Duration <= q.Walking.Egress {
								if err = addAnswer(multiAnswer{at, l.walk + w.Duration, boards, l.parent}, w, c.Arrival); err != nil {
									return
								}
							}
							continue
						}
						if boards == q.MaxBoardings || w.Duration > q.Walking.Transfer {
							continue
						}
						at, allowed, e := x.p.ReadyTime(q, c.To, w.To, c.Arrival, w.Duration)
						if e != nil {
							err = e
							return
						}
						if !allowed || at > q.LatestArrival {
							continue
						}
						newBag, _, e := addStop(next[w.To], multiLabel{at, l.walk + w.Duration, l.parent}, w, c.Arrival)
						if e != nil {
							err = e
							return
						}
						next[w.To] = newBag
					}
				}
				clear(bag[len(continuing):])
				bag = continuing
			}
		}
		previous = next
	}
	return
}
