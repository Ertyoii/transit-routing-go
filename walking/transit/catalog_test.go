package transit_test

import (
	"context"
	"errors"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/walking"
	"github.com/ertyoii/transit-routing-go/walking/graph"
	"github.com/ertyoii/transit-routing-go/walking/transit"
	"testing"
)

func dataset() model.Dataset {
	return model.Dataset{SchemaVersion: model.SchemaVersion, ID: "literal", ServiceDate: "2026-10-19", TimeZone: "Asia/Singapore", Stops: []model.Stop{{ID: "a", Accessible: model.Yes}, {ID: "b", Accessible: model.Yes}, {ID: "c", Accessible: model.Yes}}, Footpaths: []model.Footpath{{ID: "ab", From: "a", To: "b", Duration: 2, Accessible: model.Yes}, {ID: "bc", From: "b", To: "c", Duration: 3, Accessible: model.Yes}}}
}
func TestAcquiredCatalogRetainsInteriorNodesAndWitnesses(t *testing.T) {
	d := dataset()
	g, e := transit.FromDataset(d, graph.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	r := walking.Request{SnapshotID: g.Descriptor().SnapshotID, From: walking.Endpoint{NodeID: "a"}, To: walking.Endpoint{NodeID: "c"}, Options: walking.Options{Role: walking.Access, MaxSeconds: 5}}
	c, e := transit.Acquire(context.Background(), g, []walking.Request{r}, 1)
	if e != nil {
		t.Fatal(e)
	}
	bindings, e := c.Bind(d, []transit.NodeBinding{{"a", "a"}, {"b", "b"}, {"c", "c"}})
	if e != nil {
		t.Fatal(e)
	}
	next, e := c.Apply(d, bindings)
	if e != nil {
		t.Fatal(e)
	}
	if next.ID == d.ID || len(next.Footpaths) != 2 || len(c.Results[0].Path.Segments) != 2 {
		t.Fatal("lost source segments")
	}
	h, e := transit.FromDataset(next, graph.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	r.SnapshotID = h.Descriptor().SnapshotID
	r.Options.StopAt = "b"
	v, e := h.Route(context.Background(), r)
	if e != nil || v.Status != walking.Unreachable {
		t.Fatal("hidden destination traversed", v, e)
	}
	c.Results[0].Path.Seconds++
	if _, e = c.Apply(d, bindings); !errors.Is(e, walking.ErrInvalid) {
		t.Fatal("changed acquisition accepted", e)
	}
	r.SnapshotID = g.Descriptor().SnapshotID
	r.Options.StopAt = ""
	r.Options.AccessibleOnly = true
	if _, e = transit.Acquire(context.Background(), g, []walking.Request{r}, 1); !errors.Is(e, walking.ErrUnsupported) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = transit.Acquire(ctx, g, []walking.Request{r}, 1); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestReplaceAccessLayerPreservesInterchangeAndTransferRule(t *testing.T) {
	d := dataset()
	d.Footpaths[0].ID = "access:ab"
	d.Footpaths[1].ID = "interchange:bc"
	d.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "b", ToStop: "c", Type: 2, MinSeconds: 3}}
	replacement := dataset()
	replacement.Footpaths = replacement.Footpaths[:1]
	replacement.Footpaths[0].Duration = 4
	g, e := transit.FromDataset(replacement, graph.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	req := walking.Request{SnapshotID: g.Descriptor().SnapshotID, From: walking.Endpoint{NodeID: "a"}, To: walking.Endpoint{NodeID: "b"}, Options: walking.Options{Role: walking.Access, MaxSeconds: 4}}
	catalog, e := transit.Acquire(context.Background(), g, []walking.Request{req}, 1)
	if e != nil {
		t.Fatal(e)
	}
	bindings, e := catalog.Bind(d, []transit.NodeBinding{{"a", "a"}, {"b", "b"}})
	if e != nil {
		t.Fatal(e)
	}
	changed, e := catalog.ApplyLayer(d, bindings, []string{"access:ab"})
	if e != nil {
		t.Fatal(e)
	}
	if len(changed.Footpaths) != 2 || len(changed.Transfers) != 1 || changed.Transfers[0] != d.Transfers[0] {
		t.Fatal("lost retained layer/rule")
	}
	found := false
	for _, edge := range changed.Footpaths {
		if edge.ID == "access:ab" {
			t.Fatal("old layer survived")
		}
		if edge.ID == "interchange:bc" {
			found = edge == d.Footpaths[1]
		}
	}
	if !found || d.Footpaths[0].Duration != 2 {
		t.Fatal("retained/source data changed")
	}
	if _, e = catalog.ApplyLayer(d, bindings, []string{"missing"}); !errors.Is(e, walking.ErrInvalid) {
		t.Fatal(e)
	}
	if _, e = catalog.ApplyLayer(d, bindings, []string{"access:ab", "access:ab"}); !errors.Is(e, walking.ErrInvalid) {
		t.Fatal(e)
	}
}
