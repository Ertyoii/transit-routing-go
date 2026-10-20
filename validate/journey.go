// Package validate independently reconstructs full itineraries. It imports no router or oracle.
package validate

import (
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
)

type Checker struct {
	datasetSHA256 string
	queries       *model.QueryValidator
	data          model.Dataset
	stops         map[string]model.Stop
	trips         map[string]model.Trip
	rides         map[string]model.Connection
	walks         map[string]model.Footpath
}

// New owns a private value-copy lookup; callers must not mutate the dataset during a run.
func New(d model.Dataset) (*Checker, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	d = d.Clone()
	c := &Checker{queries: model.NewQueryValidator(d), datasetSHA256: model.DatasetFingerprint(d), data: d, stops: map[string]model.Stop{}, trips: map[string]model.Trip{}, rides: map[string]model.Connection{}, walks: map[string]model.Footpath{}}
	for _, x := range d.Stops {
		c.stops[x.ID] = x
	}
	for _, x := range d.Trips {
		c.trips[x.ID] = x
	}
	for _, x := range d.Connections {
		c.rides[x.ID] = x
	}
	for _, x := range d.Footpaths {
		c.walks[x.ID] = x
	}
	return c, nil
}

// Binding validates the query and binds it to this checker's owned network.
// It supplies metadata only; journey feasibility still requires Check.
func (c *Checker) Binding(q model.Query) (model.ResultBinding, error) {
	if err := c.queries.Validate(q); err != nil {
		return model.ResultBinding{}, err
	}
	return model.BindQuery(c.datasetSHA256, q), nil
}

func (c *Checker) Check(q model.Query, j model.Journey) (model.Validated, error) {
	fail := func(format string, args ...any) (model.Validated, error) {
		return model.Validated{}, fmt.Errorf(format, args...)
	}
	if err := c.queries.Validate(q); err != nil {
		return model.Validated{}, err
	}
	if len(j.Legs) > q.MaxLegs {
		return fail("leg budget exceeded")
	}
	accessible := func(s string) bool { return !q.AccessibleOnly || c.stops[s].Accessible == model.Yes }
	if !accessible(q.Origin) || !accessible(q.Destination) {
		return fail("endpoint accessibility not confirmed")
	}
	at, now := q.Origin, q.Departure
	m := model.Metrics{Arrival: now, LegCount: len(j.Legs)}
	var prev *model.Connection
	var lastAlight int64
	var lastRide *model.Connection
	var blockWalk int64
	canAlight := func() bool { return prev == nil || (prev.Dropoff && accessible(prev.To)) }
	for i, l := range j.Legs {
		if at == q.Destination && canAlight() {
			return fail("leg %d leaves eligible destination", i)
		}
		if l.Departure < now || l.Arrival < l.Departure || l.Arrival > q.LatestArrival {
			return fail("leg %d invalid time", i)
		}
		switch l.Kind {
		case "ride":
			r, ok := c.rides[l.Ref]
			if !ok {
				return fail("leg %d unknown connection", i)
			}
			if r.From != at || l.Departure != r.Departure || l.Arrival != r.Arrival {
				return fail("leg %d connection endpoints/times mismatch", i)
			}
			staying := prev != nil && prev.TripID == r.TripID && r.Sequence == prev.Sequence+1
			if !staying {
				if !canAlight() {
					return fail("leg %d cannot alight prior vehicle", i)
				}
				if prev != nil {
					lastAlight = prev.Arrival
				}
				if !r.Pickup || !accessible(r.From) || (q.AccessibleOnly && c.trips[r.TripID].Accessible != model.Yes) {
					return fail("leg %d boarding prohibited", i)
				}
				if q.Walking != nil {
					cap := q.Walking.Access
					if m.Boardings > 0 {
						cap = q.Walking.Transfer
					}
					if blockWalk > cap {
						return fail("leg %d exceeds access/transfer walking block", i)
					}
				}
				if m.Boardings > 0 {
					allow, minimum, err := c.checkTransfer(*lastRide, r, q.TransferBuffer)
					if err != nil {
						return model.Validated{}, err
					}
					if !allow || r.Departure < lastAlight+minimum {
						return fail("leg %d violates transfer rule/minimum", i)
					}
				}
				if m.Boardings == 0 {
					m.AccessWalking = blockWalk
				} else {
					m.TransferWalking += blockWalk
				}
				m.Boardings++
			}
			at, now = r.To, r.Arrival
			copyRide := r
			prev = &copyRide
			lastRide = &copyRide
			blockWalk = 0
		case "walk":
			w, ok := c.walks[l.Ref]
			if !ok {
				return fail("leg %d unknown footpath", i)
			}
			if !canAlight() {
				return fail("leg %d cannot alight before walking", i)
			}
			if prev != nil {
				lastAlight = prev.Arrival
			}
			if w.From != at || l.Departure != now || l.Arrival != now+w.Duration {
				return fail("leg %d walking direction/times mismatch", i)
			}
			if !accessible(w.From) || !accessible(w.To) || (q.AccessibleOnly && w.Accessible != model.Yes) {
				return fail("leg %d walking accessibility not confirmed", i)
			}
			m.WalkingSeconds += w.Duration
			blockWalk += w.Duration
			at, now = w.To, l.Arrival
			prev = nil
		default:
			return fail("leg %d unknown kind %q", i, l.Kind)
		}
		if m.Boardings > q.MaxBoardings || m.WalkingSeconds > q.MaxWalkSeconds {
			return fail("leg %d exceeds resource budget", i)
		}
	}
	if at != q.Destination || !canAlight() {
		return fail("journey does not terminate at an eligible destination")
	}
	if m.Boardings == 0 {
		m.DirectWalking = blockWalk
		if q.Walking != nil && blockWalk > q.Walking.Direct {
			return fail("direct walking limit exceeded")
		}
	} else {
		m.EgressWalking = blockWalk
		if q.Walking != nil && blockWalk > q.Walking.Egress {
			return fail("egress walking limit exceeded")
		}
	}
	m.Arrival = now
	if m.Boardings > 0 {
		m.Transfers = m.Boardings - 1
	}
	// Copy the slice so an adapter cannot mutate the result after validation.
	j.Legs = append([]model.Leg{}, j.Legs...)
	return model.Validated{Journey: j, Identity: model.Identity(j), Metrics: m}, nil
}

// CheckClaim validates the witness and rejects metrics/identity supplied incorrectly by a router.
func (c *Checker) CheckClaim(q model.Query, claim model.Validated) error {
	actual, err := c.Check(q, claim.Journey)
	if err != nil {
		return err
	}
	if actual.Identity != claim.Identity || actual.Metrics != claim.Metrics {
		return fmt.Errorf("router identity or metric claim differs from reconstruction")
	}
	return nil
}

// checkTransfer separately interprets rule specificity while reconstructing a witness.
func (c *Checker) checkTransfer(a, b model.Connection, fallback int64) (bool, int64, error) {
	ta, tb := c.trips[a.TripID], c.trips[b.TripID]
	ia, ib := ta.ID, tb.ID
	if ta.SourceID != "" {
		ia = ta.SourceID
	}
	if tb.SourceID != "" {
		ib = tb.SourceID
	}
	candidates := map[int][]model.TransferRule{}
	for _, r := range c.data.Transfers {
		from := r.FromStop == a.To || c.stops[r.FromStop].LocationType == 1 && c.stops[a.To].ParentStation == r.FromStop
		to := r.ToStop == b.From || c.stops[r.ToStop].LocationType == 1 && c.stops[b.From].ParentStation == r.ToStop
		if !from || !to {
			continue
		}
		if r.FromTrip != "" && r.FromTrip != ia || r.ToTrip != "" && r.ToTrip != ib || r.FromRoute != "" && r.FromRoute != ta.RouteID || r.ToRoute != "" && r.ToRoute != tb.RouteID {
			continue
		}
		n := 0
		// Each side contributes its most specific selector: trip=3, route=1, unspecified=0.
		if r.FromTrip != "" {
			n += 3
		} else if r.FromRoute != "" {
			n++
		}
		if r.ToTrip != "" {
			n += 3
		} else if r.ToRoute != "" {
			n++
		}
		candidates[n] = append(candidates[n], r)
	}
	for _, specificity := range []int{6, 4, 3, 2, 1, 0} {
		if rs := candidates[specificity]; len(rs) > 0 {
			if len(rs) > 1 {
				return false, 0, fmt.Errorf("witness has ambiguous maximum-specificity transfers")
			}
			r := rs[0]
			minimum := int64(0)
			if r.Type == 2 {
				minimum = r.MinSeconds
			}
			return r.Type != 3, minimum, nil
		}
	}
	return true, fallback, nil
}
