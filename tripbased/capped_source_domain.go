package tripbased

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
)

const sourceDomainPatternMethod = "fixed_cap_destination_source_domain_retained_exact_events_v1"

// DestinationSourceDomainConfig declares complete origin coverage for one
// destination/role-cap/window class on the entire supplied network. Sources is
// independent of query departure times. Every source must be an existing,
// distinct non-destination stop; Retained.SourceOrder must be empty.
type DestinationSourceDomainConfig struct {
	Retained DestinationRetainedCappedConfig `json:"retained"`
	Sources  []string                        `json:"sources"`
}

// SourceDomainCompletion is a completed full readiness sweep, never a sample.
type SourceDomainCompletion struct {
	Source             string `json:"source"`
	ReadinessCompleted int    `json:"readiness_completed"`
	LowerSentinel      bool   `json:"lower_sentinel_completed"`
	Complete           bool   `json:"complete"`
}

// DestinationSourceDomainSnapshot contains compact owned evidence. It does not
// clone the full base graph, transfers, or per-readiness raw witnesses.
type DestinationSourceDomainSnapshot struct {
	Method               string                         `json:"method"`
	SourceDatasetID      string                         `json:"source_dataset_id"`
	DomainComplete       bool                           `json:"domain_complete"`
	GlobalComplete       bool                           `json:"global_complete"`
	InnerGlobalComplete  bool                           `json:"inner_global_complete"`
	Error                string                         `json:"error,omitempty"`
	Config               DestinationSourceDomainConfig  `json:"config"`
	Sources              []SourceDomainCompletion       `json:"source_completion"`
	Stats                DestinationCappedPatternStats  `json:"stats"`
	RetainedStats        DestinationRetainedCappedStats `json:"retained_stats"`
	PatternStats         PatternStats                   `json:"pattern_stats"`
	IncidenceOccurrences int                            `json:"stored_incidence_occurrences"`
	FinalizationComplete bool                           `json:"finalization_complete"`
}

// DestinationSourceDomainIndex owns a separate complete-domain certificate.
// It never makes a partial legacy TP index complete. Search shares the graph
// kernel through an unexported owner-bound permit, not through legacy Search.
type DestinationSourceDomainIndex struct {
	legacy   *DestinationCappedPatternIndex
	cfg      DestinationSourceDomainConfig
	proof    *sourceDomainBuild
	permit   *patternSourceDomainPermit
	complete bool
	err      string
}

// A permit cannot be supplied by callers. Both its immutable owning index and
// source-domain proof are checked before the shared query kernel is entered.
type patternSourceDomainPermit struct {
	graph  *PatternIndex
	domain *DestinationSourceDomainIndex
}

type sourceDomainBuild struct {
	origins       []string
	allowed       map[string]bool
	rows          []SourceDomainCompletion
	started       int
	completed     int
	active        int
	lastReadiness int64
	finalized     bool
	occurrences   int
}

// BuildDestinationSourceDomainPatterns completes every exact feasible access
// readiness plus the lower sentinel for every declared source, and finalizes
// full incidence before issuing its separate domain certificate. Guard errors
// and cancellations return an unanswerable domain index; no partial serving.
func BuildDestinationSourceDomainPatterns(ctx context.Context, d model.Dataset, c DestinationSourceDomainConfig) (out *DestinationSourceDomainIndex, err error) {
	out = &DestinationSourceDomainIndex{}
	defer func() {
		if err != nil {
			out.err = err.Error()
			out.complete = false
			out.permit = nil
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	c.Sources = append([]string(nil), c.Sources...)
	c.Retained.SourceOrder = append([]string(nil), c.Retained.SourceOrder...)
	out.cfg = c
	if len(c.Sources) == 0 || len(c.Retained.SourceOrder) != 0 {
		err = fmt.Errorf("source-domain TP requires nonempty explicit sources and no global source-order override")
		return
	}
	known := map[string]bool{}
	for _, stop := range d.Stops {
		known[stop.ID] = true
	}
	allowed := map[string]bool{}
	for _, source := range c.Sources {
		if source == "" || !known[source] || source == c.Retained.Scope.Destination || allowed[source] {
			err = fmt.Errorf("source-domain TP requires distinct existing non-destination sources")
			return
		}
		allowed[source] = true
	}
	sort.Strings(c.Sources)
	out.cfg = c
	if c.Retained.Scope.MaxProfileSources < len(c.Sources) {
		err = fmt.Errorf("source-domain TP source budget cannot cover its declared domain")
		return
	}
	proof := &sourceDomainBuild{origins: append([]string(nil), c.Sources...), allowed: allowed, active: -1}
	for _, source := range proof.origins {
		proof.rows = append(proof.rows, SourceDomainCompletion{Source: source})
	}
	out.proof = proof
	out.legacy, err = buildDestinationCappedPatternsForDomain(ctx, d, c.Retained.Scope, &c.Retained, proof)
	if err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if !proof.finalized || proof.completed != len(proof.origins) || proof.started != proof.completed || proof.active != -1 || out.legacy == nil || out.legacy.Complete() || out.legacy.p.Complete() {
		err = fmt.Errorf("source-domain TP incomplete construction certificate")
		return
	}
	count := 0
	for _, row := range proof.rows {
		if !row.Complete || !row.LowerSentinel || row.ReadinessCompleted < 1 {
			err = fmt.Errorf("source-domain TP missing completed source/readiness sentinel")
			return
		}
		count += row.ReadinessCompleted
	}
	s := out.legacy.stats
	if s.SourcesStarted != len(proof.origins) || s.SourcesCompleted != len(proof.origins) || s.OfferedReadiness != count || s.NativeProfileQueries != 0 || out.legacy.retained.Stats.QueuedSegments != out.legacy.retained.Stats.ProcessedSegments {
		err = fmt.Errorf("source-domain TP incomplete source/readiness/queue accounting")
		return
	}
	out.complete = true
	out.permit = &patternSourceDomainPermit{graph: out.legacy.p, domain: out}
	return
}

func (x *DestinationSourceDomainIndex) DomainComplete() bool {
	return x != nil && x.complete && x.proof != nil && x.proof.finalized && x.proof.completed == len(x.proof.origins) && x.proof.started == x.proof.completed && x.proof.active == -1 && x.legacy != nil && x.legacy.p != nil && !x.legacy.Complete() && !x.legacy.p.Complete() && x.permit != nil && x.permit.domain == x && x.permit.graph == x.legacy.p
}

// GlobalComplete reports legacy all-source certification, which this distinct
// domain constructor never issues, even if its explicit domain is large.
func (x *DestinationSourceDomainIndex) GlobalComplete() bool {
	return x != nil && x.legacy != nil && x.legacy.Complete()
}

func (x *DestinationSourceDomainIndex) Snapshot() DestinationSourceDomainSnapshot {
	s := DestinationSourceDomainSnapshot{Method: sourceDomainPatternMethod}
	if x == nil {
		return s
	}
	s.DomainComplete, s.GlobalComplete, s.Error = x.DomainComplete(), x.GlobalComplete(), x.err
	s.Config = x.cfg
	s.Config.Sources = append([]string(nil), x.cfg.Sources...)
	s.Config.Retained.SourceOrder = append([]string(nil), x.cfg.Retained.SourceOrder...)
	if x.proof != nil {
		s.Sources = append([]SourceDomainCompletion(nil), x.proof.rows...)
		s.FinalizationComplete, s.IncidenceOccurrences = x.proof.finalized, x.proof.occurrences
	}
	if x.legacy != nil {
		s.SourceDatasetID, s.Stats = x.legacy.sourceID, x.legacy.stats
		if x.legacy.retained != nil {
			s.RetainedStats = x.legacy.retained.Stats
		}
		if x.legacy.p != nil {
			s.InnerGlobalComplete, s.PatternStats = x.legacy.p.Complete(), x.legacy.p.snap.Stats
		}
	}
	return s
}

func (x *DestinationSourceDomainIndex) CostSummary() json.RawMessage {
	v := map[string]any{"domain": x.Snapshot()}
	if x != nil && x.legacy != nil {
		v["legacy_global_incomplete"] = x.legacy.CostSummary()
	}
	b, _ := json.Marshal(v)
	return b
}

func (permit *patternSourceDomainPermit) check(graph *PatternIndex, input model.Query) error {
	if permit == nil || permit.graph != graph || permit.domain == nil || permit.domain.permit != permit || !permit.domain.DomainComplete() || permit.domain.legacy.p != graph {
		return fmt.Errorf("source-domain TP incomplete or foreign graph certificate")
	}
	if !permit.domain.proof.allowed[input.Origin] {
		return fmt.Errorf("source-domain TP capability: origin outside declared domain")
	}
	return nil
}

func (x *DestinationSourceDomainIndex) Search(ctx context.Context, q model.Query) (PatternResult, error) {
	if !x.DomainComplete() {
		err := fmt.Errorf("source-domain TP incomplete domain cannot answer queries")
		return PatternResult{Journeys: []model.Journey{}, Graph: []PatternEdge{}, Error: err.Error()}, err
	}
	return x.legacy.p.searchGraphWithSourceDomain(ctx, q, nil, x.legacy.checkQuery, x.permit)
}

func (proof *sourceDomainBuild) start(source string) error {
	if proof.active != -1 || proof.started >= len(proof.origins) || proof.origins[proof.started] != source {
		return fmt.Errorf("source-domain TP source sequence differs from declaration")
	}
	proof.active = proof.started
	proof.started++
	proof.lastReadiness = model.MaxServiceSecond + 1
	return nil
}

func (proof *sourceDomainBuild) emitted(source string, ready, lower int64) error {
	if proof.active < 0 || proof.rows[proof.active].Source != source || ready >= proof.lastReadiness || ready < lower {
		return fmt.Errorf("source-domain TP readiness sequence incomplete or unordered")
	}
	proof.rows[proof.active].ReadinessCompleted++
	proof.rows[proof.active].LowerSentinel = ready == lower
	proof.lastReadiness = ready
	return nil
}

func (proof *sourceDomainBuild) finish(source string, pending int) error {
	if proof.active < 0 || proof.rows[proof.active].Source != source || pending != 0 || !proof.rows[proof.active].LowerSentinel || proof.rows[proof.active].ReadinessCompleted < 1 {
		return fmt.Errorf("source-domain TP source missing drained readiness/sentinel")
	}
	proof.rows[proof.active].Complete = true
	proof.completed++
	proof.active = -1
	return nil
}
