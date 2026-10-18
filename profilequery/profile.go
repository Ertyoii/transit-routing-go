// Package profilequery defines a ready-time profile contract and its materializer.
// It contains no timetable search. Independent specifications do not import its
// dominance or departure-candidate helpers.
package profilequery

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type Query struct {
	Base model.Query `json:"base"`
	End  int64       `json:"ready_window_end_inclusive"`
}
type Limits struct {
	MaxConnectionVisits int `json:"max_connection_visits"`
	MaxLabels           int `json:"max_label_updates"`
	MaxDepartures       int `json:"max_departure_iterations"`
}

func (q Query) Check(p *paper.Profile, limits Limits) error {
	return q.CheckContext(context.Background(), p, limits)
}
func (q Query) CheckContext(ctx context.Context, p *paper.Profile, limits Limits) error {
	if e := p.CheckContext(ctx, q.Base); e != nil {
		return e
	}
	if q.End < q.Base.Departure || q.End > q.Base.LatestArrival {
		return fmt.Errorf("profile: invalid inclusive ready-time window")
	}
	if limits.MaxConnectionVisits <= 0 || limits.MaxLabels <= 0 || limits.MaxDepartures <= 0 {
		return fmt.Errorf("profile: positive explicit budgets required")
	}
	return nil
}

type Point struct {
	LatestReady int64         `json:"latest_ready"`
	Arrival     int64         `json:"arrival"`
	Boardings   int           `json:"boardings"`
	Journey     model.Journey `json:"journey"`
}
type Direct struct {
	Duration int64         `json:"duration"`
	Journey  model.Journey `json:"journey_at_window_start"`
}
type Stats struct {
	DepartureEventChecks int `json:"departure_event_checks"`
	Connections          int `json:"connection_visits"`
	Labels               int `json:"label_updates"`
	Departures           int `json:"departure_iterations"`
	Routes               int `json:"route_scans"`
	Rounds               int `json:"rounds"`
	WalkClosures         int `json:"walk_closures"`
	ProfileEvaluations   int `json:"stop_profile_evaluations"`
}
type Result struct {
	Query    Query   `json:"query"`
	Points   []Point `json:"transit_points"`
	Direct   *Direct `json:"direct_walking,omitempty"`
	Complete bool    `json:"complete"`
	Error    string  `json:"error,omitempty"`
	Stats    Stats   `json:"stats"`
}

// Reanchor moves only the initial walking block. Waiting for the first vehicle
// remains implicit. Transit/intermediate/egress events retain their exact times.
func Reanchor(j model.Journey, ready int64) model.Journey {
	out := model.Journey{Legs: append([]model.Leg{}, j.Legs...)}
	for i := range out.Legs {
		if out.Legs[i].Kind != "walk" {
			break
		}
		duration := out.Legs[i].Arrival - out.Legs[i].Departure
		out.Legs[i].Departure = ready
		ready += duration
		out.Legs[i].Arrival = ready
	}
	return out
}

// Finalize keeps (-latest-ready, arrival, boardings) representatives, with a
// direct affine branch handled separately. Sorting and cap minima avoid an
// all-pairs dominance pass. It is shared presentation, not a routing oracle.
func (r *Result) Finalize() {
	sort.SliceStable(r.Points, func(i, j int) bool {
		a, b := r.Points[i], r.Points[j]
		if a.LatestReady != b.LatestReady {
			return a.LatestReady > b.LatestReady
		}
		if a.Arrival != b.Arrival {
			return a.Arrival < b.Arrival
		}
		return a.Boardings < b.Boardings
	})
	best := make([]int64, r.Query.Base.MaxBoardings+1)
	for i := range best {
		best[i] = paper.Infinity
	}
	kept := make([]Point, 0, len(r.Points))
	for _, p := range r.Points {
		if r.Direct != nil && p.LatestReady+r.Direct.Duration <= p.Arrival {
			continue
		}
		if best[p.Boardings] <= p.Arrival {
			continue
		}
		kept = append(kept, p)
		for k := p.Boardings; k < len(best); k++ {
			best[k] = min(best[k], p.Arrival)
		}
	}
	r.Points = kept
}

// At materializes the complete arrival/boarding frontier for one ready time.
// The input window bounds readiness, not the first vehicle departure. This is
// an exact lookup of a completed profile, never a fresh point search.
func (r Result) At(ready int64) ([]model.Journey, error) {
	if !r.Complete {
		return nil, fmt.Errorf("profile incomplete: %s", r.Error)
	}
	if ready < r.Query.Base.Departure || ready > r.Query.End {
		return nil, fmt.Errorf("profile lookup outside inclusive window")
	}
	best := make([]*Point, r.Query.Base.MaxBoardings+1)
	if r.Direct != nil && ready+r.Direct.Duration <= r.Query.Base.LatestArrival {
		best[0] = &Point{Arrival: ready + r.Direct.Duration, Journey: Reanchor(r.Direct.Journey, ready)}
	}
	for i := range r.Points {
		p := &r.Points[i]
		if p.LatestReady < ready {
			continue
		}
		if best[p.Boardings] == nil || p.Arrival < best[p.Boardings].Arrival {
			best[p.Boardings] = p
		}
	}
	out := []model.Journey{}
	arrival := paper.Infinity
	for _, p := range best {
		if p != nil && p.Arrival < arrival {
			out = append(out, Reanchor(p.Journey, ready))
			arrival = p.Arrival
		}
	}
	return out, nil
}
