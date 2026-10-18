// Package walking defines static, directed walking snapshots, separate from transit waiting/buffers.
package walking

import (
	"context"
	"errors"
	"fmt"
	"math"
)

const SchemaVersion = "walking.v1"
const MaxSeconds int64 = 7 * 24 * 60 * 60

var (
	ErrInvalid     = errors.New("invalid walking request or data")
	ErrStale       = errors.New("walking snapshot mismatch")
	ErrUnsupported = errors.New("walking operation unsupported")
	ErrBudget      = errors.New("walking work budget exhausted")
)

type Role string

const (
	Access   Role = "access"
	Egress   Role = "egress"
	Transfer Role = "transfer"
	Direct   Role = "direct"
	Closure  Role = "closure"
)

type Accessibility string

const (
	Yes     Accessibility = "yes"
	No      Accessibility = "no"
	Unknown Accessibility = "unknown"
)

func (a Accessibility) Valid() bool { return a == Yes || a == No || a == Unknown }

type Coordinate struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func (c Coordinate) Valid() bool {
	return !math.IsNaN(c.Latitude) && !math.IsNaN(c.Longitude) && !math.IsInf(c.Latitude, 0) && !math.IsInf(c.Longitude, 0) && c.Latitude >= -90 && c.Latitude <= 90 && c.Longitude >= -180 && c.Longitude <= 180
}

// NodeID belongs to the descriptor's graph namespace. Coordinate is optional corroborating data;
// adapters must document resolution/snapping, never silently substitute a nearby node.
type Endpoint struct {
	NodeID     string      `json:"node_id"`
	Coordinate *Coordinate `json:"coordinate,omitempty"`
}
type Descriptor struct {
	Schema          string   `json:"schema"`
	ProviderID      string   `json:"provider_id"`
	GraphID         string   `json:"graph_id"`
	Version         string   `json:"version"`
	CostModel       string   `json:"cost_model"`
	SnapshotID      string   `json:"snapshot_id"`
	Sources         []string `json:"sources"`
	RoleIndependent bool     `json:"role_independent"`
}
type Options struct {
	Role           Role  `json:"role"`
	MaxSeconds     int64 `json:"max_seconds"`
	AccessibleOnly bool  `json:"accessible_only"`
	// StopAt is an absorbing node: it can be reached but cannot be traversed onward.
	StopAt string `json:"stop_at,omitempty"`
}

func (o Options) Validate() error {
	if o.Role != Access && o.Role != Egress && o.Role != Transfer && o.Role != Direct && o.Role != Closure {
		return fmt.Errorf("%w: role", ErrInvalid)
	}
	if o.MaxSeconds < 0 || o.MaxSeconds > MaxSeconds {
		return fmt.Errorf("%w: duration bound", ErrInvalid)
	}
	return nil
}

type Request struct {
	SnapshotID string   `json:"snapshot_id"`
	From       Endpoint `json:"from"`
	To         Endpoint `json:"to"`
	Options    Options  `json:"options"`
}
type Witness struct {
	Source   string   `json:"source"`
	Revision string   `json:"revision"`
	EdgeIDs  []string `json:"edge_ids"`
}
type Segment struct {
	ID         string        `json:"id"`
	From       string        `json:"from"`
	To         string        `json:"to"`
	Seconds    int64         `json:"seconds"`
	Accessible Accessibility `json:"accessible"`
	Witness    Witness       `json:"witness"`
}
type Path struct {
	SnapshotID string    `json:"snapshot_id"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Seconds    int64     `json:"seconds"`
	Segments   []Segment `json:"segments"`
}
type Status string

const (
	Reachable   Status = "reachable"
	Unreachable Status = "unreachable_within_limit"
)

// A successful empty answer is Unreachable with nil Path. Operational failures return error,
// never Unreachable. Unreachable is relative to the snapshot/options, not an unrestricted map.
type Result struct {
	SnapshotID string `json:"snapshot_id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Status     Status `json:"status"`
	Path       *Path  `json:"path,omitempty"`
}
type DurationResult struct {
	SnapshotID string `json:"snapshot_id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Status     Status `json:"status"`
	Seconds    *int64 `json:"seconds,omitempty"`
}
type MatrixRequest struct {
	SnapshotID string     `json:"snapshot_id"`
	Sources    []Endpoint `json:"sources"`
	Targets    []Endpoint `json:"targets"`
	Options    Options    `json:"options"`
}

// Rows preserve source order and columns preserve target order, including duplicates.
// Both request axes must be nonempty; an empty axis returns ErrInvalid and no rows.
type MatrixResult struct {
	SnapshotID string     `json:"snapshot_id"`
	Rows       [][]Result `json:"rows"`
}
type ReachRequest struct {
	// Targets optionally limits reconstruction to these nodes; nil/empty returns all reachable nodes.
	Targets    []Endpoint `json:"targets,omitempty"`
	SnapshotID string     `json:"snapshot_id"`
	From       Endpoint   `json:"from"`
	Options    Options    `json:"options"`
}

// Reach returns reachable requested targets (all graph nodes if Targets is empty); errors return no partial closure.
type ReachResult struct {
	SnapshotID string `json:"snapshot_id"`
	Paths      []Path `json:"paths"`
}
type Provider interface {
	Descriptor() Descriptor
	Route(context.Context, Request) (Result, error)
}
type DurationProvider interface {
	Provider
	Duration(context.Context, Request) (DurationResult, error)
}
type BatchProvider interface {
	Provider
	Batch(context.Context, []Request) ([]Result, error)
}
type MatrixProvider interface {
	Provider
	Matrix(context.Context, MatrixRequest) (MatrixResult, error)
}
type ReachProvider interface {
	Provider
	Reach(context.Context, ReachRequest) (ReachResult, error)
}

// CheckResult validates a provider's structural witness and binding. It does NOT certify that
// external source geometry/access is truthful: retain and independently check provider evidence.
func CheckResult(req Request, r Result) error {
	if req.From.NodeID == "" || req.To.NodeID == "" {
		return fmt.Errorf("%w: empty endpoint", ErrInvalid)
	}
	if err := req.Options.Validate(); err != nil {
		return err
	}
	if req.SnapshotID == "" || r.SnapshotID != req.SnapshotID {
		return ErrStale
	}
	if r.From != req.From.NodeID || r.To != req.To.NodeID {
		return fmt.Errorf("%w: result endpoints", ErrInvalid)
	}
	if r.Status == Unreachable {
		if r.Path != nil {
			return fmt.Errorf("%w: unreachable path", ErrInvalid)
		}
		return nil
	}
	if r.Status != Reachable || r.Path == nil {
		return fmt.Errorf("%w: missing path", ErrInvalid)
	}
	p := r.Path
	if p.SnapshotID != r.SnapshotID || p.From != r.From || p.To != r.To || p.Seconds < 0 || p.Seconds > req.Options.MaxSeconds {
		return fmt.Errorf("%w: path binding/duration", ErrInvalid)
	}
	at := p.From
	var sum int64
	for _, e := range p.Segments {
		if e.ID == "" || e.From != at || e.To == "" || e.Seconds <= 0 || e.Seconds > MaxSeconds || !e.Accessible.Valid() || e.Witness.Source == "" || e.Witness.Revision == "" || len(e.Witness.EdgeIDs) == 0 {
			return fmt.Errorf("%w: path segment", ErrInvalid)
		}
		for _, id := range e.Witness.EdgeIDs {
			if id == "" {
				return fmt.Errorf("%w: empty witness ID", ErrInvalid)
			}
		}
		if at == req.Options.StopAt || req.Options.AccessibleOnly && e.Accessible != Yes {
			return fmt.Errorf("%w: inaccessible/absorbing segment", ErrInvalid)
		}
		sum += e.Seconds
		if sum > req.Options.MaxSeconds {
			return fmt.Errorf("%w: path exceeds limit", ErrInvalid)
		}
		at = e.To
	}
	if at != p.To || sum != p.Seconds {
		return fmt.Errorf("%w: disconnected or wrong total", ErrInvalid)
	}
	return nil
}
func CloneResult(r Result) Result {
	if r.Path != nil {
		p := *r.Path
		p.Segments = append([]Segment{}, p.Segments...)
		for i := range p.Segments {
			p.Segments[i].Witness.EdgeIDs = append([]string{}, p.Segments[i].Witness.EdgeIDs...)
		}
		r.Path = &p
	}
	return r
}
