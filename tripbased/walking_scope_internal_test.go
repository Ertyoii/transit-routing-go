package tripbased

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

// Independently enumerate all SIMPLE raw directed paths. Positive edges mean
// some simple path attains every shortest distance; no production closure,
// Dijkstra, dominance, transfer preparation or search helper is used here.
func rawSimpleWalking(d model.Dataset, source string, radius int64) map[string]int64 {
	out := map[string]int64{source: 0}
	seen := map[string]bool{source: true}
	var visit func(string, int64)
	visit = func(at string, distance int64) {
		for _, edge := range d.Footpaths {
			if edge.From != at || seen[edge.To] || distance+edge.Duration > radius {
				continue
			}
			next := distance + edge.Duration
			if old, ok := out[edge.To]; !ok || next < old {
				out[edge.To] = next
			}
			seen[edge.To] = true
			visit(edge.To, next)
			delete(seen, edge.To)
		}
	}
	visit(source, 0)
	return out
}

func TestWalkingScopeExactDirectedRadius(t *testing.T) {
	names := []string{"A", "B", "C", "D", "E", "X"}
	d := model.Dataset{Footpaths: []model.Footpath{
		{ID: "ab", From: "A", To: "B", Duration: 2},
		{ID: "bc", From: "B", To: "C", Duration: 2},
		{ID: "cd", From: "C", To: "D", Duration: 2},
		{ID: "da", From: "D", To: "A", Duration: 2},
		{ID: "ae", From: "A", To: "E", Duration: 10},
		{ID: "ax", From: "A", To: "X", Duration: 1},
		{ID: "xe", From: "X", To: "E", Duration: 3},
	}}
	for _, radius := range []int64{0, 1, 3, 4, 6} {
		x := &FlashIndex{names: names, ids: map[string]int{}, cfg: FlashConfig{Limits: FlashLimits{MaxWork: 10000, MaxWalkSettled: 1000, MaxClosurePaths: 1000}}}
		for i, name := range names {
			x.ids[name] = i
		}
		if e := x.closeWalksWithin(d, &flashWork{context.Background(), x}, &radius); e != nil {
			t.Fatal(e)
		}
		for source, name := range names {
			got := map[string]int64{}
			for _, p := range x.walks[source] {
				got[p.To] = p.Duration
				at, cost := name, int64(0)
				for _, edge := range p.Edges {
					if edge.From != at {
						t.Fatal("lost directed intermediate", p)
					}
					at, cost = edge.To, cost+edge.Duration
				}
				if at != p.To || cost != p.Duration || cost > radius {
					t.Fatal("invalid bounded witness", p)
				}
			}
			if want := rawSimpleWalking(d, name, radius); !reflect.DeepEqual(got, want) {
				t.Fatal(radius, name, got, want)
			}
		}
	}
}

// Check endpoint searches independently of any router, including parallel
// equal-cost alternatives and destination absorption. The raw simple-path
// enumeration determines distances; the complete closure additionally checks
// that radius truncation preserves the legacy deterministic representative.
func TestWalkingScopeEndpointRawPathOracle(t *testing.T) {
	const n = 7
	for seed := int64(0); seed < 16; seed++ {
		rng := rand.New(rand.NewSource(seed))
		d := model.Dataset{}
		x := &TREXIndex{names: make([]string, n), ids: map[string]int{},
			cfg:  TREXConfig{Limits: TREXLimits{MaxWalkSettled: 10000}},
			base: &Index{p: &paper.Profile{Outgoing: map[string][]model.Footpath{}}}}
		for i := range x.names {
			x.names[i] = fmt.Sprintf("s%d", i)
			x.ids[x.names[i]] = i
		}
		for i := 0; i < 24; i++ {
			from, to := rng.Intn(n), rng.Intn(n)
			e := model.Footpath{ID: fmt.Sprintf("e%02d", i), From: x.names[from], To: x.names[to], Duration: int64(1 + rng.Intn(5))}
			d.Footpaths = append(d.Footpaths, e)
			x.base.p.Outgoing[e.From] = append(x.base.p.Outgoing[e.From], e)
		}
		for _, edges := range x.base.p.Outgoing {
			sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
		}
		for _, stopAt := range []int{-1, n - 1} {
			conditioned := model.Dataset{}
			for _, e := range d.Footpaths {
				if stopAt < 0 || e.From != x.names[stopAt] {
					conditioned.Footpaths = append(conditioned.Footpaths, e)
				}
			}
			full := &FlashIndex{names: x.names, ids: x.ids, cfg: FlashConfig{Limits: FlashLimits{MaxWork: 100000, MaxWalkSettled: 10000, MaxClosurePaths: 10000}}}
			if e := full.closeWalks(conditioned, &flashWork{context.Background(), full}); e != nil {
				t.Fatal(e)
			}
			for _, radius := range []int64{0, 1, 5, 12} {
				for source := range x.names {
					stats := TREXQueryStats{}
					charge := func() error { stats.Work++; return nil }
					paths, e := x.endpointWalks(context.Background(), source, -1, radius, stopAt, charge, &stats)
					if e != nil {
						t.Fatal(e)
					}
					got := map[string]int64{}
					for _, p := range paths {
						got[p.To] = p.Duration
						if want := full.walkMap[source][x.ids[p.To]]; want == nil || !reflect.DeepEqual(p, *want) {
							t.Fatal("endpoint representative changed", seed, stopAt, radius, source, p, want)
						}
						targetStats := TREXQueryStats{}
						one, e := x.endpointWalks(context.Background(), source, x.ids[p.To], radius, stopAt, func() error { targetStats.Work++; return nil }, &targetStats)
						if e != nil || len(one) != 1 || !reflect.DeepEqual(one[0], p) {
							t.Fatal("target-only endpoint representative changed", one, p, e)
						}
					}
					if want := rawSimpleWalking(conditioned, x.names[source], radius); !reflect.DeepEqual(got, want) || stats.EndpointWalkWork != stats.Work || stats.EndpointWalkSettled != len(got) {
						t.Fatal("raw endpoint relation/accounting", seed, stopAt, radius, source, got, want, stats)
					}
				}
			}
		}
	}
}

func TestWalkingScopeTREXLegacyFallbackAndGuards(t *testing.T) {
	d, q, _, cfg := fixedRankLiteral(t)
	// Add a long one-way walking-only tail. It must not enter the radius or
	// make a legacy nonbinding check accidentally use the truncated diameter.
	d.Stops = append(d.Stops, model.Stop{ID: "tail", Accessible: model.Yes})
	d.Footpaths = append(d.Footpaths, model.Footpath{ID: "long-tail", From: q.Origin, To: "tail", Duration: 1000, Accessible: model.Yes})
	cfg.Partition["tail"] = cfg.Partition[q.Origin]
	x, e := BuildTREXFixedTransferCapRankedCandidate(context.Background(), d, cfg, 900)
	if e != nil {
		t.Fatal(e)
	}
	if x.snap.WalkingClosureLimit == nil || x.snap.ClosureDiameter >= 1000 || x.walkMap[x.ids[q.Origin]][x.ids["tail"]] != nil {
		t.Fatal("transfer closure was not radius-bounded")
	}
	before, _ := json.Marshal(x.Snapshot())
	for _, search := range []func(context.Context, model.Query) (TREXResult, error){x.Search, x.SearchRanked, x.SearchUnranked} {
		if r, e := search(context.Background(), q); e == nil || r.Complete || !strings.Contains(e.Error(), "cap may bind") {
			t.Fatal("truncated metric certified a legacy query", r, e)
		}
	}
	after, _ := json.Marshal(x.Snapshot())
	if string(before) != string(after) {
		t.Fatal("query-local fallback mutated prepared index")
	}
	legacy, e := BuildTREXStaticRulesCandidate(context.Background(), d, cfg)
	if e != nil || legacy.snap.WalkingClosureLimit != nil || legacy.snap.ClosureDiameter < 1000 {
		t.Fatal("unrestricted builder changed", e)
	}
	// For a nonbinding query, every original legacy mode must retain its full
	// metric semantics and ordered representatives, despite bounded storage.
	wide := q
	wide.Walking = &model.WalkingLimits{Access: 1000, Transfer: 1000, Egress: 1000, Direct: 1000}
	wide.MaxWalkSeconds = model.MaxServiceSecond
	control := *x
	control.snap = x.snap
	control.snap.WalkingClosureLimit = nil
	control.snap.ClosureDiameter = legacy.snap.ClosureDiameter
	control.walks, control.walkMap = legacy.walks, legacy.walkMap
	for _, mode := range []string{"overlay", "rank-test", "unranked"} {
		want, e := control.searchTREX(context.Background(), wide, mode)
		got, err := x.searchTREX(context.Background(), wide, mode)
		if e != nil || err != nil || !got.Complete || !reflect.DeepEqual(got.Journeys, want.Journeys) || got.Stats.EndpointWalkWork == 0 {
			t.Fatal("legacy full-metric fallback changed representatives", mode, got, want, e, err)
		}
	}
	// Complete-metric reconstruction must remain query-local under concurrent
	// legacy queries, not merely under the bounded adapter's endpoint searches.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, e := x.Search(context.Background(), wide); e != nil || !r.Complete {
				t.Error("concurrent complete-metric fallback", r, e)
			}
		}()
	}
	wg.Wait()
	after, _ = json.Marshal(x.Snapshot())
	if string(before) != string(after) {
		t.Fatal("concurrent complete-metric fallback mutated prepared index")
	}
	budgeted := *x
	budgeted.cfg.Limits.MaxQueryWork = 1
	if r, e := budgeted.Search(context.Background(), wide); e == nil || r.Complete || r.Stats.Work > 1 || r.Stats.EndpointWalkWork > 1 {
		t.Fatal("legacy closure ignored query work bound", r, e)
	}
	diagnostic := x.Diagnostic()
	*diagnostic.WalkingClosureLimit = 123
	snapshot := x.Snapshot()
	*snapshot.WalkingClosureLimit = 456
	if *x.snap.WalkingClosureLimit != 900 {
		t.Fatal("radius metadata aliases index")
	}
	for _, kind := range []string{"work", "settled", "cancelled"} {
		view := *x
		ctx := context.Background()
		query := q
		if kind == "work" {
			view.cfg.Limits.MaxQueryWork = 1
		}
		if kind == "settled" {
			view.cfg.Limits.MaxWalkSettled = 1
			w := *q.Walking
			w.Access = 1000
			query.Walking = &w
			query.MaxWalkSeconds = model.MaxServiceSecond
		}
		if kind == "cancelled" {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		r, e := view.searchTREXForCertificate(ctx, query, "overlay-role-caps", true)
		if e == nil || r.Complete {
			t.Fatal("query-local endpoint guard bypassed", kind, r, e)
		}
		if kind == "settled" && !strings.Contains(e.Error(), "settlement budget") {
			t.Fatal("settlement guard test failed for another reason", e)
		}
	}
}

func TestWalkingScopeExistingGuardsRemainBinding(t *testing.T) {
	const n = 100
	d := model.Dataset{}
	newCarrier := func() *FlashIndex {
		x := &FlashIndex{ids: map[string]int{}, cfg: FlashConfig{Limits: FlashLimits{MaxWork: 10000, MaxWalkSettled: 300, MaxClosurePaths: 300}}}
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("%03d", i)
			x.names = append(x.names, name)
			x.ids[name] = i
		}
		return x
	}
	x := newCarrier()
	for i := 0; i+1 < n; i++ {
		d.Footpaths = append(d.Footpaths, model.Footpath{ID: fmt.Sprintf("e%03d", i), From: x.names[i], To: x.names[i+1], Duration: 1})
	}
	radius := int64(2)
	if e := x.closeWalksWithin(d, &flashWork{context.Background(), x}, &radius); e != nil || x.snap.Stats.WalkSettled != 3*n-3 || x.snap.Stats.ClosurePaths != 3*n-3 || x.snap.ClosureDiameter != 2 {
		t.Fatal("bounded closure did not complete under original guards", x.snap, e)
	}
	legacy := newCarrier()
	if e := legacy.closeWalks(d, &flashWork{context.Background(), legacy}); e == nil || !strings.Contains(e.Error(), "budget exhausted") {
		t.Fatal("complete legacy closure should still hit the unchanged guards", e)
	}
}
