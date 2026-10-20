// Package capverify performs bounded exhaustive completeness verification.
// It intentionally scans every input event and shares no transition/pruning code with validators or routers.
package capverify

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
)

type Limits struct {
	MaxScans    int `json:"max_scans"`
	MaxStates   int `json:"max_states"`
	MaxJourneys int `json:"max_journeys"`
}
type Result struct {
	Journeys []model.Journey `json:"journeys"`
	Complete bool            `json:"complete"`
	Scans    int             `json:"scans"`
	States   int             `json:"states"`
}
type TruncatedError struct{ Limit string }

func (e *TruncatedError) Error() string {
	return "oracle inconclusive: enumeration limit reached: " + e.Limit
}

func Enumerate(ctx context.Context, d model.Dataset, q model.Query, limits Limits) (Result, error) {
	out := Result{Journeys: []model.Journey{}}
	// The owning prepared index validates the dataset once; original-query
	// binding/replay validates q before entering this internal verifier.
	if limits.MaxScans <= 0 || limits.MaxStates <= 0 || limits.MaxJourneys <= 0 {
		return out, fmt.Errorf("oracle budgets must be positive")
	}
	scan := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if out.Scans >= limits.MaxScans {
			return &TruncatedError{"scans"}
		}
		out.Scans++
		return nil
	}
	stopOK := func(id string) bool {
		if !q.AccessibleOnly {
			return true
		}
		for _, s := range d.Stops {
			if s.ID == id {
				return s.Accessible == model.Yes
			}
		}
		return false
	}
	tripOK := func(id string) bool {
		if !q.AccessibleOnly {
			return true
		}
		for _, t := range d.Trips {
			if t.ID == id {
				return t.Accessible == model.Yes
			}
		}
		return false
	}
	if !stopOK(q.Origin) || !stopOK(q.Destination) {
		out.Complete = true
		return out, nil
	}
	type state struct {
		lastRide             int
		blockWalk            int64
		stop                 string
		time, walk, lastExit int64
		boards               int
		onboard              int
		legs                 []model.Leg
	}
	var visit func(state) error
	visit = func(s state) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if out.States >= limits.MaxStates {
			return &TruncatedError{"states"}
		}
		out.States++
		exitOK := s.onboard < 0 || (d.Connections[s.onboard].Dropoff && stopOK(s.stop))
		if s.stop == q.Destination && exitOK {
			if q.Walking != nil {
				cap := q.Walking.Egress
				if s.boards == 0 {
					cap = q.Walking.Direct
				}
				if s.blockWalk > cap {
					return nil
				}
			}
			if len(out.Journeys) >= limits.MaxJourneys {
				return &TruncatedError{"journeys"}
			}
			out.Journeys = append(out.Journeys, model.Journey{Legs: append([]model.Leg{}, s.legs...)})
			return nil
		}
		if len(s.legs) >= q.MaxLegs {
			return nil
		}
		for i, r := range d.Connections {
			if e := scan(); e != nil {
				return e
			}
			if r.From != s.stop || r.Departure < s.time || r.Arrival > q.LatestArrival {
				continue
			}
			continuing := false
			if s.onboard >= 0 {
				prior := d.Connections[s.onboard]
				continuing = r.TripID == prior.TripID && r.Sequence == prior.Sequence+1
			}
			boards, lastExit := s.boards, s.lastExit
			if !continuing {
				if !exitOK || !r.Pickup || !stopOK(r.From) || !tripOK(r.TripID) {
					continue
				}
				if s.onboard >= 0 {
					lastExit = d.Connections[s.onboard].Arrival
				}
				if q.Walking != nil {
					cap := q.Walking.Transfer
					if boards == 0 {
						cap = q.Walking.Access
					}
					if s.blockWalk > cap {
						continue
					}
				}
				if boards > 0 {
					for n := 0; n < len(d.Transfers)*(4*len(d.Stops)+1)+len(d.Trips); n++ {
						if e := scan(); e != nil {
							return e
						}
					}
					allowed, minimum, err := referenceRule(d, d.Connections[s.lastRide], r, q.TransferBuffer)
					if err != nil {
						return err
					}
					if !allowed || r.Departure < lastExit+minimum {
						continue
					}
				}
				boards++
			}
			if boards > q.MaxBoardings {
				continue
			}
			legs := append(append([]model.Leg{}, s.legs...), model.Leg{Kind: "ride", Ref: r.ID, Departure: r.Departure, Arrival: r.Arrival})
			if err := visit(state{stop: r.To, time: r.Arrival, walk: s.walk, lastExit: lastExit, boards: boards, onboard: i, lastRide: i, blockWalk: 0, legs: legs}); err != nil {
				return err
			}
		}
		if exitOK {
			for _, w := range d.Footpaths {
				if e := scan(); e != nil {
					return e
				}
				if w.From != s.stop || s.walk+w.Duration > q.MaxWalkSeconds || s.time+w.Duration > q.LatestArrival {
					continue
				}
				if !stopOK(w.From) || !stopOK(w.To) || (q.AccessibleOnly && w.Accessible != model.Yes) {
					continue
				}
				if q.Walking != nil {
					cap := max(q.Walking.Transfer, q.Walking.Egress)
					if s.boards == 0 {
						cap = max(q.Walking.Access, q.Walking.Direct)
					}
					if s.blockWalk+w.Duration > cap {
						continue
					}
				}
				lastExit := s.lastExit
				if s.onboard >= 0 {
					lastExit = d.Connections[s.onboard].Arrival
				}
				legs := append(append([]model.Leg{}, s.legs...), model.Leg{Kind: "walk", Ref: w.ID, Departure: s.time, Arrival: s.time + w.Duration})
				if err := visit(state{stop: w.To, time: s.time + w.Duration, walk: s.walk + w.Duration, lastExit: lastExit, boards: s.boards, onboard: -1, lastRide: s.lastRide, blockWalk: s.blockWalk + w.Duration, legs: legs}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := visit(state{stop: q.Origin, time: q.Departure, onboard: -1, lastRide: -1})
	out.Complete = err == nil
	return out, err
}

// referenceRule is intentionally a full independent scan, not the production resolver.
func referenceRule(d model.Dataset, arriving, departing model.Connection, fallback int64) (bool, int64, error) {
	var a, b model.Trip
	for _, t := range d.Trips {
		if t.ID == arriving.TripID {
			a = t
		}
		if t.ID == departing.TripID {
			b = t
		}
	}
	aid, bid := a.SourceID, b.SourceID
	if aid == "" {
		aid = a.ID
	}
	if bid == "" {
		bid = b.ID
	}
	matchStop := func(rule, actual string) bool {
		if rule == actual {
			return true
		}
		for _, s := range d.Stops {
			if s.ID == actual && s.ParentStation == rule {
				for _, p := range d.Stops {
					if p.ID == rule && p.LocationType == 1 {
						return true
					}
				}
			}
		}
		return false
	}
	top := 0
	selected := []model.TransferRule{}
	for _, r := range d.Transfers {
		if !matchStop(r.FromStop, arriving.To) || !matchStop(r.ToStop, departing.From) {
			continue
		}
		if r.FromTrip != "" && r.FromTrip != aid || r.ToTrip != "" && r.ToTrip != bid || r.FromRoute != "" && r.FromRoute != a.RouteID || r.ToRoute != "" && r.ToRoute != b.RouteID {
			continue
		}
		level := 1
		if r.FromRoute != "" || r.ToRoute != "" {
			level = 2
		}
		if r.FromRoute != "" && r.ToRoute != "" {
			level = 3
		}
		if r.FromTrip != "" || r.ToTrip != "" {
			level = 4
		}
		if r.FromTrip != "" && r.ToRoute != "" || r.ToTrip != "" && r.FromRoute != "" {
			level = 5
		}
		if r.FromTrip != "" && r.ToTrip != "" {
			level = 6
		}
		if level > top {
			top = level
			selected = nil
		}
		if level == top {
			selected = append(selected, r)
		}
	}
	if len(selected) > 1 {
		return false, 0, fmt.Errorf("oracle: ambiguous maximal transfer rules")
	}
	if len(selected) == 0 {
		return true, fallback, nil
	}
	r := selected[0]
	minimum := int64(0)
	if r.Type == 2 {
		minimum = r.MinSeconds
	}
	return r.Type != 3, minimum, nil
}
