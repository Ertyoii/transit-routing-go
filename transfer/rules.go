// Package transfer implements production GTFS transfer precedence for static types 0,2,3.
// Oracle and witness validation intentionally implement their own rule interpretation.
package transfer

import (
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
)

type Resolver struct {
	stops  map[string]model.Stop
	trips  map[string]model.Trip
	rules  []model.TransferRule
	byFrom map[string][]int
}
type Decision struct {
	Allowed bool
	Minimum int64
	RuleID  string
}

func New(d model.Dataset) *Resolver {
	r := &Resolver{stops: map[string]model.Stop{}, trips: map[string]model.Trip{}, rules: append([]model.TransferRule{}, d.Transfers...), byFrom: map[string][]int{}}
	for _, s := range d.Stops {
		r.stops[s.ID] = s
	}
	for _, t := range d.Trips {
		r.trips[t.ID] = t
	}
	// Keep candidate rules in their original order. Expanding station origins
	// once preserves direct-stop and parent matching without scanning unrelated
	// rules on every transfer. Destination and trip/route precedence stay below.
	children := map[string][]string{}
	for id, s := range r.stops {
		children[s.ParentStation] = append(children[s.ParentStation], id)
	}
	for i, rule := range r.rules {
		r.byFrom[rule.FromStop] = append(r.byFrom[rule.FromStop], i)
		if r.stops[rule.FromStop].LocationType == 1 {
			for _, id := range children[rule.FromStop] {
				if id != rule.FromStop {
					r.byFrom[id] = append(r.byFrom[id], i)
				}
			}
		}
	}
	return r
}
func (r *Resolver) Resolve(from, to model.Connection, defaultMinimum int64) (Decision, error) {
	fromTrip, toTrip := r.trips[from.TripID], r.trips[to.TripID]
	source := func(t model.Trip) string {
		if t.SourceID != "" {
			return t.SourceID
		}
		return t.ID
	}
	stopMatch := func(selector, actual string) bool {
		return selector == actual || (r.stops[selector].LocationType == 1 && r.stops[actual].ParentStation == selector)
	}
	best := 0
	ambiguous := false
	decision := Decision{Allowed: true, Minimum: defaultMinimum}
	for _, i := range r.byFrom[from.To] {
		rule := r.rules[i]
		if !stopMatch(rule.ToStop, to.From) {
			continue
		}
		if rule.FromTrip != "" && rule.FromTrip != source(fromTrip) || rule.ToTrip != "" && rule.ToTrip != source(toTrip) || rule.FromRoute != "" && rule.FromRoute != fromTrip.RouteID || rule.ToRoute != "" && rule.ToRoute != toTrip.RouteID {
			continue
		}
		priority := 1
		switch {
		case rule.FromTrip != "" && rule.ToTrip != "":
			priority = 6
		case rule.FromTrip != "" && rule.ToRoute != "" || rule.ToTrip != "" && rule.FromRoute != "":
			priority = 5
		case rule.FromTrip != "" || rule.ToTrip != "":
			priority = 4
		case rule.FromRoute != "" && rule.ToRoute != "":
			priority = 3
		case rule.FromRoute != "" || rule.ToRoute != "":
			priority = 2
		}
		if priority < best {
			continue
		}
		if priority == best {
			ambiguous = true
			continue
		}
		best = priority
		ambiguous = false
		decision = Decision{Allowed: rule.Type != 3, RuleID: rule.ID}
		if rule.Type == 2 {
			decision.Minimum = rule.MinSeconds
		}
	}
	if ambiguous {
		return Decision{}, fmt.Errorf("ambiguous maximum-specificity transfer rules at %q", decision.RuleID)
	}
	return decision, nil
}
