// Independently authored whole-itinerary specification retained from the prior
// candidate review, before this Trip-Based implementation existed.
// Original SHA-256: 3ec52dc513bd23265cd1d6748a4cf948df83487be8537db5f4c97331a735db56
// Only package name changed; supports the unique stop-pair-rule corpus below.
package tripbased_test

import (
	"encoding/json"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
)

const inf = int64(1 << 60)

func data() model.Dataset {
	d := model.Dataset{SchemaVersion: "0.2", ID: "review", TimeZone: "Asia/Singapore", ServiceDate: "2026-10-17"}
	for _, s := range []string{"A", "B", "C", "D", "X", "Y"} {
		d.Stops = append(d.Stops, model.Stop{ID: s, Accessible: model.Yes})
	}
	return d
}
func trip(d *model.Dataset, id string, stops []string, times ...int64) {
	d.Trips = append(d.Trips, model.Trip{ID: id, Accessible: model.Yes})
	for i := 0; i < len(stops)-1; i++ {
		d.Connections = append(d.Connections, model.Connection{ID: fmt.Sprintf("%s/%d", id, i), TripID: id, Sequence: i, From: stops[i], To: stops[i+1], Departure: times[2*i], Arrival: times[2*i+1], Pickup: true, Dropoff: true})
	}
}
func query() model.Query {
	return model.Query{ID: "q", Origin: "A", Destination: "D", LatestArrival: 100, MaxBoardings: 3, MaxLegs: 64, MaxWalkSeconds: 100, Walking: &model.WalkingLimits{Access: 10, Egress: 10, Transfer: 10, Direct: 10}}
}
func objectives(ms []model.Metrics, k int) []int64 {
	out := make([]int64, k+1)
	for i := range out {
		out[i] = inf
	}
	for _, m := range ms {
		for b := m.Boardings; b <= k; b++ {
			out[b] = min(out[b], m.Arrival)
		}
	}
	return out
}

// Independent whole-word semantics. It never imports an oracle, transfer resolver,
// path builder, or production selection helper. Words are generated unpruned;
// validity is assessed by full event arrays and maximal vehicle blocks.
func assess(d model.Dataset, q model.Query, word []int) (model.Journey, model.Metrics, bool) {
	n := len(word)
	j := model.Journey{Legs: []model.Leg{}}
	m := model.Metrics{LegCount: n}
	stopOK := func(id string) bool {
		for _, s := range d.Stops {
			if s.ID == id {
				return !q.AccessibleOnly || s.Accessible == model.Yes
			}
		}
		return false
	}
	tripOK := func(id string) bool {
		for _, s := range d.Trips {
			if s.ID == id {
				return !q.AccessibleOnly || s.Accessible == model.Yes
			}
		}
		return false
	}
	if !stopOK(q.Origin) || !stopOK(q.Destination) || n > q.MaxLegs {
		return j, m, false
	}
	at := make([]string, n+1)
	at[0] = q.Origin
	tm := make([]int64, n+1)
	tm[0] = q.Departure
	ride := make([]bool, n)
	cs := make([]model.Connection, n)
	ws := make([]model.Footpath, n)
	for i, a := range word {
		if a < len(d.Connections) {
			ride[i] = true
			cs[i] = d.Connections[a]
			c := cs[i]
			if c.From != at[i] || c.Departure < tm[i] {
				return j, m, false
			}
			at[i+1] = c.To
			tm[i+1] = c.Arrival
			j.Legs = append(j.Legs, model.Leg{Kind: "ride", Ref: c.ID, Departure: c.Departure, Arrival: c.Arrival})
		} else {
			ws[i] = d.Footpaths[a-len(d.Connections)]
			w := ws[i]
			if w.From != at[i] || !stopOK(w.From) || !stopOK(w.To) || (q.AccessibleOnly && w.Accessible != model.Yes) {
				return j, m, false
			}
			at[i+1] = w.To
			tm[i+1] = tm[i] + w.Duration
			m.WalkingSeconds += w.Duration
			j.Legs = append(j.Legs, model.Leg{Kind: "walk", Ref: w.ID, Departure: tm[i], Arrival: tm[i+1]})
		}
		if tm[i+1] > q.LatestArrival {
			return j, m, false
		}
	}
	stays := func(i int) bool {
		return i > 0 && i < n && ride[i] && ride[i-1] && cs[i].TripID == cs[i-1].TripID && cs[i].Sequence == cs[i-1].Sequence+1
	}
	first, last := n, -1
	for i := range word {
		if !ride[i] {
			continue
		}
		first = min(first, i)
		last = i
		if !stays(i) {
			m.Boardings++
			if !cs[i].Pickup || !stopOK(cs[i].From) || !tripOK(cs[i].TripID) {
				return j, m, false
			}
			p := i - 1
			for p >= 0 && !ride[p] {
				p--
			}
			if p >= 0 {
				minimum := q.TransferBuffer
				allowed := true
				for _, r := range d.Transfers {
					if r.FromTrip != "" && r.FromTrip != cs[p].TripID || r.ToTrip != "" && r.ToTrip != cs[i].TripID {
						continue
					}
					if r.FromStop == cs[p].To && r.ToStop == cs[i].From {
						switch r.Type {
						case 0, 1:
							minimum = 0
						case 2:
							minimum = r.MinSeconds
						case 3:
							allowed = false
						}
					}
				}
				if !allowed || cs[i].Departure-cs[p].Arrival < minimum {
					return j, m, false
				}
			}
		}
		if i == n-1 || !stays(i+1) {
			if !cs[i].Dropoff || !stopOK(cs[i].To) {
				return j, m, false
			}
		}
	}
	for i := 0; i < n; i++ {
		if at[i] == q.Destination && (i == 0 || !ride[i-1] || cs[i-1].Dropoff && stopOK(at[i])) {
			return j, m, false
		}
	}
	for i := 0; i < n; {
		if ride[i] {
			i++
			continue
		}
		begin := i
		duration := int64(0)
		for i < n && !ride[i] {
			duration += ws[i].Duration
			i++
		}
		limit := int64(1 << 60)
		if q.Walking != nil {
			limit = q.Walking.Transfer
		}
		switch {
		case last < 0:
			if q.Walking != nil {
				limit = q.Walking.Direct
			}
			m.DirectWalking += duration
		case begin < first:
			if q.Walking != nil {
				limit = q.Walking.Access
			}
			m.AccessWalking += duration
		case begin > last:
			if q.Walking != nil {
				limit = q.Walking.Egress
			}
			m.EgressWalking += duration
		default:
			m.TransferWalking += duration
		}
		if q.Walking != nil && duration > limit {
			return j, m, false
		}
	}
	m.Arrival = tm[n]
	m.Transfers = max(0, m.Boardings-1)
	return j, m, at[n] == q.Destination && m.Boardings <= q.MaxBoardings && m.WalkingSeconds <= q.MaxWalkSeconds
}

func identity(j model.Journey) string {
	type token struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
	}
	a := []token{}
	for _, l := range j.Legs {
		a = append(a, token{l.Kind, l.Ref})
	}
	b, _ := json.Marshal(a)
	return string(b)
}
