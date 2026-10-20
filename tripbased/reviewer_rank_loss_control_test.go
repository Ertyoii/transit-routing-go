package tripbased

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ertyoii/transit-routing-go/internal/capverify"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

func TestReviewRoleCapsDoNotPreserveCustomization(t *testing.T) {
	d := model.Dataset{
		SchemaVersion: "0.2", ID: "review-literal-rank-loss-901", TimeZone: "Asia/Singapore", ServiceDate: "2026-10-16",
		Window: &model.TimeWindow{Start: 0, End: 2000},
		Stops:  []model.Stop{{ID: "S", Accessible: model.Yes}, {ID: "B", Accessible: model.Yes}, {ID: "C", Accessible: model.Yes}, {ID: "T", Accessible: model.Yes}},
		Trips:  []model.Trip{{ID: "t0", Accessible: model.Yes}, {ID: "t1", Accessible: model.Yes}, {ID: "t2", Accessible: model.Yes}},
		Connections: []model.Connection{
			{ID: "sb", TripID: "t0", Sequence: 0, From: "S", To: "B", Departure: 10, Arrival: 20, Pickup: true, Dropoff: true},
			{ID: "bc", TripID: "t1", Sequence: 0, From: "B", To: "C", Departure: 30, Arrival: 40, Pickup: true, Dropoff: true},
			{ID: "ct", TripID: "t2", Sequence: 0, From: "C", To: "T", Departure: 1000, Arrival: 1100, Pickup: true, Dropoff: true},
		},
		Footpaths: []model.Footpath{{ID: "wbc", From: "B", To: "C", Duration: 901, Accessible: model.Yes}},
	}
	q := model.Query{ID: "literal-transfer900", Origin: "S", Destination: "T", Departure: 0, LatestArrival: 2000,
		MaxBoardings: 3, MaxLegs: 1024, MaxWalkSeconds: 1800, Walking: &model.WalkingLimits{Transfer: 900}}
	cfg := TREXConfig{ExactStorage: true, Levels: 1, MaxBoardings: 3, Partition: map[string]int{"S": 0, "T": 0, "B": 1, "C": 1},
		Limits: TREXLimits{MaxStops: 100, MaxTrips: 100, MaxInputEdges: 100, MaxClosurePaths: 1000, MaxWalkSettled: 1000,
			MaxTransfers: 1000, MaxRuns: 1000, MaxWork: 100000, MaxWitnesses: 1000, MaxOverlayEntries: 10000,
			MaxQueuedSegments: 1000, MaxQueryWork: 2000000}}
	limits := TREXStaticCapVerifyLimits{MaxScans: 4000000, MaxStates: 50000, MaxJourneys: 50000}
	expected := model.Journey{Legs: []model.Leg{{Kind: "ride", Ref: "sb", Departure: 10, Arrival: 20}, {Kind: "ride", Ref: "bc", Departure: 30, Arrival: 40}, {Kind: "ride", Ref: "ct", Departure: 1000, Arrival: 1100}}}
	bad := model.Journey{Legs: []model.Leg{{Kind: "ride", Ref: "sb", Departure: 10, Arrival: 20}, {Kind: "walk", Ref: "wbc", Departure: 20, Arrival: 921}, {Kind: "ride", Ref: "ct", Departure: 1000, Arrival: 1100}}}
	ctx := context.Background()
	checker, err := validate.New(d)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := checker.Check(q, expected)
	if err != nil || valid.Metrics.Arrival != 1100 || valid.Metrics.Boardings != 3 {
		t.Fatal("literal feasible capped word", valid, err)
	}
	if _, err = checker.Check(q, bad); err == nil || !strings.Contains(err.Error(), "walking block") {
		t.Fatal("901 transfer should be infeasible", err)
	}
	reference, err := capverify.Enumerate(ctx, d, q, capverify.Limits{MaxScans: limits.MaxScans, MaxStates: limits.MaxStates, MaxJourneys: limits.MaxJourneys})
	if err != nil || !reference.Complete || !reflect.DeepEqual(reference.Journeys, []model.Journey{expected}) {
		t.Fatal("literal reference words", reference, err)
	}
	x, err := BuildTREXStaticRulesCandidate(ctx, d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := x.Snapshot()
	ranks := map[string]int{}
	for _, edge := range snapshot.Transfers {
		ranks[edge.FromTrip+"->"+edge.ToTrip] = edge.Rank
	}
	wantRanks := map[string]int{"t0->t1": 0, "t0->t2": 1, "t1->t2": 0}
	if !reflect.DeepEqual(ranks, wantRanks) {
		t.Fatal("wrong independent rank prediction", ranks)
	}
	if len(snapshot.Witnesses) != 1 || !reflect.DeepEqual(snapshot.Witnesses[0].Journey, bad) {
		t.Fatal("customization retained a different rank witness", snapshot.Witnesses)
	}
	gate, err := PrepareTREXRoleCapCandidate(ctx, x, limits)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(q)
	t.Run("unrestricted-customization-misses-feasible-capped-objective", func(t *testing.T) {
		core, err := x.searchTREXForCertificate(ctx, q, "overlay-role-caps", true)
		if err != nil || !core.Complete || len(core.Journeys) != 0 {
			t.Fatal("ranked kernel should miss the literal feasible route", core, err)
		}
		result, err := gate.Search(ctx, q)
		if err == nil || result.DeclineReason != "frontier_mismatch" || !result.VerificationComplete || result.Complete || result.Certificate.Accepted || len(result.Journeys) != 0 || result.Certificate.RankedRepresentatives != 0 || result.Certificate.CheckedRepresentatives != 0 {
			t.Fatal("owning certificate must decline the ranked miss", result, err)
		}
		t.Logf("ranked=empty; literal capped=(1100,3), [sb,bc,ct]; ranks=%v; safe decline=%s; verifier states=%d scans=%d", ranks, result.DeclineReason, result.VerificationStates, result.VerificationScans)
	})
	t.Run("one-second-cap-boundary", func(t *testing.T) {
		control := q
		control.ID = "literal-transfer901-control"
		control.Walking = &model.WalkingLimits{Transfer: 901}
		control.MaxWalkSeconds = 1802 // separately declared nonbinding control, never mutate q
		result, err := gate.Search(ctx, control)
		if err != nil || !result.Complete || !result.Certificate.Accepted || !result.VerificationComplete || !reflect.DeepEqual(result.Journeys, []model.Journey{bad}) {
			t.Fatal("ranked901 control should certify its literal two-boarding word", result, err)
		}
	})
	t.Run("level-zero-overlay-control", func(t *testing.T) {
		control := cfg
		control.Levels = 0
		control.Partition = map[string]int{"S": 0, "T": 0, "B": 0, "C": 0}
		index, err := BuildTREXStaticRulesCandidate(ctx, d, control)
		if err != nil {
			t.Fatal(err)
		}
		certificate, err := PrepareTREXRoleCapCandidate(ctx, index, limits)
		if err != nil {
			t.Fatal(err)
		}
		result, err := certificate.Search(ctx, q)
		if err != nil || !result.Complete || !result.Certificate.Accepted || !result.VerificationComplete || !reflect.DeepEqual(result.Journeys, []model.Journey{expected}) {
			t.Fatal("level0 overlay control should return literal feasible capped route", result, err)
		}
	})
	after, _ := json.Marshal(q)
	if string(before) != string(after) {
		t.Fatal("original query mutated")
	}
}
