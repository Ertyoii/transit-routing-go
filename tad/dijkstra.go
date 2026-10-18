// Package tad implements the unaccelerated transfer-aware Dijkstra trip scan.
// Source mapping and declared state expansion: docs/TAD_CONTRACT.md.
package tad

import (
	"container/heap"
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type occurrence struct {
	trip      string
	position  int
	departure int64
	id        string
}
type Index struct {
	p     *paper.Profile
	trips map[string][]model.Connection
	at    map[string][]occurrence
}

func New(d model.Dataset) (*Index, error) {
	p, e := paper.New(d)
	if e != nil {
		return nil, e
	}
	x := &Index{p: p, trips: map[string][]model.Connection{}, at: map[string][]occurrence{}}
	for _, c := range p.Data.Connections {
		x.trips[c.TripID] = append(x.trips[c.TripID], c)
	}
	for id, cs := range x.trips {
		sort.Slice(cs, func(i, j int) bool { return cs[i].Sequence < cs[j].Sequence })
		x.trips[id] = cs
		for i, c := range cs {
			x.at[c.From] = append(x.at[c.From], occurrence{id, i, c.Departure, c.ID})
		}
	}
	for s, os := range x.at {
		sort.Slice(os, func(i, j int) bool {
			if os[i].departure != os[j].departure {
				return os[i].departure < os[j].departure
			}
			return os[i].id < os[j].id
		})
		x.at[s] = os
	}
	return x, nil
}
func (x *Index) Capabilities() paper.Capability { return x.p.Capability }

type Config struct {
	MaxLabels           int `json:"max_label_insertions"`
	MaxConnectionVisits int `json:"max_connection_visits"`
	MaxWalkVisits       int `json:"max_walk_edge_visits"`
}
type Stats struct {
	Labels          int `json:"label_insertions"`
	Settled         int `json:"settled_states"`
	Connections     int `json:"connection_visits"`
	TripScans       int `json:"trip_scans"`
	VehicleSkips    int `json:"already_scanned_vehicle_suffixes"`
	WalkEdges       int `json:"walk_edge_visits"`
	DominanceChecks int `json:"dominance_checks"`
}
type Result struct {
	Journeys []model.Journey `json:"journeys"`
	Complete bool            `json:"complete"`
	Error    string          `json:"error,omitempty"`
	Stats    Stats           `json:"stats"`
}
type stateKey struct {
	stop   string
	boards int
	alight string
}
type label struct {
	key                    stateKey
	time, alightTime, walk int64
	parent                 *paper.Node
	serial                 int
	active                 bool
}
type queue []*label

func (q queue) Len() int { return len(q) }
func (q queue) Less(i, j int) bool {
	if q[i].time != q[j].time {
		return q[i].time < q[j].time
	}
	return q[i].serial < q[j].serial
}
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(v any)   { *q = append(*q, v.(*label)) }
func (q *queue) Pop() any     { old := *q; v := old[len(old)-1]; *q = old[:len(old)-1]; return v }

type vehicleKey struct {
	trip   string
	boards int
}

// Search returns one minimum-arrival witness under a hard boarding cap. It does
// not promise fewer boardings among equal arrivals or a full Pareto frontier.
// ScanTrip always follows the entire seated suffix, even if a stop arrival is
// dominated. Raw walking edges are relaxed directly; there is no metric closure.
func (x *Index) Search(ctx context.Context, q model.Query, cfg Config) (out Result, err error) {
	best := paper.Infinity
	var answer *paper.Node
	defer func() {
		out.Journeys = []model.Journey{}
		if best != paper.Infinity {
			out.Journeys = append(out.Journeys, paper.Journey(answer))
		}
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}()
	if cfg.MaxLabels <= 0 || cfg.MaxConnectionVisits <= 0 || cfg.MaxWalkVisits <= 0 {
		err = fmt.Errorf("TAD capability: positive label, connection and walking budgets required")
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
		best = q.Departure
		return
	}
	bags := map[stateKey][]*label{}
	h := &queue{}
	vehicleStart := map[vehicleKey]int{}
	add := func(c *label) error {
		if c.time > q.LatestArrival || c.time >= best {
			return nil
		}
		if c.key.stop == q.Destination {
			cap := q.Walking.Egress
			if c.key.boards == 0 {
				cap = q.Walking.Direct
			}
			if c.walk <= cap {
				best = c.time
				answer = c.parent
			}
			// Destination is absorbing, even when this particular walking block exceeds
			// its terminal role limit. It cannot be used as an intermediate waypoint.
			return nil
		}
		bag := bags[c.key]
		for _, a := range bag {
			out.Stats.DominanceChecks++
			if a.time <= c.time && a.alightTime <= c.alightTime && a.walk <= c.walk {
				return nil
			}
		}
		if out.Stats.Labels == cfg.MaxLabels {
			return fmt.Errorf("TAD incomplete: label budget exhausted")
		}
		out.Stats.Labels++
		c.serial = out.Stats.Labels
		c.active = true
		kept := bag[:0]
		for _, a := range bag {
			if c.time <= a.time && c.alightTime <= a.alightTime && c.walk <= a.walk {
				a.active = false
			} else {
				kept = append(kept, a)
			}
		}
		bags[c.key] = append(kept, c)
		heap.Push(h, c)
		return nil
	}
	if err = add(&label{key: stateKey{stop: q.Origin}, time: q.Departure, alightTime: q.Departure}); err != nil {
		return
	}
	for h.Len() > 0 {
		if err = ctx.Err(); err != nil {
			return
		}
		u := heap.Pop(h).(*label)
		if !u.active {
			continue
		}
		if u.time >= best {
			break
		}
		out.Stats.Settled++
		// Scan every catchable trip occurrence. Non-FIFO vehicle options are never
		// discarded based on their immediate next-stop arrival.
		boardingLimit := q.Walking.Access
		if u.key.boards > 0 {
			boardingLimit = q.Walking.Transfer
		}
		if u.key.boards < q.MaxBoardings && u.walk <= boardingLimit {
			ready := u.time
			allowed := true
			if u.key.boards > 0 {
				ready, allowed, err = x.p.ReadyTime(q, u.key.alight, u.key.stop, u.alightTime, u.walk)
				if err != nil {
					return
				}
			}
			if allowed {
				os := x.at[u.key.stop]
				begin := sort.Search(len(os), func(i int) bool { return os[i].departure >= ready })
				for _, o := range os[begin:] {
					if err = ctx.Err(); err != nil {
						return
					}
					if o.departure >= best || o.departure > q.LatestArrival {
						break
					}
					cs := x.trips[o.trip]
					first := cs[o.position]
					if !first.Pickup || !x.p.TripAllowed(q, o.trip) {
						continue
					}
					// A contiguous seated continuation has already been scanned in the prior
					// boarding layer; do not manufacture a new boarding of that same segment.
					if u.parent != nil && u.parent.Leg.Kind == "ride" && o.position > 0 && u.parent.Leg.Ref == cs[o.position-1].ID {
						continue
					}
					key := vehicleKey{o.trip, u.key.boards + 1}
					prior, seen := vehicleStart[key]
					if seen && prior <= o.position {
						out.Stats.VehicleSkips++
						continue
					}
					vehicleStart[key] = o.position
					out.Stats.TripScans++
					parent := u.parent
					for i := o.position; i < len(cs); i++ {
						if seen && i >= prior {
							break
						}
						if err = ctx.Err(); err != nil {
							return
						}
						c := cs[i]
						if c.Arrival >= best || c.Arrival > q.LatestArrival {
							break
						}
						if out.Stats.Connections == cfg.MaxConnectionVisits {
							err = fmt.Errorf("TAD incomplete: connection budget exhausted")
							return
						}
						out.Stats.Connections++
						parent = paper.Ride(parent, c)
						if c.Dropoff && x.p.Accessible(q, c.To) {
							if err = add(&label{key: stateKey{c.To, u.key.boards + 1, c.To}, time: c.Arrival, alightTime: c.Arrival, parent: parent}); err != nil {
								return
							}
							if c.To == q.Destination {
								break
							}
						}
					}
				}
			}
		}
		walkCap := max(q.Walking.Access, q.Walking.Direct)
		if u.key.boards > 0 {
			walkCap = max(q.Walking.Transfer, q.Walking.Egress)
		}
		for _, w := range x.p.Outgoing[u.key.stop] {
			if err = ctx.Err(); err != nil {
				return
			}
			if out.Stats.WalkEdges == cfg.MaxWalkVisits {
				err = fmt.Errorf("TAD incomplete: walking budget exhausted")
				return
			}
			out.Stats.WalkEdges++
			if !x.p.Accessible(q, w.To) || q.AccessibleOnly && w.Accessible != model.Yes || u.walk+w.Duration > walkCap {
				continue
			}
			parent := &paper.Node{Previous: u.parent, Leg: model.Leg{Kind: "walk", Ref: w.ID, Departure: u.time, Arrival: u.time + w.Duration}}
			if err = add(&label{key: stateKey{w.To, u.key.boards, u.key.alight}, time: u.time + w.Duration, alightTime: u.alightTime, walk: u.walk + w.Duration, parent: parent}); err != nil {
				return
			}
		}
	}
	return
}
