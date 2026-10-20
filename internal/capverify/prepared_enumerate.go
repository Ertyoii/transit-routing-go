package capverify

import (
	"context"
	"fmt"

	"github.com/ertyoii/transit-routing-go/model"
)

// Enumerate preserves raw input order and every resource-feasible physical
// word. Its separate meter charges candidate rows and lookup probes, including
// misses. It does not preserve legacy scan-budget/cancellation boundaries.
func (p *Prepared) Enumerate(ctx context.Context, q model.Query, limits Limits) (out PreparedResult, err error) {
	out.Journeys = []model.Journey{}
	out.ProbeStats.Policy = ActualProbePolicy
	defer func() {
		if err == nil {
			err = ctx.Err()
		}
		out.Scans = out.ProbeStats.Probes
		out.ProbeStats.LegacyWouldDecline = out.ProbeStats.LegacySaturated || out.ProbeStats.LegacyScans > int64(limits.MaxScans)
		out.Complete = err == nil
		out.ProbeStats.LegacyComplete = out.Complete && !out.ProbeStats.LegacySaturated
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	if p == nil || p.query == nil {
		err = fmt.Errorf("raw verifier unprepared")
		return
	}
	if limits.MaxScans <= 0 || limits.MaxStates <= 0 || limits.MaxJourneys <= 0 {
		err = fmt.Errorf("oracle budgets must be positive")
		return
	}
	probe := func(kind string) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if out.ProbeStats.Probes >= limits.MaxScans {
			return &TruncatedError{"actual probes"}
		}
		out.ProbeStats.Probes++
		switch kind {
		case "connection":
			out.ProbeStats.ConnectionRows++
		case "footpath":
			out.ProbeStats.FootpathRows++
		case "rule":
			out.ProbeStats.RuleRows++
		default:
			out.ProbeStats.MetadataProbes++
		}
		return nil
	}
	shadow := func(n int64) { out.ProbeStats.addLegacy(n, false) }

	// QueryValidator performs two endpoint lookups and only raw bound/window
	// validation; it knows nothing about route pruning or transition rules.
	for i := 0; i < 2; i++ {
		if err = probe("metadata"); err != nil {
			return
		}
	}
	if err = p.query.Validate(q); err != nil {
		return
	}
	if q.Walking != nil {
		w := *q.Walking
		q.Walking = &w
	}
	accessible := func(id string) (bool, error) {
		if !q.AccessibleOnly {
			return true, nil
		}
		if e := probe("metadata"); e != nil {
			return false, e
		}
		s, ok := p.stops[id]
		return ok && s.row.Accessible == model.Yes, nil
	}
	for _, id := range []string{q.Origin, q.Destination} {
		var ok bool
		ok, err = accessible(id)
		if err != nil {
			return
		}
		if !ok {
			return
		}
	}
	type prefix struct {
		stop                                       string
		clock, totalWalk, walkingBlock, lastAlight int64
		boardings, onboard, lastRide               int
		legs                                       []model.Leg
	}
	copyLeg := func(legs []model.Leg, leg model.Leg) []model.Leg {
		out.ProbeStats.CopiedLegs += len(legs) + 1
		owned := make([]model.Leg, len(legs)+1)
		copy(owned, legs)
		owned[len(legs)] = leg
		return owned
	}
	var visit func(prefix) error
	visit = func(s prefix) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if out.States >= limits.MaxStates {
			return &TruncatedError{"states"}
		}
		out.States++
		canAlight := s.onboard < 0
		if s.onboard >= 0 && p.d.Connections[s.onboard].Dropoff {
			var e error
			canAlight, e = accessible(s.stop)
			if e != nil {
				return e
			}
		}
		if s.stop == q.Destination && canAlight {
			if q.Walking != nil {
				allowance := q.Walking.Egress
				if s.boardings == 0 {
					allowance = q.Walking.Direct
				}
				if s.walkingBlock > allowance {
					return nil
				}
			}
			if len(out.Journeys) >= limits.MaxJourneys {
				return &TruncatedError{"journeys"}
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			legs := append([]model.Leg{}, s.legs...)
			out.ProbeStats.CopiedLegs += len(legs)
			out.Journeys = append(out.Journeys, model.Journey{Legs: legs})
			return nil
		}
		if len(s.legs) >= q.MaxLegs {
			return nil
		}
		if e := probe("metadata"); e != nil {
			return e
		}
		connections := p.rides[s.stop]
		previous := -1
		for _, ordinal := range connections {
			shadow(int64(ordinal - previous))
			previous = ordinal
			if e := probe("connection"); e != nil {
				return e
			}
			ride := p.d.Connections[ordinal]
			if ride.Departure < s.clock || ride.Arrival > q.LatestArrival {
				continue
			}
			stay := false
			if s.onboard >= 0 {
				prior := p.d.Connections[s.onboard]
				stay = ride.TripID == prior.TripID && ride.Sequence == prior.Sequence+1
			}
			boards, alighted := s.boardings, s.lastAlight
			if !stay {
				if !canAlight || !ride.Pickup {
					continue
				}
				ok, e := accessible(ride.From)
				if e != nil {
					return e
				}
				if !ok {
					continue
				}
				if q.AccessibleOnly {
					if e := probe("metadata"); e != nil {
						return e
					}
					trip, exists := p.trips[ride.TripID]
					if !exists || trip.row.Accessible != model.Yes {
						continue
					}
				}
				if s.onboard >= 0 {
					alighted = p.d.Connections[s.onboard].Arrival
				}
				if q.Walking != nil {
					allowance := q.Walking.Transfer
					if boards == 0 {
						allowance = q.Walking.Access
					}
					if s.walkingBlock > allowance {
						continue
					}
				}
				if boards > 0 {
					legacyCost, saturated := legacyRuleCost(int64(len(p.d.Transfers)), int64(len(p.d.Stops)), int64(len(p.d.Trips)))
					out.ProbeStats.addLegacy(legacyCost, saturated)
					allowed, minimum, e := p.rule(p.d.Connections[s.lastRide], ride, q.TransferBuffer, probe)
					if e != nil {
						return e
					}
					if !allowed || ride.Departure < alighted+minimum {
						continue
					}
				}
				boards++
			}
			if boards > q.MaxBoardings {
				continue
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			legs := copyLeg(s.legs, model.Leg{Kind: "ride", Ref: ride.ID, Departure: ride.Departure, Arrival: ride.Arrival})
			if e := visit(prefix{stop: ride.To, clock: ride.Arrival, totalWalk: s.totalWalk, lastAlight: alighted, boardings: boards, onboard: ordinal, lastRide: ordinal, legs: legs}); e != nil {
				return e
			}
		}
		shadow(int64(len(p.d.Connections) - previous - 1))
		if !canAlight {
			return nil
		}
		if e := probe("metadata"); e != nil {
			return e
		}
		walks := p.walks[s.stop]
		previous = -1
		for _, ordinal := range walks {
			shadow(int64(ordinal - previous))
			previous = ordinal
			if e := probe("footpath"); e != nil {
				return e
			}
			walk := p.d.Footpaths[ordinal]
			if s.totalWalk+walk.Duration > q.MaxWalkSeconds || s.clock+walk.Duration > q.LatestArrival {
				continue
			}
			ok, e := accessible(walk.From)
			if e != nil {
				return e
			}
			if !ok {
				continue
			}
			ok, e = accessible(walk.To)
			if e != nil {
				return e
			}
			if !ok || q.AccessibleOnly && walk.Accessible != model.Yes {
				continue
			}
			if q.Walking != nil {
				allowance := max(q.Walking.Transfer, q.Walking.Egress)
				if s.boardings == 0 {
					allowance = max(q.Walking.Access, q.Walking.Direct)
				}
				if s.walkingBlock+walk.Duration > allowance {
					continue
				}
			}
			alighted := s.lastAlight
			if s.onboard >= 0 {
				alighted = p.d.Connections[s.onboard].Arrival
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			legs := copyLeg(s.legs, model.Leg{Kind: "walk", Ref: walk.ID, Departure: s.clock, Arrival: s.clock + walk.Duration})
			if e := visit(prefix{stop: walk.To, clock: s.clock + walk.Duration, totalWalk: s.totalWalk + walk.Duration, walkingBlock: s.walkingBlock + walk.Duration, lastAlight: alighted, boardings: s.boardings, onboard: -1, lastRide: s.lastRide, legs: legs}); e != nil {
				return e
			}
		}
		shadow(int64(len(p.d.Footpaths) - previous - 1))
		return nil
	}
	err = visit(prefix{stop: q.Origin, clock: q.Departure, onboard: -1, lastRide: -1})
	return
}
