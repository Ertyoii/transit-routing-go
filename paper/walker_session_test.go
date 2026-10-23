package paper

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
)

func TestWalkerSessionKeepsLexicalWitnessesAndQueryIsolation(t *testing.T) {
	d := model.Dataset{SchemaVersion: model.SchemaVersion, ID: "walker-session", TimeZone: "UTC", ServiceDate: "2026-10-23"}
	for _, id := range []string{"source", "n2", "target", "after", "n10"} {
		d.Stops = append(d.Stops, model.Stop{ID: id, Accessible: model.Yes})
	}
	d.Trips = []model.Trip{{ID: "t", Accessible: model.Yes}, {ID: "u", Accessible: model.Yes}}
	d.Connections = []model.Connection{
		{ID: "ride", TripID: "t", From: "n10", To: "n2", Departure: 10, Arrival: 11},
		{ID: "ride-after", TripID: "u", From: "target", To: "after", Departure: 10, Arrival: 11},
	}
	for _, e := range []struct{ id, from, to string }{
		{"a-source-n2", "source", "n2"},
		{"c-source-n10", "source", "n10"},
		{"b-source-n10", "source", "n10"},
		{"d-n2-target", "n2", "target"},
		{"e-n10-target", "n10", "target"},
		{"f-target-after", "target", "after"},
	} {
		d.Footpaths = append(d.Footpaths, model.Footpath{ID: e.id, From: e.from, To: e.to, Duration: 1, Accessible: model.Yes})
	}
	p, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	q := model.Query{Origin: "source", Destination: "target", Walking: &model.WalkingLimits{Direct: 3}}
	w := p.Walker(q)
	if w.reach == nil || w.snapshot != p.walkingGraph.Descriptor().SnapshotID {
		t.Fatal("walker has no snapshot-bound reach session")
	}
	// Walker owns the query's limits, and the destination remains absorbing even
	// though the cap would otherwise allow reaching the transit stop after it.
	q.Walking.Direct = 0
	got, err := w.Paths(context.Background(), "source")
	want := []Path{
		{To: "n10", Duration: 1, Edges: []model.Footpath{d.Footpaths[2]}},
		{To: "n2", Duration: 1, Edges: []model.Footpath{d.Footpaths[0]}},
		{To: "source", Edges: []model.Footpath{}},
		{To: "target", Duration: 2, Edges: []model.Footpath{d.Footpaths[2], d.Footpaths[4]}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("witness/order/absorption changed: %+v %v", got, err)
	}
	if _, err = w.Paths(context.Background(), "n10"); err != nil {
		t.Fatal(err)
	}
	again, err := w.Paths(context.Background(), "source")
	if err != nil || !reflect.DeepEqual(again, want) || w.Count != 2 {
		t.Fatalf("scratch reuse changed cached paths: %+v %v closures=%d", again, err, w.Count)
	}
	other := p.Walker(q)
	if other.reach == w.reach {
		t.Fatal("independent walkers share mutable sessions")
	}
	short, err := other.Paths(context.Background(), "source")
	if err != nil || len(short) != 1 || short[0].To != "source" {
		t.Fatalf("another query inherited distances: %+v %v", short, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if paths, err := w.Paths(ctx, "source"); !errors.Is(err, context.Canceled) || paths != nil {
		t.Fatalf("cache bypassed cancellation: %+v %v", paths, err)
	}
}
