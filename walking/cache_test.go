package walking_test

import (
	"context"
	"errors"
	"github.com/ertyoii/transit-routing-go/walking"
	"testing"
)

type provider struct {
	version string
	calls   int
	fail    bool
}

func (p *provider) Descriptor() walking.Descriptor { return walking.Descriptor{SnapshotID: p.version} }
func (p *provider) Route(_ context.Context, r walking.Request) (walking.Result, error) {
	p.calls++
	if p.fail {
		return walking.Result{}, walking.ErrBudget
	}
	return walking.Result{SnapshotID: p.version, From: r.From.NodeID, To: r.To.NodeID, Status: walking.Reachable, Path: &walking.Path{SnapshotID: p.version, From: r.From.NodeID, To: r.To.NodeID, Seconds: 1, Segments: []walking.Segment{{ID: "ab", From: "a", To: "b", Seconds: 1, Accessible: walking.Yes, Witness: walking.Witness{Source: "test", Revision: p.version, EdgeIDs: []string{"ab"}}}}}}, nil
}
func TestCachePolicyIdentityOwnershipAndInvalidation(t *testing.T) {
	p := &provider{version: "1"}
	c, e := walking.NewCache(p, 1)
	if e != nil {
		t.Fatal(e)
	}
	r := walking.Request{SnapshotID: "1", From: walking.Endpoint{NodeID: "a"}, To: walking.Endpoint{NodeID: "b"}, Options: walking.Options{Role: walking.Access, MaxSeconds: 2}}
	v, e := c.Route(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	v.Path.Segments[0].Witness.EdgeIDs[0] = "changed"
	v, e = c.Route(context.Background(), r)
	if e != nil || p.calls != 1 || v.Path.Segments[0].Witness.EdgeIDs[0] != "ab" {
		t.Fatal(v, e, p.calls)
	}
	r.Options.Role = walking.Egress
	if _, e = c.Route(context.Background(), r); e != nil || p.calls != 2 {
		t.Fatal(e, p.calls)
	}
	r.Options.Role = walking.Access
	p.fail = true
	for i := 0; i < 2; i++ {
		if _, e = c.Route(context.Background(), r); !errors.Is(e, walking.ErrBudget) {
			t.Fatal(e)
		}
	}
	if p.calls != 4 {
		t.Fatal("error cached")
	}
	p.version = "2"
	if _, e = c.Route(context.Background(), r); !errors.Is(e, walking.ErrStale) {
		t.Fatal(e)
	}
	if p.calls != 4 {
		t.Fatal("stale request reached provider")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.Route(ctx, r); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
