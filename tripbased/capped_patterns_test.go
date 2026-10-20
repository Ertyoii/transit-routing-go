package tripbased

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
	"os"
	"reflect"
	"testing"
)

type capCounterexample struct {
	ID                string        `json:"id"`
	Family            string        `json:"family"`
	Dataset           model.Dataset `json:"dataset"`
	Query             model.Query   `json:"query"`
	Expected          []*int64      `json:"expected"`
	Golden            model.Journey `json:"golden"`
	UnboundedQuery    model.Query   `json:"unbounded_query"`
	UnboundedExpected []*int64      `json:"unbounded_expected"`
	Probes            []struct {
		Origin    string   `json:"origin"`
		Departure int64    `json:"departure"`
		Expected  []*int64 `json:"expected"`
	} `json:"probes"`
}

func capCases(t testing.TB) []capCounterexample {
	t.Helper()
	f, e := os.Open("testdata/fixed_caps_cases.jsonl")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 1000000)
	out := []capCounterexample{}
	for s.Scan() {
		var c capCounterexample
		if e := json.Unmarshal(s.Bytes(), &c); e != nil {
			t.Fatal(e)
		}
		out = append(out, c)
	}
	if e := s.Err(); e != nil {
		t.Fatal(e)
	}
	if len(out) != 56 {
		t.Fatal(len(out))
	}
	return out
}
func capPatternConfig(retained bool) PatternConfig {
	c := PatternConfig{Profile: FlashConfig{Semantics: "static_stop_pair_v1", Cells: 1, MaxBoardings: 4, Limits: FlashLimits{MaxStops: 100, MaxTrips: 100, MaxClosurePaths: 10000, MaxWalkSettled: 100000, MaxTransfers: 100000, MaxRuns: 10000, MaxWork: 10000000, MaxFlagBytes: 1000000, MaxWitnesses: 10000, MaxQuerySegments: 100000, MaxSuffixBytes: 1000000, MaxSnapshotTransfers: 10000, MaxRetainedReadiness: 10000, MaxRetainedRuns: 10000, MaxRetainedSources: 100, MaxRetainedQueued: 10000}}, MaxPatterns: 10000, MaxNodes: 10000, MaxEdges: 20000, MaxDirectEntries: 100000, MaxBuildWork: 10000000, MaxQueryWork: 1000000, MaxQueryLabels: 100000}
	if retained {
		c.Profile.ProfilePolicy = "retained_exact_event_v1"
	}
	return c
}
func capConfig(c capCounterexample) DestinationCappedPatternConfig {
	return DestinationCappedPatternConfig{Patterns: capPatternConfig(false), Destination: c.Query.Destination, Walking: *c.Query.Walking, ProfileMaxLegs: 1024, ProfileMaxWalkingSeconds: 6000, MaxProfileSources: 100, MaxProfileQueries: 10000, MaxProfileSegments: 1000000}
}
func capVector(t testing.TB, d model.Dataset, q model.Query, js []model.Journey) []*int64 {
	t.Helper()
	checker, e := validate.New(d)
	if e != nil {
		t.Fatal(e)
	}
	out := make([]*int64, q.MaxBoardings+1)
	for _, j := range js {
		v, e := checker.Check(q, j)
		if e != nil {
			t.Fatal("invalid raw witness", e, j)
		}
		for b := v.Metrics.Boardings; b <= q.MaxBoardings; b++ {
			if out[b] == nil || v.Metrics.Arrival < *out[b] {
				at := v.Metrics.Arrival
				out[b] = &at
			}
		}
	}
	return out
}

// Unsupported test-only intervention. No production entry point changes its
// guard. This disproves the tempting proposal to permit binding caps against an
// unchanged unbounded-optimal pattern union. Always restore the private flag.
func capUnsafeGuardOnly(p *PatternIndex, q model.Query) (PatternResult, error) {
	old := p.base.cfg.WalkingPolicy
	p.base.cfg.WalkingPolicy = "query_role_caps_v1"
	defer func() { p.base.cfg.WalkingPolicy = old }()
	return p.Search(context.Background(), q)
}
func capInjectDiagnosticPattern(t testing.TB, p *PatternIndex, c capCounterexample) {
	t.Helper()
	p.prefix = map[string]map[patternPrefix]int{}
	p.seen = map[string]bool{}
	for source, dag := range p.dags {
		m := map[patternPrefix]int{}
		for i, n := range dag.Nodes {
			if n.Parent >= 0 {
				m[patternPrefix{n.Parent, n.Kind, n.Stop}] = i
			}
		}
		p.prefix[source] = m
	}
	for _, w := range p.snap.Witnesses {
		b, _ := json.Marshal(w.Edges)
		p.seen[string(b)] = true
	}
	if e := p.addPattern(context.Background(), c.Query.Origin, c.Query.Destination, c.Query.Departure, c.Golden); e != nil {
		t.Fatal(e)
	}
}

func TestBindingCapCounterexamplesRejectGuardRemoval(t *testing.T) {
	records := []map[string]any{}
	for _, c := range capCases(t) {
		for _, retained := range []bool{false, true} {
			p, e := BuildPatterns(context.Background(), c.Dataset, capPatternConfig(retained))
			if e != nil {
				t.Fatal(c.ID, e)
			}
			u, e := p.Search(context.Background(), c.UnboundedQuery)
			if e != nil || !u.Complete {
				t.Fatal(c.ID, e, u)
			}
			if got := capVector(t, c.Dataset, c.UnboundedQuery, u.Journeys); !reflect.DeepEqual(got, c.UnboundedExpected) {
				t.Fatal(c.ID, "unbounded model mismatch", got, c.UnboundedExpected)
			}
			if _, e := validate.New(c.Dataset); e != nil {
				t.Fatal(e)
			}
			if got := capVector(t, c.Dataset, c.Query, []model.Journey{c.Golden}); !reflect.DeepEqual(got, c.Expected) {
				t.Fatal("independent golden mismatch", c.ID, got, c.Expected)
			}
			if r, e := p.Search(context.Background(), c.Query); e == nil || r.Complete || len(r.Journeys) != 0 {
				t.Fatal("original guard must remain", c.ID, r, e)
			}
			unsafe, e := capUnsafeGuardOnly(p, c.Query)
			if e != nil || !unsafe.Complete {
				t.Fatal("diagnostic must execute native graph query", c.ID, e)
			}
			if len(unsafe.Journeys) != 0 {
				t.Fatal("generated missing-pattern counterexample did not reproduce", c.ID, unsafe)
			}
			record := map[string]any{"id": c.ID, "retained": retained, "guard_only": unsafe}
			if c.Family == "goal-detour" {
				capInjectDiagnosticPattern(t, p, c)
				unsafe, e = capUnsafeGuardOnly(p, c.Query)
				if e != nil || !unsafe.Complete || len(unsafe.Journeys) == 0 {
					t.Fatal(c.ID, e, unsafe)
				}
				checker, _ := validate.New(c.Dataset)
				invalid := false
				for _, j := range unsafe.Journeys {
					if _, e := checker.Check(c.Query, j); e != nil {
						invalid = true
					}
				}
				if !invalid {
					t.Fatal("inserting correct avoiding-walk witness alone must still expose unsafe unrestricted replay", c.ID, unsafe)
				}
				record["injected_correct_pattern_unrestricted_replay"] = unsafe
			}
			records = append(records, record)
		}
	}
	if path := os.Getenv("CAP_PATTERN_DIAGNOSTIC"); path != "" {
		b, e := json.MarshalIndent(map[string]any{"scope": "unsupported test-only guard removal and witness injection; not a supported method or qualification", "cases": records}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, append(b, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
	t.Log("56 independent frozen-cap counterexamples x fresh/retained: guard removal misses feasible journeys; 32 injected-witness replay checks expose goal-crossing unsoundness")
}

func TestDestinationCappedPatternsIndependentCases(t *testing.T) {
	probes := 0
	evidence := []map[string]any{}
	for _, c := range capCases(t) {
		queries := []map[string]any{}
		x, e := BuildDestinationCappedPatterns(context.Background(), c.Dataset, capConfig(c))
		if e != nil || !x.Complete() {
			t.Fatal(c.ID, e, x.CostSummary())
		}
		r, e := x.Search(context.Background(), c.Query)
		if e != nil || !r.Complete {
			t.Fatal(c.ID, e, r)
		}
		if got := capVector(t, c.Dataset, c.Query, r.Journeys); !reflect.DeepEqual(got, c.Expected) {
			t.Fatal(c.ID, got, c.Expected)
		}
		queries = append(queries, map[string]any{"query": c.Query, "journeys": r.Journeys})
		for boards := 0; boards < c.Query.MaxBoardings; boards++ {
			q := c.Query
			q.MaxBoardings = boards
			answer, e := x.Search(context.Background(), q)
			if e != nil || !answer.Complete {
				t.Fatal(c.ID, e)
			}
			if got := capVector(t, c.Dataset, q, answer.Journeys); !reflect.DeepEqual(got, c.Expected[:boards+1]) {
				t.Fatal("boarding frontier prefix", c.ID, boards, got, c.Expected)
			}
			queries = append(queries, map[string]any{"query": q, "journeys": answer.Journeys})
		}
		for _, probe := range c.Probes {
			q := c.Query
			q.Origin, q.Departure = probe.Origin, probe.Departure
			r, e := x.Search(context.Background(), q)
			if e != nil || !r.Complete {
				t.Fatal(c.ID, q, e)
			}
			if got := capVector(t, c.Dataset, q, r.Journeys); !reflect.DeepEqual(got, probe.Expected) {
				t.Fatal(c.ID, q, got, probe.Expected)
			}
			probes++
			queries = append(queries, map[string]any{"query": q, "journeys": r.Journeys})
		}
		for _, w := range x.Snapshot().Patterns.Witnesses {
			q := c.Query
			q.Origin, q.Departure = w.Source, w.Departure
			q.MaxLegs = 1024
			capVector(t, c.Dataset, q, []model.Journey{w.Journey})
		}
		q := c.Query
		q.LatestArrival = *c.Expected[len(c.Expected)-1] - 1
		r, e = x.Search(context.Background(), q)
		if e != nil || !r.Complete || len(r.Journeys) != 0 {
			t.Fatal("latest-arrival boundary", c.ID, r, e)
		}
		queries = append(queries, map[string]any{"query": q, "journeys": r.Journeys})
		evidence = append(evidence, map[string]any{"id": c.ID, "queries": queries, "emitted": x.Snapshot().Patterns.Witnesses, "stats": x.stats})
	}
	if path := os.Getenv("CAP_PATTERN_EVIDENCE"); path != "" {
		b, e := json.MarshalIndent(map[string]any{"method": fixedCapPatternMethod, "scope": "synthetic finite models only; not full Singapore qualification", "cases": evidence}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, append(b, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("56 frozen-cap models; %d independently enumerated source/readiness probes; original-data witness replay and latest-arrival boundaries", probes)
}

func TestDestinationCappedPatternsScopeAndIncomplete(t *testing.T) {
	var c capCounterexample
	for _, v := range capCases(t) {
		if v.Family == "goal-detour" {
			c = v
			break
		}
	}
	for _, mutate := range []func(*DestinationCappedPatternConfig){
		func(c *DestinationCappedPatternConfig) { c.MaxProfileSources = 1 },
		func(c *DestinationCappedPatternConfig) { c.MaxProfileQueries = 1 },
		func(c *DestinationCappedPatternConfig) { c.MaxProfileSegments = 1 },
		func(c *DestinationCappedPatternConfig) { c.Patterns.MaxPatterns = 1 },
		func(c *DestinationCappedPatternConfig) { c.Patterns.Profile.Limits.MaxWork = 1 },
		func(c *DestinationCappedPatternConfig) { c.Patterns.Profile.ProfilePolicy = "retained_exact_event_v1" },
		func(c *DestinationCappedPatternConfig) { c.ProfileMaxWalkingSeconds = 1 },
	} {
		cfg := capConfig(c)
		mutate(&cfg)
		x, e := BuildDestinationCappedPatterns(context.Background(), c.Dataset, cfg)
		if e == nil || x.Complete() || x.Snapshot().Complete {
			t.Fatal("partial accepted", e, x.CostSummary())
		}
		if r, e := x.Search(context.Background(), c.Query); e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal(r, e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if x, e := BuildDestinationCappedPatterns(ctx, c.Dataset, capConfig(c)); e == nil || x.Complete() {
		t.Fatal(e)
	}
	x, e := BuildDestinationCappedPatterns(context.Background(), c.Dataset, capConfig(c))
	if e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*model.Query){
		func(q *model.Query) { q.Destination = "A" },
		func(q *model.Query) { w := *q.Walking; w.Transfer--; q.Walking = &w },
		func(q *model.Query) { q.TransferBuffer = 1 },
		func(q *model.Query) { q.AccessibleOnly = true },
		func(q *model.Query) { q.Departure = c.Dataset.Window.Start - 1 },
		func(q *model.Query) { q.LatestArrival = c.Dataset.Window.End + 1 },
	} {
		q := c.Query
		mutate(&q)
		if r, e := x.Search(context.Background(), q); e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal("scope mismatch accepted", q, r, e)
		}
	}
	bad := c.Dataset.Clone()
	for i := range bad.Footpaths {
		if bad.Footpaths[i].From == c.Query.Destination {
			bad.Footpaths[i].Duration = 0
		}
	}
	if x, e := BuildDestinationCappedPatterns(context.Background(), bad, capConfig(c)); e == nil || x.Complete() {
		t.Fatal("conditioning hid invalid raw input", e)
	}
}
