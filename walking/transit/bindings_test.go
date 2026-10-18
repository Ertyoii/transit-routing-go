package transit_test

import (
	"context"
	"errors"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/raptor"
	"github.com/ertyoii/transit-routing-go/validate"
	"github.com/ertyoii/transit-routing-go/walking"
	"github.com/ertyoii/transit-routing-go/walking/graph"
	"github.com/ertyoii/transit-routing-go/walking/transit"
	"reflect"
	"testing"
)

func collisionDataset() model.Dataset {
	return model.Dataset{SchemaVersion: model.SchemaVersion, ID: "namespace-collision", TimeZone: "Asia/Singapore", ServiceDate: "2026-10-19",
		Stops:       []model.Stop{{ID: "origin", Accessible: model.Yes}, {ID: "destination", Accessible: model.Yes}, {ID: "platform", Accessible: model.Yes}},
		Trips:       []model.Trip{{ID: "vehicle", Accessible: model.Yes}},
		Connections: []model.Connection{{ID: "ride", TripID: "vehicle", From: "platform", To: "destination", Departure: 5, Arrival: 6, Pickup: true, Dropoff: true}}}
}

func acquired(t *testing.T, provider, version, interior string, first, second int64) (transit.Catalog, *graph.Graph) {
	t.Helper()
	segment := func(id, from, to string, seconds int64) walking.Segment {
		return walking.Segment{ID: id, From: from, To: to, Seconds: seconds, Accessible: walking.Yes,
			Witness: walking.Witness{Source: provider, Revision: version, EdgeIDs: []string{id}}}
	}
	g, err := graph.New(graph.Data{
		Descriptor: walking.Descriptor{ProviderID: provider, GraphID: "streets", Version: version, CostModel: "seconds", RoleIndependent: true},
		Nodes:      []graph.Node{{ID: "external-start", Accessible: walking.Yes}, {ID: interior, Accessible: walking.Yes}, {ID: "external-end", Accessible: walking.Yes}},
		Edges:      []walking.Segment{segment("first", "external-start", interior, first), segment("second", interior, "external-end", second)},
	}, graph.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	r := walking.Request{SnapshotID: g.Descriptor().SnapshotID, From: walking.Endpoint{NodeID: "external-start"}, To: walking.Endpoint{NodeID: "external-end"},
		Options: walking.Options{Role: walking.Transfer, MaxSeconds: first + second}}
	c, err := transit.Acquire(context.Background(), g, []walking.Request{r}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return c, g
}

func anchors() []transit.NodeBinding {
	return []transit.NodeBinding{{ProviderNodeID: "external-start", TransitStopID: "origin"}, {ProviderNodeID: "external-end", TransitStopID: "destination"}}
}

func bestArrival(t *testing.T, d model.Dataset) int64 {
	t.Helper()
	q := model.Query{ID: "collision", Origin: "origin", Destination: "destination", Departure: 0, LatestArrival: 40,
		MaxBoardings: 1, MaxLegs: 20, MaxWalkSeconds: 40, Walking: &model.WalkingLimits{Access: 20, Egress: 20, Transfer: 20, Direct: 20}}
	x, err := raptor.New(d)
	if err != nil {
		t.Fatal(err)
	}
	r, err := x.Search(context.Background(), q)
	if err != nil || !r.Complete {
		t.Fatal(err, r)
	}
	v, err := validate.New(d)
	if err != nil {
		t.Fatal(err)
	}
	best := int64(1 << 60)
	for _, j := range r.Journeys {
		actual, err := v.Check(q, j)
		if err != nil {
			t.Fatal(err)
		}
		best = min(best, actual.Metrics.Arrival)
	}
	return best
}

func TestMaterializedInteriorNamesCannotInventBoarding(t *testing.T) {
	for _, interior := range []string{"street:17", "platform"} {
		t.Run(interior, func(t *testing.T) {
			d := collisionDataset()
			before := model.DatasetFingerprint(d)
			c, _ := acquired(t, "provider", "1", interior, 1, 19)
			b, err := c.Bind(d, anchors())
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.Materialize(d, b)
			if err != nil {
				t.Fatal(err)
			}
			if got := bestArrival(t, out.Dataset); got != 20 {
				t.Fatalf("unbound spelling invented boarding: %d", got)
			}
			if len(out.Dataset.Stops) != 4 || model.DatasetFingerprint(d) != before {
				t.Fatal("source mutated or interior lost")
			}
			for _, node := range out.Mapping.Nodes {
				if node.ProviderNodeID == interior && (node.Explicit || node.DatasetStopID == interior || node.DatasetStopID == "platform") {
					t.Fatal("unbound node reused transit namespace", node)
				}
			}
			// An explicitly supplied physical anchor may create the boarding, and
			// must change the identity/provenance of the materialized graph.
			bound, err := c.Bind(d, append(anchors(), transit.NodeBinding{ProviderNodeID: interior, TransitStopID: "platform"}))
			if err != nil {
				t.Fatal(err)
			}
			joined, err := c.Apply(d, bound)
			if err != nil {
				t.Fatal(err)
			}
			if bestArrival(t, joined) != 6 || joined.ID == out.Dataset.ID || joined.Footpaths[0].ID == out.Dataset.Footpaths[0].ID {
				t.Fatal("explicit anchor not applied or graph identity reused")
			}
		})
	}
}

func TestLayeredProvidersAndVersionsNeverShareUnboundInteriors(t *testing.T) {
	for _, tc := range []struct{ provider, version string }{{"other-provider", "1"}, {"provider", "2"}, {"provider", "1"}} {
		t.Run(tc.provider+tc.version, func(t *testing.T) {
			d := collisionDataset()
			a, _ := acquired(t, "provider", "1", "private", 1, 19)
			ab, err := a.Bind(d, anchors())
			if err != nil {
				t.Fatal(err)
			}
			first, err := a.Materialize(d, ab)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := acquired(t, tc.provider, tc.version, "private", 19, 1)
			if _, err := b.Apply(d, ab); !errors.Is(err, walking.ErrStale) {
				t.Fatal("cross-provider/snapshot binding accepted", err)
			}
			bb, err := b.Bind(first.Dataset, anchors())
			if err != nil {
				t.Fatal(err)
			}
			combined, err := b.MaterializeLayer(first.Dataset, bb, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(combined.Dataset.Stops) != 5 || len(combined.Dataset.Footpaths) != 4 || bestArrival(t, combined.Dataset) != 20 {
				t.Fatal("implicit cross-layer shortcut")
			}
			for _, old := range first.Mapping.Nodes {
				for _, fresh := range combined.Mapping.Nodes {
					if !old.Explicit && !fresh.Explicit && old.DatasetStopID == fresh.DatasetStopID {
						t.Fatal("unbound cross-layer alias")
					}
				}
			}
		})
	}
}

func TestMaterializationMappingPreservesProviderWitnesses(t *testing.T) {
	d := collisionDataset()
	c, g := acquired(t, "provider", "1", "platform", 1, 19)
	original := walking.CloneResult(c.Results[0])
	b, err := c.Bind(d, anchors())
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Materialize(d, b)
	if err != nil {
		t.Fatal(err)
	}
	nodes, edges := map[string]string{}, map[string]model.Footpath{}
	for _, n := range out.Mapping.Nodes {
		nodes[n.ProviderNodeID] = n.DatasetStopID
	}
	for _, edge := range out.Dataset.Footpaths {
		edges[edge.ID] = edge
	}
	segments := map[string]walking.Segment{}
	for _, s := range original.Path.Segments {
		segments[s.ID] = s
	}
	if out.Mapping.CatalogSHA256 != c.SHA256 || out.Mapping.BindingSHA256 != b.SHA256 {
		t.Fatal("mapping provenance lost")
	}
	for _, pair := range out.Mapping.Segments {
		s, edge := segments[pair.ProviderSegmentID], edges[pair.FootpathID]
		if edge.From != nodes[s.From] || edge.To != nodes[s.To] || edge.Duration != s.Seconds || edge.Accessible != model.Access(s.Accessible) {
			t.Fatal("normalized edge detached from source witness")
		}
	}
	if !reflect.DeepEqual(original, c.Results[0]) {
		t.Fatal("provider witness rewritten")
	}
	if err := g.ValidatePath(c.Requests[0], original); err != nil {
		t.Fatal(err)
	}
	for _, s := range out.Dataset.Stops {
		if s.ID == nodes["platform"] && s.Accessible != model.Unknown {
			t.Fatal("invented accessibility")
		}
	}
}

func TestNodeBindingsAreExplicitOwnedCanonicalAndSnapshotBound(t *testing.T) {
	d := collisionDataset()
	c, _ := acquired(t, "provider", "1", "platform", 1, 19)
	for _, nodes := range [][]transit.NodeBinding{nil, anchors()[:1], append(anchors(), anchors()[0]),
		append(anchors(), transit.NodeBinding{ProviderNodeID: "platform", TransitStopID: "origin"}),
		append(anchors(), transit.NodeBinding{ProviderNodeID: "not-observed", TransitStopID: "platform"}),
		{{ProviderNodeID: "external-start", TransitStopID: "absent"}, anchors()[1]}} {
		if _, err := c.Bind(d, nodes); !errors.Is(err, walking.ErrInvalid) {
			t.Fatal("invalid anchor map accepted", nodes, err)
		}
	}
	nodes := anchors()
	b, err := c.Bind(d, nodes)
	if err != nil {
		t.Fatal(err)
	}
	nodes[0].TransitStopID = "platform"
	if _, err := c.Apply(d, b); err != nil {
		t.Fatal("caller alias changed bindings", err)
	}
	reversed := anchors()
	reversed[0], reversed[1] = reversed[1], reversed[0]
	b2, err := c.Bind(d, reversed)
	if err != nil || b2.SHA256 != b.SHA256 {
		t.Fatal("anchor ordering changed identity", err)
	}
	if _, err := c.Apply(d, transit.Bindings{}); !errors.Is(err, walking.ErrInvalid) {
		t.Fatal("implicit endpoint binding", err)
	}
	changed := d.Clone()
	changed.Connections[0].Departure++
	if _, err := c.Apply(changed, b); !errors.Is(err, walking.ErrStale) {
		t.Fatal("stale base dataset accepted", err)
	}
	b.Nodes[0].TransitStopID = "platform"
	if _, err := c.Apply(d, b); !errors.Is(err, walking.ErrInvalid) {
		t.Fatal("tampered binding accepted", err)
	}
}

func TestEmptyMatrixAxesAreInvalid(t *testing.T) {
	_, g := acquired(t, "provider", "1", "interior", 1, 19)
	for _, emptySource := range []bool{true, false} {
		r := walking.MatrixRequest{SnapshotID: g.Descriptor().SnapshotID,
			Sources: []walking.Endpoint{{NodeID: "external-start"}}, Targets: []walking.Endpoint{{NodeID: "external-end"}},
			Options: walking.Options{Role: walking.Direct, MaxSeconds: 20}}
		if emptySource {
			r.Sources = nil
		} else {
			r.Targets = nil
		}
		if result, err := g.Matrix(context.Background(), r); !errors.Is(err, walking.ErrInvalid) || result.Rows != nil {
			t.Fatal("empty axis did not fail atomically", result, err)
		}
	}
}

func FuzzProviderInteriorNamespace(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(1))
	f.Add(uint8(255))
	f.Fuzz(func(t *testing.T, seed uint8) {
		first := int64(1 + seed%4)
		for _, interior := range []string{"platform", "street-node"} {
			c, _ := acquired(t, "provider", "1", interior, first, 20-first)
			d := collisionDataset()
			b, err := c.Bind(d, anchors())
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.Apply(d, b)
			if err != nil {
				t.Fatal(err)
			}
			if bestArrival(t, out) != 20 {
				t.Fatal("interior spelling invented transit access")
			}
		}
	})
}
