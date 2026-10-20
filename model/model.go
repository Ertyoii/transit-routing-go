// Package model defines the finite static timetable contract. It contains no search logic.
package model

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
	_ "time/tzdata"
)

const SchemaVersion = "0.2"

func SupportedVersion(v string) bool { return v == "0.1" || v == SchemaVersion }

const MaxServiceSecond int64 = 7 * 24 * 60 * 60

type Access string

const (
	Yes     Access = "yes"
	No      Access = "no"
	Unknown Access = "unknown"
)

func (a Access) valid() bool { return a == Yes || a == No || a == Unknown }

type Stop struct {
	ParentStation string `json:"parent_station,omitempty"`
	LocationType  int    `json:"location_type,omitempty"`
	ID            string `json:"id"`
	Accessible    Access `json:"accessible"`
}
type Trip struct {
	RouteID     string `json:"route_id,omitempty"`
	SourceID    string `json:"source_id,omitempty"`
	ServiceDate string `json:"service_date,omitempty"`
	ID          string `json:"id"`
	Accessible  Access `json:"accessible"`
}
type Connection struct {
	ID        string `json:"id"`
	TripID    string `json:"trip_id"`
	Sequence  int    `json:"sequence"`
	From      string `json:"from"`
	To        string `json:"to"`
	Departure int64  `json:"departure"`
	Arrival   int64  `json:"arrival"`
	Pickup    bool   `json:"pickup"`
	Dropoff   bool   `json:"dropoff"`
}
type Footpath struct {
	Provenance string `json:"provenance,omitempty"`
	ID         string `json:"id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Duration   int64  `json:"duration"`
	Accessible Access `json:"accessible"`
}
type TransferRule struct {
	ID         string `json:"id"`
	FromStop   string `json:"from_stop"`
	ToStop     string `json:"to_stop"`
	FromTrip   string `json:"from_trip,omitempty"`
	ToTrip     string `json:"to_trip,omitempty"`
	FromRoute  string `json:"from_route,omitempty"`
	ToRoute    string `json:"to_route,omitempty"`
	Type       int    `json:"type"`
	MinSeconds int64  `json:"min_seconds"`
}

type Dataset struct {
	Transfers     []TransferRule `json:"transfers,omitempty"`
	Window        *TimeWindow    `json:"window,omitempty"`
	DataKind      string         `json:"data_kind,omitempty"`
	SchemaVersion string         `json:"schema_version"`
	ID            string         `json:"id"`
	TimeZone      string         `json:"time_zone"`
	ServiceDate   string         `json:"service_date"`
	Stops         []Stop         `json:"stops"`
	Trips         []Trip         `json:"trips"`
	Connections   []Connection   `json:"connections"`
	Footpaths     []Footpath     `json:"footpaths"`
}
type TimeWindow struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Walking limits constrain contiguous access/egress/transfer blocks separately.
// Direct is an explicitly separate walking-only journey limit.
type WalkingLimits struct {
	Access   int64 `json:"access_seconds"`
	Egress   int64 `json:"egress_seconds"`
	Transfer int64 `json:"transfer_seconds"`
	Direct   int64 `json:"direct_seconds"`
}

type Query struct {
	Walking        *WalkingLimits `json:"walking_limits,omitempty"`
	ID             string         `json:"id"`
	Origin         string         `json:"origin"`
	Destination    string         `json:"destination"`
	Departure      int64          `json:"departure"`
	LatestArrival  int64          `json:"latest_arrival"`
	TransferBuffer int64          `json:"transfer_buffer"`
	MaxWalkSeconds int64          `json:"max_walk_seconds"`
	MaxBoardings   int            `json:"max_boardings"`
	MaxLegs        int            `json:"max_legs"`
	AccessibleOnly bool           `json:"accessible_only"`
}
type Leg struct {
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	Departure int64  `json:"departure"`
	Arrival   int64  `json:"arrival"`
}
type Journey struct {
	Legs []Leg `json:"legs"`
}
type Metrics struct {
	AccessWalking   int64 `json:"access_walking_seconds"`
	TransferWalking int64 `json:"transfer_walking_seconds"`
	EgressWalking   int64 `json:"egress_walking_seconds"`
	DirectWalking   int64 `json:"direct_walking_seconds"`
	Arrival         int64 `json:"arrival"`
	Boardings       int   `json:"boardings"`
	Transfers       int   `json:"transfers"`
	WalkingSeconds  int64 `json:"walking_seconds"`
	LegCount        int   `json:"leg_count"`
}

// Validated is a reconstructed result. Adapters must not populate it without validation.
type Validated struct {
	Journey  Journey `json:"journey"`
	Identity string  `json:"identity"`
	Metrics  Metrics `json:"metrics"`
}

// Identity deliberately excludes reported times and metrics. Validation checks those.
func Identity(j Journey) string {
	type token struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
	}
	tokens := make([]token, len(j.Legs))
	for i, l := range j.Legs {
		tokens[i] = token{l.Kind, l.Ref}
	}
	b, _ := json.Marshal(tokens)
	return string(b)
}

func (d Dataset) Validate() error {
	if !SupportedVersion(d.SchemaVersion) || d.ID == "" {
		return fmt.Errorf("invalid dataset schema or ID")
	}
	loc, err := time.LoadLocation(d.TimeZone)
	if err != nil || d.TimeZone == "" || d.TimeZone == "Local" {
		return fmt.Errorf("explicit IANA timezone required: %q", d.TimeZone)
	}
	parsed, err := time.ParseInLocation("2006-01-02", d.ServiceDate, loc)
	if err != nil || parsed.Format("2006-01-02") != d.ServiceDate {
		return fmt.Errorf("invalid service date %q", d.ServiceDate)
	}
	stops := map[string]bool{}
	for _, s := range d.Stops {
		if s.ID == "" || stops[s.ID] || !s.Accessible.valid() || s.LocationType < 0 || s.LocationType > 4 {
			return fmt.Errorf("invalid/duplicate stop %q", s.ID)
		}
		stops[s.ID] = true
	}
	for _, s := range d.Stops {
		if s.ParentStation != "" && !stops[s.ParentStation] {
			return fmt.Errorf("unknown parent station %q", s.ParentStation)
		}
	}
	if len(stops) == 0 {
		return fmt.Errorf("empty stop set")
	}
	trips := map[string]bool{}
	for _, t := range d.Trips {
		if t.ID == "" || trips[t.ID] || !t.Accessible.valid() {
			return fmt.Errorf("invalid/duplicate trip %q", t.ID)
		}
		trips[t.ID] = true
	}
	ids := map[string]bool{}
	chains := map[string][]Connection{}
	for _, c := range d.Connections {
		if c.ID == "" || ids[c.ID] || !trips[c.TripID] || !stops[c.From] || !stops[c.To] || c.Sequence < 0 || c.Departure < 0 || c.Arrival < c.Departure || c.Arrival > MaxServiceSecond {
			return fmt.Errorf("invalid/duplicate connection %q", c.ID)
		}
		ids[c.ID] = true
		chains[c.TripID] = append(chains[c.TripID], c)
	}
	for t, cs := range chains {
		sort.Slice(cs, func(i, j int) bool { return cs[i].Sequence < cs[j].Sequence })
		for i, c := range cs {
			if c.Sequence != i {
				return fmt.Errorf("trip %q must have contiguous zero-based sequences", t)
			}
			if i > 0 && (cs[i-1].To != c.From || cs[i-1].Arrival > c.Departure) {
				return fmt.Errorf("trip %q has discontinuous events", t)
			}
		}
	}
	ids = map[string]bool{}
	for _, w := range d.Footpaths {
		if w.ID == "" || ids[w.ID] || !stops[w.From] || !stops[w.To] || w.Duration <= 0 || w.Duration > MaxServiceSecond || !w.Accessible.valid() {
			return fmt.Errorf("invalid/duplicate footpath %q", w.ID)
		}
		ids[w.ID] = true
	}
	if err := d.validateTransferInputs(); err != nil {
		return err
	}
	if d.Window != nil && (d.Window.Start < 0 || d.Window.End < d.Window.Start || d.Window.End > MaxServiceSecond) {
		return fmt.Errorf("invalid snapshot window")
	}
	return nil
}

func (q Query) validateBounds(window *TimeWindow) error {
	if q.ID == "" || q.Departure < 0 || q.LatestArrival < q.Departure || q.LatestArrival > MaxServiceSecond || q.TransferBuffer < 0 || q.TransferBuffer > MaxServiceSecond || q.MaxWalkSeconds < 0 || q.MaxWalkSeconds > MaxServiceSecond || q.MaxBoardings < 0 || q.MaxLegs < 0 || q.MaxLegs > 1024 || q.MaxBoardings > q.MaxLegs {
		return fmt.Errorf("invalid query %q bounds (max legs <=1024)", q.ID)
	}
	if window != nil && (q.Departure < window.Start || q.LatestArrival > window.End) {
		return fmt.Errorf("query extends beyond normalized snapshot window")
	}
	if q.Walking != nil {
		for _, n := range []int64{q.Walking.Access, q.Walking.Egress, q.Walking.Transfer, q.Walking.Direct} {
			if n < 0 || n > MaxServiceSecond {
				return fmt.Errorf("invalid separated walking resource")
			}
		}
	}
	return nil
}

func (q Query) Validate(d Dataset) error {
	if err := q.validateBounds(d.Window); err != nil {
		return err
	}
	var origin, destination bool
	for _, s := range d.Stops {
		origin = origin || s.ID == q.Origin
		destination = destination || s.ID == q.Destination
	}
	if !origin || !destination {
		return fmt.Errorf("query %q has unknown endpoint", q.ID)
	}
	return nil
}

// Clone returns a snapshot that cannot be changed by a caller mutating its input slices.
func (d Dataset) Clone() Dataset {
	d.Stops = append([]Stop{}, d.Stops...)
	d.Trips = append([]Trip{}, d.Trips...)
	d.Connections = append([]Connection{}, d.Connections...)
	d.Footpaths = append([]Footpath{}, d.Footpaths...)
	d.Transfers = append([]TransferRule{}, d.Transfers...)
	if d.Window != nil {
		w := *d.Window
		d.Window = &w
	}
	return d
}
func (d Dataset) validateTransferInputs() error {
	stops := map[string]Stop{}
	for _, s := range d.Stops {
		stops[s.ID] = s
	}
	trips := map[string]Trip{}
	routes := map[string]bool{}
	for _, t := range d.Trips {
		id := t.ID
		if t.SourceID != "" {
			id = t.SourceID
		}
		if prior, ok := trips[id]; ok && prior.RouteID != t.RouteID {
			return fmt.Errorf("source trip has inconsistent route")
		}
		trips[id] = t
		routes[t.RouteID] = true
	}
	keys := map[string]bool{}
	ids := map[string]bool{}
	for _, r := range d.Transfers {
		if r.ID == "" || ids[r.ID] {
			return fmt.Errorf("empty/duplicate transfer ID")
		}
		ids[r.ID] = true
		if _, ok := stops[r.FromStop]; !ok {
			return fmt.Errorf("unknown transfer origin")
		}
		if _, ok := stops[r.ToStop]; !ok {
			return fmt.Errorf("unknown transfer destination")
		}
		if r.Type != 0 && r.Type != 2 && r.Type != 3 {
			return fmt.Errorf("unsupported transfer type %d: timed and linked trips need a separate contract", r.Type)
		}
		if r.MinSeconds < 0 || r.MinSeconds > MaxServiceSecond || (r.Type != 2 && r.MinSeconds != 0) {
			return fmt.Errorf("invalid/unsupported minimum transfer time")
		}
		for _, side := range []struct{ trip, route string }{{r.FromTrip, r.FromRoute}, {r.ToTrip, r.ToRoute}} {
			if side.trip != "" {
				t, ok := trips[side.trip]
				if !ok || (side.route != "" && side.route != t.RouteID) {
					return fmt.Errorf("invalid trip/route transfer selector")
				}
			}
			if side.route != "" && !routes[side.route] {
				return fmt.Errorf("unknown transfer route")
			}
		}
		k, _ := json.Marshal([]string{r.FromStop, r.ToStop, r.FromTrip, r.ToTrip, r.FromRoute, r.ToRoute})
		if keys[string(k)] {
			return fmt.Errorf("duplicate transfer selector")
		}
		keys[string(k)] = true
	}
	return nil
}
