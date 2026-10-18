// Package profilecsa implements the decreasing-departure profile scan from
// Dibbelt et al., Figures 7/8/11, with explicit boarding-cap vector components.
package profilecsa

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"github.com/ertyoii/transit-routing-go/profilequery"
	"sort"
)

type Index struct {
	p           *paper.Profile
	connections []model.Connection
}

func New(d model.Dataset) (*Index, error) {
	p, e := paper.New(d)
	if e != nil {
		return nil, e
	}
	cs := append([]model.Connection{}, p.Data.Connections...)
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Departure != cs[j].Departure {
			return cs[i].Departure > cs[j].Departure
		}
		return cs[i].ID < cs[j].ID
	})
	return &Index{p, cs}, nil
}
func (x *Index) Capabilities() paper.Capability { return x.p.Capability }

type suffix struct {
	leg  model.Leg
	next *suffix
}
type label struct {
	departure, arrival int64
	boards             int
	path               *suffix
}

func better(a, b label) bool {
	return a.boards > 0 && (b.boards == 0 || a.arrival < b.arrival || a.arrival == b.arrival && a.boards < b.boards)
}
func prependWalk(w paper.Path, start int64, tail *suffix) *suffix {
	end := start + w.Duration
	for i := len(w.Edges) - 1; i >= 0; i-- {
		edge := w.Edges[i]
		tail = &suffix{model.Leg{Kind: "walk", Ref: edge.ID, Departure: end - edge.Duration, Arrival: end}, tail}
		end -= edge.Duration
	}
	return tail
}
func journey(s *suffix) model.Journey {
	j := model.Journey{Legs: []model.Leg{}}
	for ; s != nil; s = s.next {
		j.Legs = append(j.Legs, s.leg)
	}
	return j
}
func (x *Index) Search(ctx context.Context, q profilequery.Query, limits profilequery.Limits) (out profilequery.Result, err error) {
	out.Query = q
	out.Points = []profilequery.Point{}
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		} else {
			out.Finalize()
		}
	}()
	if err = q.CheckContext(ctx, x.p, limits); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	bq := q.Base
	if !x.p.Accessible(bq, bq.Origin) || !x.p.Accessible(bq, bq.Destination) {
		return
	}
	walker := x.p.Walker(bq)
	defer func() { out.Stats.WalkClosures = walker.Count }()
	access, e := walker.Paths(ctx, bq.Origin)
	if e != nil {
		err = e
		return
	}
	for _, w := range access {
		if w.To == bq.Destination && w.Duration <= bq.Walking.Direct && bq.Departure+w.Duration <= bq.LatestArrival {
			out.Direct = &profilequery.Direct{Duration: w.Duration, Journey: paper.Journey(w.Append(nil, bq.Departure))}
		}
	}
	if bq.Origin == bq.Destination || bq.MaxBoardings == 0 {
		return
	}
	// stop[b][s] is a decreasing-departure staircase for boarding at s with
	// at most b boardings. Walking/minima are applied by the arriving event;
	// access is kept separate, so it never inherits a transfer buffer.
	stop := make([]map[string][]label, bq.MaxBoardings+1)
	onboard := make([]map[string]label, bq.MaxBoardings+1)
	lastSeq := map[string]int{}
	for b := range stop {
		stop[b] = map[string][]label{}
		onboard[b] = map[string]label{}
	}
	for _, c := range x.connections {
		if err = ctx.Err(); err != nil {
			return
		}
		if c.Departure < bq.Departure {
			break
		}
		if c.Arrival > bq.LatestArrival || !x.p.TripAllowed(bq, c.TripID) {
			continue
		}
		paths := []paper.Path{}
		alight := c.Dropoff && x.p.Accessible(bq, c.To)
		if alight {
			paths, e = walker.Paths(ctx, c.To)
			if e != nil {
				err = e
				return
			}
		}
		for b := 1; b <= bq.MaxBoardings; b++ {
			if out.Stats.Connections == limits.MaxConnectionVisits {
				err = fmt.Errorf("profile CSA incomplete: connection budget exhausted")
				return
			}
			out.Stats.Connections++
			best := label{}
			if seq, ok := lastSeq[c.TripID]; ok && seq == c.Sequence+1 && !(c.To == bq.Destination && alight) {
				best = onboard[b][c.TripID]
			}
			for _, w := range paths {
				if w.To == bq.Destination {
					arrival := c.Arrival + w.Duration
					if w.Duration <= bq.Walking.Egress && arrival <= bq.LatestArrival {
						// A nil egress suffix is valid for an immediate arrival.
						if best.boards == 0 || arrival < best.arrival || arrival == best.arrival && best.boards > 1 {
							best = label{arrival: arrival, boards: 1, path: prependWalk(w, c.Arrival, nil)}
						}
					}
					continue
				}
				if b <= 1 || w.Duration > bq.Walking.Transfer {
					continue
				}
				ready, allowed, e := x.p.ReadyTime(bq, c.To, w.To, c.Arrival, w.Duration)
				if e != nil {
					err = e
					return
				}
				if !allowed {
					continue
				}
				s := stop[b-1][w.To]
				out.Stats.ProfileEvaluations++
				pos := sort.Search(len(s), func(i int) bool { return s[i].departure < ready }) - 1
				if pos < 0 {
					continue
				}
				cand := s[pos]
				cand.boards++
				cand.path = prependWalk(w, c.Arrival, cand.path)
				if best.boards == 0 || better(cand, best) {
					best = cand
				}
			}
			if best.boards == 0 {
				delete(onboard[b], c.TripID)
				continue
			}
			if out.Stats.Labels == limits.MaxLabels {
				err = fmt.Errorf("profile CSA incomplete: label budget exhausted")
				return
			}
			out.Stats.Labels++
			best.path = &suffix{model.Leg{Kind: "ride", Ref: c.ID, Departure: c.Departure, Arrival: c.Arrival}, best.path}
			best.departure = c.Departure
			onboard[b][c.TripID] = best
			if !c.Pickup || !x.p.Accessible(bq, c.From) || c.From == bq.Destination {
				continue
			}
			s := stop[b][c.From]
			if len(s) == 0 || better(best, s[len(s)-1]) {
				if len(s) > 0 && s[len(s)-1].departure == best.departure {
					s[len(s)-1] = best
				} else {
					s = append(s, best)
				}
				stop[b][c.From] = s
			}
		}
		lastSeq[c.TripID] = c.Sequence
	}
	for _, w := range access {
		if w.To == bq.Destination || w.Duration > bq.Walking.Access {
			continue
		}
		for b := 1; b <= bq.MaxBoardings; b++ {
			for _, l := range stop[b][w.To] {
				ready := min(q.End, l.departure-w.Duration)
				if ready < bq.Departure {
					continue
				}
				out.Points = append(out.Points, profilequery.Point{LatestReady: ready, Arrival: l.arrival, Boardings: l.boards, Journey: journey(prependWalk(w, ready, l.path))})
			}
		}
	}
	return
}
