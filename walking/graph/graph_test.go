package graph_test

import (
	"context"
	"errors"
	"github.com/ertyoii/transit-routing-go/walking"
	"github.com/ertyoii/transit-routing-go/walking/graph"
	"reflect"
	"sync"
	"testing"
)

func data() graph.Data {
	d := graph.Data{Descriptor: walking.Descriptor{ProviderID: "fixture", GraphID: "directed", Version: "1", CostModel: "seconds", RoleIndependent: true}}
	for _, id := range []string{"a", "b", "c", "d", "z"} {
		d.Nodes = append(d.Nodes, graph.Node{ID: id, Accessible: walking.Yes})
	}
	d.Nodes[0].Coordinate = &walking.Coordinate{Latitude: 1, Longitude: 103}
	for _, e := range []struct {
		id, from, to string
		seconds      int64
	}{{"ab", "a", "b", 2}, {"ac", "a", "c", 2}, {"bd", "b", "d", 3}, {"cd", "c", "d", 3}, {"dz", "d", "z", 1}} {
		d.Edges = append(d.Edges, walking.Segment{ID: e.id, From: e.from, To: e.to, Seconds: e.seconds, Accessible: walking.Yes, Witness: walking.Witness{Source: "literal", Revision: "1", EdgeIDs: []string{e.id}}})
	}
	return d
}
func graphFor(t *testing.T, d graph.Data) *graph.Graph {
	t.Helper()
	g, e := graph.New(d, graph.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	return g
}
func request(g *graph.Graph, a, b string) walking.Request {
	return walking.Request{SnapshotID: g.Descriptor().SnapshotID, From: walking.Endpoint{NodeID: a}, To: walking.Endpoint{NodeID: b}, Options: walking.Options{Role: walking.Transfer, MaxSeconds: 6}}
}
func TestDirectedLimitsWitnessesAndOwnership(t *testing.T) {
	d := data()
	g := graphFor(t, d)
	r := request(g, "a", "d")
	v, e := g.Route(context.Background(), r)
	if e != nil || v.Path == nil || v.Path.Seconds != 5 {
		t.Fatalf("%+v %v", v, e)
	}
	if got := []string{v.Path.Segments[0].ID, v.Path.Segments[1].ID}; !reflect.DeepEqual(got, []string{"ab", "bd"}) {
		t.Fatal(got)
	}
	if e = g.ValidatePath(r, v); e != nil {
		t.Fatal(e)
	}
	v.Path.Segments[0].Witness.EdgeIDs[0] = "tampered"
	if e = g.ValidatePath(r, v); !errors.Is(e, walking.ErrInvalid) {
		t.Fatal(e)
	}
	d.Edges[0].Witness.EdgeIDs[0] = "input-mutated"
	d.Nodes[0].Coordinate.Latitude = 5
	r.From.Coordinate = &walking.Coordinate{Latitude: 1, Longitude: 103}
	v, e = g.Route(context.Background(), r)
	if e != nil || v.Path.Segments[0].Witness.EdgeIDs[0] != "ab" {
		t.Fatal(v, e)
	}
	for _, pair := range [][2]string{{"d", "a"}, {"z", "b"}} {
		u, e := g.Route(context.Background(), request(g, pair[0], pair[1]))
		if e != nil || u.Status != walking.Unreachable || u.Path != nil {
			t.Fatal(u, e)
		}
	}
	r.Options.MaxSeconds = 4
	v, e = g.Route(context.Background(), r)
	if e != nil || v.Status != walking.Unreachable {
		t.Fatal(v, e)
	}
	r = request(g, "a", "a")
	r.Options.MaxSeconds = 0
	v, e = g.Route(context.Background(), r)
	if e != nil || v.Path == nil || v.Path.Seconds != 0 || len(v.Path.Segments) != 0 {
		t.Fatal(v, e)
	}
	r = request(g, "a", "z")
	r.Options.StopAt = "d"
	v, e = g.Route(context.Background(), r)
	if e != nil || v.Status != walking.Unreachable {
		t.Fatal(v, e)
	}
}
func TestInvalidStaleAccessibilityAndBudget(t *testing.T) {
	d := data()
	d.Nodes[1].Accessible = walking.Unknown
	d.Edges[3].Accessible = walking.No
	g := graphFor(t, d)
	r := request(g, "a", "d")
	r.Options.AccessibleOnly = true
	v, e := g.Route(context.Background(), r)
	if e != nil || v.Status != walking.Unreachable {
		t.Fatal(v, e)
	}
	for _, change := range []func(*walking.Request){func(r *walking.Request) { r.To.NodeID = "missing" }, func(r *walking.Request) { r.Options.Role = "invalid" }, func(r *walking.Request) { r.From.Coordinate = &walking.Coordinate{Latitude: 2, Longitude: 103} }, func(r *walking.Request) { r.Options.StopAt = "missing" }} {
		r = request(g, "a", "d")
		change(&r)
		if _, e = g.Route(context.Background(), r); !errors.Is(e, walking.ErrInvalid) {
			t.Fatal(e)
		}
	}
	r = request(g, "a", "d")
	r.SnapshotID = "stale"
	if _, e = g.Route(context.Background(), r); !errors.Is(e, walking.ErrStale) {
		t.Fatal(e)
	}
	tiny, e := graph.New(data(), graph.Limits{MaxSettled: 1, MaxEdgeVisits: 1, MaxMatrixCells: 1, MaxResultSegments: 1})
	if e != nil {
		t.Fatal(e)
	}
	v, e = tiny.Route(context.Background(), request(tiny, "a", "d"))
	if !errors.Is(e, walking.ErrBudget) || v.Path != nil || v.Status != "" {
		t.Fatal(v, e)
	}
}
func TestMatrixOrderingPermutationCancellationAndConcurrency(t *testing.T) {
	g := graphFor(t, data())
	ctx := context.Background()
	req := walking.MatrixRequest{SnapshotID: g.Descriptor().SnapshotID, Sources: []walking.Endpoint{{NodeID: "d"}, {NodeID: "a"}, {NodeID: "a"}}, Targets: []walking.Endpoint{{NodeID: "z"}, {NodeID: "a"}, {NodeID: "d"}}, Options: walking.Options{Role: walking.Direct, MaxSeconds: 6}}
	m, e := g.Matrix(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	for i, from := range req.Sources {
		for j, to := range req.Targets {
			r := walking.Request{SnapshotID: req.SnapshotID, From: from, To: to, Options: req.Options}
			want, e := g.Route(ctx, r)
			if e != nil || !reflect.DeepEqual(want, m.Rows[i][j]) {
				t.Fatalf("cell %d/%d", i, j)
			}
		}
	}
	d := data()
	for i, j := 0, len(d.Edges)-1; i < j; i, j = i+1, j-1 {
		d.Edges[i], d.Edges[j] = d.Edges[j], d.Edges[i]
	}
	h := graphFor(t, d)
	if h.Descriptor().SnapshotID != g.Descriptor().SnapshotID {
		t.Fatal("input order changes snapshot")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := g.Matrix(ctx, req)
			if e != nil || !reflect.DeepEqual(m, v) {
				t.Error("concurrent mismatch", e)
			}
		}()
	}
	wg.Wait()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	v, e := g.Matrix(canceled, req)
	if !errors.Is(e, context.Canceled) || v.Rows != nil {
		t.Fatal(v, e)
	}
	values, e := g.Batch(canceled, nil)
	if !errors.Is(e, context.Canceled) || values != nil {
		t.Fatal("empty canceled batch", values, e)
	}
}

func TestMatrixWitnessBudgetFailsAtomically(t *testing.T) {
	limits := graph.DefaultLimits()
	limits.MaxResultSegments = 3
	g, e := graph.New(data(), limits)
	if e != nil {
		t.Fatal(e)
	}
	r := request(g, "a", "d")
	values, e := g.Batch(context.Background(), []walking.Request{r, r})
	if !errors.Is(e, walking.ErrBudget) || values != nil {
		t.Fatal("partial matrix escaped", values, e)
	}
	single, e := g.Route(context.Background(), r)
	if e != nil || single.Path == nil {
		t.Fatal("single path incorrectly fails", e)
	}
}

func TestReachTargetsAvoidUnrequestedWitnessExplosion(t *testing.T) {
	limits := graph.DefaultLimits()
	limits.MaxResultSegments = 2
	g, e := graph.New(data(), limits)
	if e != nil {
		t.Fatal(e)
	}
	r := walking.ReachRequest{SnapshotID: g.Descriptor().SnapshotID, From: walking.Endpoint{NodeID: "a"}, Targets: []walking.Endpoint{{NodeID: "d"}, {NodeID: "d"}}, Options: walking.Options{Role: walking.Closure, MaxSeconds: 6}}
	v, e := g.Reach(context.Background(), r)
	if e != nil || len(v.Paths) != 1 || v.Paths[0].To != "d" {
		t.Fatal(v, e)
	}
	r.Targets = nil
	v, e = g.Reach(context.Background(), r)
	if !errors.Is(e, walking.ErrBudget) || v.Paths != nil {
		t.Fatal(v, e)
	}
}

func TestPreparedTargetsAreBoundAndComposeWithEndpoints(t *testing.T) {
	g := graphFor(t, data())
	ctx := context.Background()
	selection, e := g.PrepareTargets(ctx, []walking.Endpoint{{NodeID: "b"}})
	if e != nil {
		t.Fatal(e)
	}
	req := walking.ReachRequest{SnapshotID: g.Descriptor().SnapshotID, From: walking.Endpoint{NodeID: "a"}, Targets: []walking.Endpoint{{NodeID: "z"}}, Options: walking.Options{Role: walking.Closure, MaxSeconds: 6}}
	v, e := g.ReachWithTargets(ctx, req, selection)
	if e != nil || len(v.Paths) != 2 || v.Paths[0].To != "b" || v.Paths[1].To != "z" {
		t.Fatal(v, e)
	}
	changed := data()
	changed.Edges[0].Seconds++
	h := graphFor(t, changed)
	req.SnapshotID = h.Descriptor().SnapshotID
	if _, e = h.ReachWithTargets(ctx, req, selection); !errors.Is(e, walking.ErrStale) {
		t.Fatal(e)
	}
}
