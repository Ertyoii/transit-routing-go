package graph

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ertyoii/transit-routing-go/walking"
)

func sessionFixture(seed int64) Data {
	rng := rand.New(rand.NewSource(seed))
	d := Data{Descriptor: walking.Descriptor{ProviderID: "session-test", GraphID: fmt.Sprint(seed), Version: "1", CostModel: "seconds", RoleIndependent: true}}
	for i := 0; i < 15; i++ {
		access := walking.Yes
		if i%5 == 0 {
			access = walking.Unknown
		}
		d.Nodes = append(d.Nodes, Node{ID: fmt.Sprintf("n%02d", i), Accessible: access})
	}
	for i := 0; i < 85; i++ {
		from, to := rng.Intn(len(d.Nodes)), rng.Intn(len(d.Nodes))
		access := walking.Yes
		if i%7 == 0 {
			access = walking.No
		}
		id := fmt.Sprintf("e%03d", i)
		d.Edges = append(d.Edges, walking.Segment{ID: id, From: d.Nodes[from].ID, To: d.Nodes[to].ID, Seconds: int64(1 + rng.Intn(4)), Accessible: access, Witness: walking.Witness{Source: "fixture", Revision: "1", EdgeIDs: []string{id, "original"}}})
	}
	// Deliberately scramble caller order. Both queues must still break ties by
	// lexical node ID and outgoing edge ID after canonical graph preparation.
	rng.Shuffle(len(d.Nodes), func(i, j int) { d.Nodes[i], d.Nodes[j] = d.Nodes[j], d.Nodes[i] })
	rng.Shuffle(len(d.Edges), func(i, j int) { d.Edges[i], d.Edges[j] = d.Edges[j], d.Edges[i] })
	return d
}

func compareSession(t *testing.T, g *Graph, s *ReachSession, req walking.ReachRequest, selected *TargetSet) {
	t.Helper()
	want, wantErr := g.ReachWithTargets(context.Background(), req, selected)
	got, gotErr := s.ReachWithTargets(context.Background(), req, selected)
	if !reflect.DeepEqual(want, got) || (wantErr == nil) != (gotErr == nil) || wantErr != nil && wantErr.Error() != gotErr.Error() {
		t.Fatalf("session differs: req=%+v selected=%+v\nwant=%+v %v\ngot=%+v %v", req, selected, want, wantErr, got, gotErr)
	}
	for _, p := range got.Paths {
		r := walking.Request{SnapshotID: req.SnapshotID, From: req.From, To: walking.Endpoint{NodeID: p.To}, Options: req.Options}
		if err := g.ValidatePath(r, walking.Result{SnapshotID: req.SnapshotID, From: p.From, To: p.To, Status: walking.Reachable, Path: &p}); err != nil {
			t.Fatal("invalid returned witness", err)
		}
	}
}

func TestReachSessionMatchesMapSearchExactly(t *testing.T) {
	for seed := int64(1); seed <= 8; seed++ {
		g, err := New(sessionFixture(seed), DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		s := g.NewReachSession()
		empty, err := g.PrepareTargets(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := g.PrepareTargets(context.Background(), []walking.Endpoint{{NodeID: "n04"}, {NodeID: "n10"}, {NodeID: "n04"}})
		if err != nil {
			t.Fatal(err)
		}
		for origin := 0; origin < 15; origin++ {
			for _, cap := range []int64{0, 1, 3, 8, walking.MaxSeconds} {
				for _, accessible := range []bool{false, true} {
					for _, stopAt := range []string{"", "n04", fmt.Sprintf("n%02d", origin)} {
						for i, selection := range []*TargetSet{nil, empty, selected} {
							req := walking.ReachRequest{SnapshotID: g.descriptor.SnapshotID, From: walking.Endpoint{NodeID: fmt.Sprintf("n%02d", origin)}, Options: walking.Options{Role: walking.Closure, MaxSeconds: cap, AccessibleOnly: accessible, StopAt: stopAt}}
							if i%2 == 0 {
								req.Targets = []walking.Endpoint{{NodeID: "n12"}, {NodeID: "n01"}, {NodeID: "n12"}}
							}
							compareSession(t, g, s, req, selection)
						}
					}
				}
			}
		}
	}
}

func TestReachSessionBudgetsAndInvalidRequests(t *testing.T) {
	for _, modify := range []func(*Limits){
		func(l *Limits) { l.MaxSettled = 1 },
		func(l *Limits) { l.MaxSettled = 4 },
		func(l *Limits) { l.MaxEdgeVisits = 1 },
		func(l *Limits) { l.MaxEdgeVisits = 13 },
		func(l *Limits) { l.MaxResultSegments = 1 },
		func(l *Limits) { l.MaxResultSegments = 7 },
	} {
		limits := DefaultLimits()
		modify(&limits)
		g, err := New(sessionFixture(27), limits)
		if err != nil {
			t.Fatal(err)
		}
		s := g.NewReachSession()
		for _, origin := range []string{"n00", "n03", "n14"} {
			req := walking.ReachRequest{SnapshotID: g.descriptor.SnapshotID, From: walking.Endpoint{NodeID: origin}, Options: walking.Options{Role: walking.Closure, MaxSeconds: 100}}
			compareSession(t, g, s, req, nil)
			req.Options.MaxSeconds = 0
			compareSession(t, g, s, req, nil)
		}
	}
	g, err := New(sessionFixture(5), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	s := g.NewReachSession()
	base := walking.ReachRequest{SnapshotID: g.descriptor.SnapshotID, From: walking.Endpoint{NodeID: "n03"}, Options: walking.Options{Role: walking.Closure, MaxSeconds: 8}}
	for _, change := range []func(*walking.ReachRequest){
		func(r *walking.ReachRequest) { r.SnapshotID = "stale" },
		func(r *walking.ReachRequest) { r.From.NodeID = "missing" },
		func(r *walking.ReachRequest) { r.Options.Role = "invalid" },
		func(r *walking.ReachRequest) { r.Options.MaxSeconds = -1 },
		func(r *walking.ReachRequest) { r.Options.StopAt = "missing" },
		func(r *walking.ReachRequest) { r.Targets = []walking.Endpoint{{NodeID: "missing"}} },
	} {
		req := base
		change(&req)
		compareSession(t, g, s, req, nil)
		compareSession(t, g, s, base, nil)
	}
	stale := &TargetSet{snapshot: "stale"}
	compareSession(t, g, s, base, stale)
}

type cancelAfterChecks struct {
	context.Context
	remaining int
}

func (c *cancelAfterChecks) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestReachSessionCancellationOwnershipConcurrencyAndWrap(t *testing.T) {
	g, err := New(sessionFixture(9), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	s := g.NewReachSession()
	req := walking.ReachRequest{SnapshotID: g.descriptor.SnapshotID, From: walking.Endpoint{NodeID: "n03"}, Options: walking.Options{Role: walking.Closure, MaxSeconds: 100}}
	want, err := g.ReachWithTargets(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, checks := range []int{1, 3, 11, 30, 75, 140} {
		got, err := s.ReachWithTargets(&cancelAfterChecks{Context: context.Background(), remaining: checks}, req, nil)
		if !errors.Is(err, context.Canceled) || got.Paths != nil {
			t.Fatalf("partial cancellation response after %d checks: %+v %v", checks, got, err)
		}
		compareSession(t, g, s, req, nil)
	}
	// Scratch reuse and generation rollover must not leak previously seen nodes.
	s.scratch.generation = ^uint32(0)
	for i := range s.scratch.seen {
		s.scratch.seen[i] = 1
	}
	short := req
	short.Options.MaxSeconds = 0
	compareSession(t, g, s, short, nil)
	got, err := s.ReachWithTargets(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range got.Paths {
		for j := range got.Paths[i].Segments {
			got.Paths[i].Segments[j].ID = "changed"
			got.Paths[i].Segments[j].Witness.EdgeIDs[0] = "changed"
		}
	}
	compareSession(t, g, s, req, nil)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.ReachWithTargets(context.Background(), req, nil)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("shared session differs: %v", err)
			}
			other := g.NewReachSession()
			got, err = other.ReachWithTargets(context.Background(), req, nil)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("independent session differs: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestReachSessionLexicalNodeAndParallelEdgeTies(t *testing.T) {
	d := Data{Descriptor: walking.Descriptor{ProviderID: "tie-test", GraphID: "ties", Version: "1", CostModel: "seconds", RoleIndependent: true}}
	for _, id := range []string{"source", "n2", "target", "n10"} {
		d.Nodes = append(d.Nodes, Node{ID: id, Accessible: walking.Yes})
	}
	for _, e := range []struct{ id, from, to string }{
		{"a-source-n2", "source", "n2"},
		{"c-source-n10", "source", "n10"},
		{"b-source-n10", "source", "n10"},
		{"d-n2-target", "n2", "target"},
		{"e-n10-target", "n10", "target"},
	} {
		d.Edges = append(d.Edges, walking.Segment{ID: e.id, From: e.from, To: e.to, Seconds: 1, Accessible: walking.Yes, Witness: walking.Witness{Source: "fixture", Revision: "1", EdgeIDs: []string{e.id}}})
	}
	g, err := New(d, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	req := walking.ReachRequest{SnapshotID: g.descriptor.SnapshotID, From: walking.Endpoint{NodeID: "source"}, Targets: []walking.Endpoint{{NodeID: "target"}}, Options: walking.Options{Role: walking.Closure, MaxSeconds: 2}}
	for _, role := range []walking.Role{walking.Access, walking.Egress, walking.Transfer, walking.Direct, walking.Closure} {
		req.Options.Role = role
		got, err := g.NewReachSession().ReachWithTargets(context.Background(), req, nil)
		if err != nil || len(got.Paths) != 1 || len(got.Paths[0].Segments) != 2 || got.Paths[0].Segments[0].ID != "b-source-n10" || got.Paths[0].Segments[1].ID != "e-n10-target" {
			t.Fatalf("tie witness changed for %s: %+v %v", role, got, err)
		}
	}
}

func BenchmarkReachStorage(b *testing.B) {
	g, err := New(sessionFixture(17), DefaultLimits())
	if err != nil {
		b.Fatal(err)
	}
	req := walking.ReachRequest{SnapshotID: g.descriptor.SnapshotID, From: walking.Endpoint{NodeID: "n03"}, Options: walking.Options{Role: walking.Closure, MaxSeconds: 8}}
	b.Run("map-per-closure", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := g.ReachWithTargets(context.Background(), req, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("dense-query-session", func(b *testing.B) {
		s := g.NewReachSession()
		// As in a real Walker, one session is reused across its source closures.
		if _, err := s.ReachWithTargets(context.Background(), req, nil); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := s.ReachWithTargets(context.Background(), req, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}

type observedContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *observedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestReachSessionCanceledWaiterReturnsWithoutAcquiringGate(t *testing.T) {
	g, err := New(sessionFixture(21), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	s := g.NewReachSession()
	s.gate <- struct{}{}
	inner, cancel := context.WithCancel(context.Background())
	ctx := &observedContext{Context: inner, checked: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		got, err := s.ReachWithTargets(ctx, walking.ReachRequest{}, nil)
		if got.Paths != nil {
			done <- fmt.Errorf("partial canceled response")
			return
		}
		done <- err
	}()
	<-ctx.checked
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		<-s.gate
		t.Fatal("canceled request waited for active session")
	}
	<-s.gate
}

// Frozen b102816 append-then-CloneResult reconstruction, retained only as exact
// output evidence for the new exact-sized owned-witness implementation.
func frozenResultV1(ctx context.Context, g *Graph, from, to string, s search, remaining *int) (walking.Result, error) {
	r := walking.Result{SnapshotID: g.descriptor.SnapshotID, From: from, To: to, Status: walking.Unreachable}
	seconds, ok := s.dist[to]
	if !ok {
		return r, nil
	}
	p := walking.Path{SnapshotID: r.SnapshotID, From: from, To: to, Seconds: seconds, Segments: []walking.Segment{}}
	for at := to; at != from; {
		if e := ctx.Err(); e != nil {
			return walking.Result{}, e
		}
		if *remaining <= 0 {
			return walking.Result{}, walking.ErrBudget
		}
		*remaining--
		edge := s.prev[at]
		p.Segments = append(p.Segments, edge)
		at = edge.From
	}
	for i, j := 0, len(p.Segments)-1; i < j; i, j = i+1, j-1 {
		p.Segments[i], p.Segments[j] = p.Segments[j], p.Segments[i]
	}
	r.Status = walking.Reachable
	r.Path = &p
	return walking.CloneResult(r), nil
}

func TestOwnedReconstructionMatchesFrozenResult(t *testing.T) {
	ctx := context.Background()
	for seed := int64(1); seed <= 4; seed++ {
		g, err := New(sessionFixture(seed), DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		for _, from := range g.nodeIDs {
			s, err := g.search(ctx, from, walking.Options{Role: walking.Closure, MaxSeconds: 8})
			if err != nil {
				t.Fatal(err)
			}
			for _, to := range g.nodeIDs {
				for _, budget := range []int{0, 1, 2, 3, 1000} {
					a, b := budget, budget
					want, wantErr := frozenResultV1(ctx, g, from, to, s, &a)
					got, gotErr := g.result(ctx, from, to, s, &b)
					if !reflect.DeepEqual(want, got) || a != b || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) {
						t.Fatalf("owned reconstruction differs: %s->%s budget %d: %+v %v / %+v %v, remaining %d/%d", from, to, budget, want, wantErr, got, gotErr, a, b)
					}
				}
			}
		}
	}
}

func TestOwnedResponsesRemainIndependentAcrossReuseAndCells(t *testing.T) {
	ctx := context.Background()
	g, err := New(sessionFixture(33), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	s := g.NewReachSession()
	req := walking.ReachRequest{SnapshotID: g.descriptor.SnapshotID, From: walking.Endpoint{NodeID: "n03"}, Options: walking.Options{Role: walking.Closure, MaxSeconds: 8}}
	first, err := s.ReachWithTargets(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := g.ReachWithTargets(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := req
	next.From.NodeID = "n10"
	next.Options.MaxSeconds = 1
	if _, err := s.ReachWithTargets(ctx, next, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatal("scratch reuse changed an existing response")
	}
	// Locate a repeated edge across paths, and mutate just one occurrence.
	type position struct{ path, segment int }
	seen := map[string]position{}
	checked := false
	for i := range first.Paths {
		for j, edge := range first.Paths[i].Segments {
			if old, ok := seen[edge.ID]; ok {
				first.Paths[old.path].Segments[old.segment].Witness.EdgeIDs[0] = "changed"
				if first.Paths[i].Segments[j].Witness.EdgeIDs[0] != edge.ID {
					t.Fatal("paths share witness ownership")
				}
				checked = true
				break
			}
			seen[edge.ID] = position{i, j}
		}
		if checked {
			break
		}
	}
	if !checked {
		t.Fatal("fixture needs a repeated edge")
	}
	var target string
	for _, p := range want.Paths {
		if len(p.Segments) > 0 {
			target = p.To
			break
		}
	}
	r := walking.Request{SnapshotID: req.SnapshotID, From: req.From, To: walking.Endpoint{NodeID: target}, Options: req.Options}
	batch, err := g.Batch(ctx, []walking.Request{r, r})
	if err != nil {
		t.Fatal(err)
	}
	batch[0].Path.Segments[0].Witness.EdgeIDs[0] = "batch-change"
	if batch[1].Path.Segments[0].Witness.EdgeIDs[0] == "batch-change" {
		t.Fatal("duplicate batch results alias")
	}
	matrix, err := g.Matrix(ctx, walking.MatrixRequest{SnapshotID: req.SnapshotID, Sources: []walking.Endpoint{req.From, req.From}, Targets: []walking.Endpoint{r.To, r.To}, Options: req.Options})
	if err != nil {
		t.Fatal(err)
	}
	matrix.Rows[0][0].Path.Segments[0].Witness.EdgeIDs[0] = "matrix-change"
	for _, cell := range []walking.Result{matrix.Rows[0][1], matrix.Rows[1][0], matrix.Rows[1][1]} {
		if cell.Path.Segments[0].Witness.EdgeIDs[0] == "matrix-change" {
			t.Fatal("duplicate matrix cells alias")
		}
	}
	compareSession(t, g, s, req, nil)
}

func TestReachSessionValueCopySharesGenerationAndGate(t *testing.T) {
	g, err := New(sessionFixture(72), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	s := g.NewReachSession()
	req := walking.ReachRequest{SnapshotID: g.descriptor.SnapshotID, From: walking.Endpoint{NodeID: "n03"}, Options: walking.Options{Role: walking.Closure, MaxSeconds: 8}}
	compareSession(t, g, s, req, nil)
	copy := *s
	for _, origin := range []string{"n10", "n03", "n14", "n01"} {
		req.From.NodeID = origin
		for _, cap := range []int64{0, 8} {
			req.Options.MaxSeconds = cap
			compareSession(t, g, &copy, req, nil)
			compareSession(t, g, s, req, nil)
		}
	}
}

func TestReachSessionUninitializedFailsPromptly(t *testing.T) {
	var nilGraph *Graph
	for _, tc := range []struct {
		name    string
		session *ReachSession
	}{
		{"zero_value", &ReachSession{}},
		{"nil_receiver", nil},
		{"nil_graph", nilGraph.NewReachSession()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, err := tc.session.ReachWithTargets(context.Background(), walking.ReachRequest{}, nil)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("uninitialized session accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("uninitialized session blocked on nil gate")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := tc.session.ReachWithTargets(ctx, walking.ReachRequest{}, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation did not take precedence: %v", err)
			}
		})
	}
}
