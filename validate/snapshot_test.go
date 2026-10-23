package validate

import (
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
)

func checkerSnapshotFixture() model.Dataset {
	return model.Dataset{SchemaVersion: model.SchemaVersion, ID: "checker-snapshot", TimeZone: "Etc/UTC", ServiceDate: "2026-10-20",
		Stops:       []model.Stop{{ID: "a", Accessible: model.Yes}, {ID: "b", Accessible: model.Yes}, {ID: "c", Accessible: model.Yes}},
		Trips:       []model.Trip{{ID: "one", SourceID: "source-one", RouteID: "r1", Accessible: model.Yes}, {ID: "two", SourceID: "source-two", RouteID: "r2", Accessible: model.Yes}},
		Connections: []model.Connection{{ID: "one", TripID: "one", From: "a", To: "b", Departure: 0, Arrival: 10, Pickup: true, Dropoff: true}, {ID: "two", TripID: "two", From: "b", To: "c", Departure: 30, Arrival: 40, Pickup: true, Dropoff: true}},
		Transfers:   []model.TransferRule{{ID: "generic", FromStop: "b", ToStop: "b", Type: 3}, {ID: "specific", FromStop: "b", ToStop: "b", FromTrip: "source-one", ToTrip: "source-two", Type: 2, MinSeconds: 15}},
	}
}
func TestSnapshotAccessorsReturnCopiesAndStableFingerprint(t *testing.T) {
	d := checkerSnapshotFixture()
	expected := model.DatasetFingerprint(d)
	c, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	d.Connections[0].Arrival = 99
	d.Trips[0].RouteID = "changed"
	d.Transfers[1].Type = 3
	if c.DatasetFingerprint() != expected {
		t.Fatal("checker fingerprint changed with caller dataset")
	}
	first, ok := c.Connection("one")
	if !ok || first.Arrival != 10 {
		t.Fatal("connection snapshot aliased caller")
	}
	first.Arrival = 99
	again, _ := c.Connection("one")
	if again.Arrival != 10 {
		t.Fatal("connection accessor exposed mutable snapshot")
	}
	trip, ok := c.Trip("one")
	if !ok || trip.RouteID != "r1" {
		t.Fatal("trip snapshot aliased caller")
	}
	trip.RouteID = "changed"
	againTrip, _ := c.Trip("one")
	if againTrip.RouteID != "r1" {
		t.Fatal("trip accessor exposed mutable snapshot")
	}
	if _, ok = c.Connection("missing"); ok {
		t.Fatal("unknown connection exists")
	}
	if _, ok = c.Trip("missing"); ok {
		t.Fatal("unknown trip exists")
	}
	second, _ := c.Connection("two")
	allowed, minimum, err := c.TransferDecision(again, second, 99)
	if err != nil || !allowed || minimum != 15 {
		t.Fatalf("independent specificity/snapshot changed: %v %d %v", allowed, minimum, err)
	}
	q := model.Query{ID: "q", Origin: "a", Destination: "c", Departure: 0, LatestArrival: 100, TransferBuffer: 99, MaxBoardings: 2, MaxLegs: 2}
	j := model.Journey{Legs: []model.Leg{{Kind: "ride", Ref: "one", Departure: 0, Arrival: 10}, {Kind: "ride", Ref: "two", Departure: 30, Arrival: 40}}}
	if _, err = c.Check(q, j); err != nil {
		t.Fatalf("metadata accessor disagrees with independent journey checker: %v", err)
	}
}
func TestTransferDecisionFallbackAndProhibitionRemainMetadata(t *testing.T) {
	d := checkerSnapshotFixture()
	d.Transfers = nil
	c, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := c.Connection("one")
	b, _ := c.Connection("two")
	allowed, minimum, err := c.TransferDecision(a, b, 7)
	if err != nil || !allowed || minimum != 7 {
		t.Fatalf("fallback changed: %v %d %v", allowed, minimum, err)
	}
	d.Transfers = []model.TransferRule{{ID: "blocked", FromStop: "b", ToStop: "b", Type: 3}}
	c, err = New(d)
	if err != nil {
		t.Fatal(err)
	}
	allowed, minimum, err = c.TransferDecision(a, b, 7)
	if err != nil || allowed || minimum != 0 {
		t.Fatalf("prohibition changed: %v %d %v", allowed, minimum, err)
	}
	// Transfer metadata never authenticates a whole journey or reported times.
	q := model.Query{ID: "q", Origin: "a", Destination: "c", LatestArrival: 100, MaxBoardings: 2, MaxLegs: 2}
	j := model.Journey{Legs: []model.Leg{{Kind: "ride", Ref: "one", Departure: 0, Arrival: 10}, {Kind: "ride", Ref: "two", Departure: 30, Arrival: 40}}}
	if _, err = c.Check(q, j); err == nil {
		t.Fatal("prohibited transfer passed independent witness validation")
	}
}
