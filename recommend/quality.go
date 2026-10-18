// Package recommend measures and selects from a supplied candidate pool. It makes
// no search-completeness, passenger-preference or physical-access claims.
package recommend

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/transfer"
	"github.com/ertyoii/transit-routing-go/validate"
)

type Recovery struct {
	Margin    int64  `json:"assumed_lateness_seconds"`
	Departure *int64 `json:"next_same_pattern_departure"`
	Arrival   *int64 `json:"next_same_pattern_alighting_arrival"`
	Censored  bool   `json:"no_recovery_in_loaded_horizon"`
}
type Interchange struct {
	From     string     `json:"from_connection"`
	To       string     `json:"to_connection"`
	Ready    int64      `json:"physical_ready"`
	Slack    int64      `json:"slack_seconds"`
	Recovery []Recovery `json:"recovery"`
}
type Feature struct {
	Identity             string        `json:"identity"`
	Pattern              string        `json:"route_stop_walk_pattern"`
	Metrics              model.Metrics `json:"metrics"`
	Duration             int64         `json:"duration_seconds"`
	InitialWait          int64         `json:"initial_wait_seconds"`
	Ride                 int64         `json:"ride_seconds"`
	Wait                 int64         `json:"total_wait_seconds"`
	LongestWalk          int64         `json:"longest_walk_block_seconds"`
	SameRouteReboardings int           `json:"same_route_reboardings"`
	Transfers            []Interchange `json:"interchanges"`
}
type Selection struct {
	Limit                 int               `json:"limit"`
	Envelope              int64             `json:"score_envelope_seconds"`
	Selected              []string          `json:"selected"`
	Decisions             map[string]string `json:"decisions"`
	GlobalOptimalityClaim bool              `json:"global_optimality_claim"`
}
type Result struct {
	Choices    *Choices    `json:"choices,omitempty"`
	Policy     string      `json:"policy"`
	Features   []Feature   `json:"features"`
	Selections []Selection `json:"selections"`
}
type routeIdentity struct {
	Missing bool
	ID      string
}

type Index struct {
	checker         *validate.Checker
	rules           *transfer.Resolver
	connections     map[string]model.Connection
	routes          map[string]string
	tripConnections map[string][]model.Connection
	byRoute         map[routeIdentity][]string
	routeKeys       map[string]routeIdentity
	tripAccessible  map[string]bool
}

func New(d model.Dataset) (*Index, error) {
	c, e := validate.New(d)
	if e != nil {
		return nil, e
	}
	x := &Index{checker: c, rules: transfer.New(d), connections: map[string]model.Connection{}, routes: map[string]string{}, tripConnections: map[string][]model.Connection{}, byRoute: map[routeIdentity][]string{}, routeKeys: map[string]routeIdentity{}, tripAccessible: map[string]bool{}}
	for _, t := range d.Trips {
		route := t.RouteID
		key := routeIdentity{ID: route}
		if route == "" {
			key = routeIdentity{Missing: true, ID: t.ID}
			route = "trip:" + t.ID
		}
		x.routes[t.ID] = route
		x.routeKeys[t.ID] = key
		x.byRoute[key] = append(x.byRoute[key], t.ID)
		x.tripAccessible[t.ID] = t.Accessible == model.Yes
	}
	for _, c := range d.Connections {
		x.connections[c.ID] = c
		x.tripConnections[c.TripID] = append(x.tripConnections[c.TripID], c)
	}
	for _, cs := range x.tripConnections {
		sort.Slice(cs, func(i, j int) bool { return cs[i].Sequence < cs[j].Sequence })
	}
	for _, ts := range x.byRoute {
		sort.Strings(ts)
	}
	return x, nil
}

// Evaluate independently validates every supplied witness, including its times.
// Its cost is presentation overhead; it does not upgrade the pool's certificate.
func (x *Index) Evaluate(q model.Query, js []model.Journey) (Result, error) {
	if _, e := x.checker.Binding(q); e != nil {
		return Result{}, e
	}
	out := Result{Policy: "pattern_anchors_v1", Features: []Feature{}, Selections: []Selection{}}
	seen := map[string]bool{}
	for _, j := range js {
		v, e := x.checker.Check(q, j)
		if e != nil {
			return Result{}, e
		}
		if seen[v.Identity] {
			continue
		}
		seen[v.Identity] = true
		f, e := x.measure(q, v)
		if e != nil {
			return Result{}, e
		}
		out.Features = append(out.Features, f)
	}
	sort.Slice(out.Features, func(i, j int) bool { return out.Features[i].Identity < out.Features[j].Identity })
	for _, delta := range []int64{120, 300, 600} {
		for _, k := range []int{3, 5} {
			out.Selections = append(out.Selections, selectFeatures(out.Features, k, delta))
		}
	}
	return out, nil
}
func (x *Index) measure(q model.Query, v model.Validated) (Feature, error) {
	f := Feature{Identity: v.Identity, Metrics: v.Metrics, Duration: v.Metrics.Arrival - q.Departure, Transfers: []Interchange{}}
	type token struct {
		Kind         string
		Route        string
		Stops        []string
		Walk         string
		MissingRoute bool `json:",omitempty"`
	}
	tokens := []token{}
	blocks := [][]model.Connection{}
	var previous *model.Connection
	var last *model.Connection
	current := q.Departure
	walking := int64(0)
	firstRide := true
	seenRoutes := map[routeIdentity]bool{}
	for _, l := range v.Journey.Legs {
		f.Wait += l.Departure - current
		if l.Kind == "walk" {
			walking += l.Arrival - l.Departure
			f.LongestWalk = max(f.LongestWalk, walking)
			tokens = append(tokens, token{Kind: "walk", Walk: l.Ref})
			last = nil
		} else {
			c := x.connections[l.Ref]
			continuing := last != nil && last.TripID == c.TripID && last.Sequence+1 == c.Sequence
			f.Ride += l.Arrival - l.Departure
			if !continuing {
				route := x.routes[c.TripID]
				if seenRoutes[x.routeKeys[c.TripID]] {
					f.SameRouteReboardings++
				}
				seenRoutes[x.routeKeys[c.TripID]] = true
				if firstRide {
					f.InitialWait = l.Departure - q.Departure - v.Metrics.AccessWalking
					firstRide = false
				}
				if previous != nil {
					rule, e := x.rules.Resolve(*previous, c, q.TransferBuffer)
					if e != nil {
						return Feature{}, e
					}
					ready := max(current, previous.Arrival+rule.Minimum)
					if !rule.Allowed || ready > c.Departure {
						return Feature{}, fmt.Errorf("invalid validated interchange")
					}
					f.Transfers = append(f.Transfers, Interchange{From: previous.ID, To: c.ID, Ready: ready, Slack: c.Departure - ready, Recovery: []Recovery{}})
				}
				tokens = append(tokens, token{Kind: "ride", Route: route, MissingRoute: x.routeKeys[c.TripID].Missing, Stops: []string{c.From, c.To}})
				blocks = append(blocks, []model.Connection{c})
			} else {
				tokens[len(tokens)-1].Stops = append(tokens[len(tokens)-1].Stops, c.To)
				blocks[len(blocks)-1] = append(blocks[len(blocks)-1], c)
			}
			previous = &c
			last = &c
			walking = 0
		}
		current = l.Arrival
	}
	b, _ := json.Marshal(tokens)
	f.Pattern = string(b)
	for i := range f.Transfers {
		block := blocks[i+1]
		first, end := block[0], block[len(block)-1]
		prev := x.connections[f.Transfers[i].From]
		for _, margin := range []int64{60, 120, 300} {
			r := Recovery{Margin: margin, Censored: true}
			for _, tid := range x.byRoute[x.routeKeys[first.TripID]] {
				if q.AccessibleOnly && !x.tripAccessible[tid] {
					continue
				}
				for start, c := range x.tripConnections[tid] {
					cs := x.tripConnections[tid]
					if start+len(block) > len(cs) || c.Departure <= first.Departure || c.Departure < first.Departure+margin || !c.Pickup {
						continue
					}
					ok := true
					for j, old := range block {
						if cs[start+j].From != old.From || cs[start+j].To != old.To || cs[start+j].Sequence != c.Sequence+j {
							ok = false
							break
						}
					}
					candidateEnd := cs[start+len(block)-1]
					if !ok || !candidateEnd.Dropoff || candidateEnd.To != end.To {
						continue
					}
					// Same physical incoming transfer, now applying the candidate trip's rule.
					rule, e := x.rules.Resolve(prev, c, q.TransferBuffer)
					if e != nil {
						return Feature{}, e
					}
					if !rule.Allowed || c.Departure < prev.Arrival+rule.Minimum {
						continue
					}
					if r.Departure == nil || c.Departure < *r.Departure || (c.Departure == *r.Departure && candidateEnd.Arrival < *r.Arrival) {
						dep, arr := c.Departure, candidateEnd.Arrival
						r.Departure = &dep
						r.Arrival = &arr
						r.Censored = false
					}
				}
			}
			f.Transfers[i].Recovery = append(f.Transfers[i].Recovery, r)
		}
	}
	return f, nil
}
func score(f Feature) int64 { return f.Duration + 300*int64(f.Metrics.Transfers) }
func less(a, b Feature) bool {
	if score(a) != score(b) {
		return score(a) < score(b)
	}
	if a.Metrics.Arrival != b.Metrics.Arrival {
		return a.Metrics.Arrival < b.Metrics.Arrival
	}
	if a.Metrics.WalkingSeconds != b.Metrics.WalkingSeconds {
		return a.Metrics.WalkingSeconds < b.Metrics.WalkingSeconds
	}
	return a.Identity < b.Identity
}
func selectFeatures(fs []Feature, k int, delta int64) Selection {
	s := Selection{Limit: k, Envelope: delta, Selected: []string{}, Decisions: map[string]string{}}
	if len(fs) == 0 {
		return s
	}
	ranked := append([]Feature{}, fs...)
	sort.Slice(ranked, func(i, j int) bool { return less(ranked[i], ranked[j]) })
	best := score(ranked[0])
	patterns := map[string]bool{}
	eligible := []Feature{}
	for _, f := range ranked {
		if score(f) > best+delta {
			s.Decisions[f.Identity] = "outside_score_envelope"
			continue
		}
		if patterns[f.Pattern] {
			s.Decisions[f.Identity] = "repeated_physical_pattern"
			continue
		}
		patterns[f.Pattern] = true
		dominated := false
		for _, other := range fs {
			a, b := other.Metrics, f.Metrics
			if a.Arrival <= b.Arrival && a.Boardings <= b.Boardings && a.WalkingSeconds <= b.WalkingSeconds && (a.Arrival < b.Arrival || a.Boardings < b.Boardings || a.WalkingSeconds < b.WalkingSeconds) {
				dominated = true
				break
			}
		}
		if dominated {
			s.Decisions[f.Identity] = "three_vector_dominated"
			continue
		}
		eligible = append(eligible, f)
		s.Decisions[f.Identity] = "capacity"
	}
	add := func(f Feature, why string) {
		if len(s.Selected) < k && s.Decisions[f.Identity] == "capacity" {
			s.Selected = append(s.Selected, f.Identity)
			s.Decisions[f.Identity] = why
		}
	}
	if len(eligible) == 0 {
		return s
	}
	add(eligible[0], "selected_score_anchor")
	for _, metric := range []func(Feature) int64{func(f Feature) int64 { return f.Metrics.Arrival }, func(f Feature) int64 { return f.Metrics.WalkingSeconds }, func(f Feature) int64 { return int64(f.Metrics.Transfers) }} {
		best := eligible[0]
		for _, f := range eligible[1:] {
			if metric(f) < metric(best) || (metric(f) == metric(best) && less(f, best)) {
				best = f
			}
		}
		add(best, "selected_tradeoff_anchor")
	}
	for _, f := range eligible {
		add(f, "selected_distinct_pattern")
	}
	return s
}
