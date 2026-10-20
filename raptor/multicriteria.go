package raptor

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type MultiConfig struct {
	EarlyPruning        bool   `json:"early_pruning"`
	Criterion           string `json:"additional_criterion"`
	MaxLabels           int    `json:"max_label_insertions"`
	MaxConnectionVisits int    `json:"max_connection_visits"`
}
type MultiStats struct {
	EarlyPruning    EarlyPruningStats `json:"early_pruning"`
	Rounds          int               `json:"rounds"`
	Routes          int               `json:"route_scans"`
	Connections     int               `json:"connection_visits"`
	Labels          int               `json:"label_insertions"`
	DominanceChecks int               `json:"dominance_checks"`
	WalkClosures    int               `json:"walk_closures"`
}
type MultiResult struct {
	Journeys []model.Journey `json:"journeys"`
	Complete bool            `json:"complete"`
	Error    string          `json:"error,omitempty"`
	Stats    MultiStats      `json:"stats"`
}
type multiLabel struct {
	time, walk int64
	parent     *paper.Node
}
type routeLabel struct {
	trip   int
	walk   int64
	parent *paper.Node
}
type multiAnswer struct {
	time, walk int64
	boards     int
	parent     *paper.Node
}

// SearchMulti is McRAPTOR's route/stop-bag algorithm (ALENEX 2012, Section 4.1)
// instantiated with total walking seconds as the additional monotone criterion.
// It is a fixed-departure, three-objective Pareto query, never strict top-k.
func (x *Index) SearchMulti(ctx context.Context, q model.Query, cfg MultiConfig) (out MultiResult, err error) {
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
	previous := make([][]multiLabel, len(x.atStop))
	previousStops := []int{}
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
			stop := x.stopIDs[w.To]
			wasEmpty := len(previous[stop]) == 0
			previous[stop], _, e = addStop(previous[stop], multiLabel{at, w.Duration, nil}, w, q.Departure)
			if wasEmpty && len(previous[stop]) > 0 {
				previousStops = append(previousStops, stop)
			}
			if e != nil {
				err = e
				return
			}
		}
	}
	for boards := 1; boards <= q.MaxBoardings && len(previousStops) > 0; boards++ {
		if err = ctx.Err(); err != nil {
			return
		}
		out.Stats.Rounds++
		queue, routes := x.collectRoutes(previousStops)
		next := make([][]multiLabel, len(x.atStop))
		nextStops := []int{}
		for _, r := range routes {
			out.Stats.Routes++
			route := x.routes[r]
			bag := []routeLabel{}
			for i := queue[r] - 1; i < len(route.trips[0].cs); i++ {
				if err = ctx.Err(); err != nil {
					return
				}
				prototype := route.trips[0].cs[i]
				if prototype.Pickup && x.p.Accessible(q, prototype.From) {
					for _, label := range previous[route.from[i]] {
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
						stop := x.stopIDs[w.To]
						newBag, _, e := addStop(next[stop], multiLabel{at, l.walk + w.Duration, l.parent}, w, c.Arrival)
						if e != nil {
							err = e
							return
						}
						if len(next[stop]) == 0 && len(newBag) > 0 {
							nextStops = append(nextStops, stop)
						}
						next[stop] = newBag
					}
				}
				clear(bag[len(continuing):])
				bag = continuing
			}
		}
		previous, previousStops = next, nextStops
	}
	return
}
