package tripbased

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ertyoii/transit-routing-go/internal/capverify"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

func roleCapCandidate(t *testing.T, fixture syntheticCapCase, config TREXConfig, limits TREXStaticCapVerifyLimits) *TREXRoleCapCandidate {
	t.Helper()
	index, err := BuildTREXStaticRulesCandidate(context.Background(), fixture.Dataset, config)
	if err != nil || !index.Complete() {
		t.Fatal("tiny construction incomplete", err)
	}
	candidate, err := PrepareTREXRoleCapCandidate(context.Background(), index, limits)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

// The five formerly hidden witnesses have independent literal route/objective
// expectations. The candidate must restore them without taking oracle journeys.
func TestTREXRoleCapCandidateLiteralRecovery(t *testing.T) {
	for _, fixture := range syntheticCapCases(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			candidate := roleCapCandidate(t, fixture, syntheticCapConfig(fixture.Levels), syntheticCapVerifyLimits())
			before, _ := json.Marshal(fixture.Query)
			result, err := candidate.Search(context.Background(), fixture.Query)
			if err != nil || !result.Complete || !result.Certificate.Accepted || !result.VerificationComplete || result.VerificationStates == 0 {
				t.Fatal("candidate did not certify literal capped frontier", result, err)
			}
			if result.Certificate.Method != TREXRoleCapCandidateMethod || result.Certificate.CheckedRepresentatives != len(result.Journeys) {
				t.Fatal("candidate identity or mandatory replay missing", result)
			}
			if got := syntheticCapRefs(result.Journeys); !reflect.DeepEqual(got, fixture.FeasibleRefs) {
				t.Fatal("literal capped witness mismatch", got, fixture.FeasibleRefs)
			}
			if got := syntheticCapFrontier(t, fixture.Dataset, fixture.Query, result.Journeys); !reflect.DeepEqual(got, fixture.FeasiblePoints) {
				t.Fatal("literal capped objective mismatch", got, fixture.FeasiblePoints)
			}
			checker, err := validate.New(fixture.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			for _, journey := range result.Journeys {
				if _, err := checker.Check(fixture.Query, journey); err != nil {
					t.Fatal("full original-query replay", err)
				}
			}
			after, _ := json.Marshal(fixture.Query)
			if string(before) != string(after) {
				t.Fatal("original query caps mutated")
			}
		})
	}
}

func TestTREXRoleCapCandidateMetamorphic(t *testing.T) {
	for _, original := range syntheticCapCases(t) {
		for _, transformation := range []string{"permutation", "translation"} {
			t.Run(original.Name+"/"+transformation, func(t *testing.T) {
				fixture := original
				fixture.Dataset = original.Dataset.Clone()
				fixture.FeasiblePoints = append([]syntheticCapPoint{}, original.FeasiblePoints...)
				if transformation == "permutation" {
					for i, j := 0, len(fixture.Dataset.Connections)-1; i < j; i, j = i+1, j-1 {
						fixture.Dataset.Connections[i], fixture.Dataset.Connections[j] = fixture.Dataset.Connections[j], fixture.Dataset.Connections[i]
					}
					for i, j := 0, len(fixture.Dataset.Stops)-1; i < j; i, j = i+1, j-1 {
						fixture.Dataset.Stops[i], fixture.Dataset.Stops[j] = fixture.Dataset.Stops[j], fixture.Dataset.Stops[i]
					}
				} else {
					const shift = int64(37)
					fixture.Query.Departure += shift
					fixture.Query.LatestArrival += shift
					fixture.Dataset.Window.Start += shift
					fixture.Dataset.Window.End += shift
					for i := range fixture.Dataset.Connections {
						fixture.Dataset.Connections[i].Departure += shift
						fixture.Dataset.Connections[i].Arrival += shift
					}
					for i := range fixture.FeasiblePoints {
						fixture.FeasiblePoints[i].Arrival += shift
					}
				}
				candidate := roleCapCandidate(t, fixture, syntheticCapConfig(fixture.Levels), syntheticCapVerifyLimits())
				result, err := candidate.Search(context.Background(), fixture.Query)
				if err != nil || !result.Certificate.Accepted || !result.VerificationComplete {
					t.Fatal("metamorphic case declined", result, err)
				}
				if got := syntheticCapFrontier(t, fixture.Dataset, fixture.Query, result.Journeys); !reflect.DeepEqual(got, fixture.FeasiblePoints) {
					t.Fatal("literal transformed frontier mismatch", got, fixture.FeasiblePoints)
				}
			})
		}
	}
}

// Existing sufficient resource guards remain intact even when a valid capped
// witness exists. Such unsupported queries must not be claimed complete-empty.
func TestTREXRoleCapCandidateResidualConstraintsDecline(t *testing.T) {
	fixture := syntheticCapCases(t)[0]
	for _, kind := range []string{"aggregate_walk", "raw_legs"} {
		t.Run(kind, func(t *testing.T) {
			q := fixture.Query
			w := *q.Walking
			q.Walking = &w
			q.Walking.Access = 1
			if kind == "aggregate_walk" {
				q.MaxWalkSeconds = 0
			} else {
				q.MaxLegs = 1
				q.MaxBoardings = 1
			}
			candidate := roleCapCandidate(t, fixture, syntheticCapConfig(fixture.Levels), syntheticCapVerifyLimits())
			result, err := candidate.Search(context.Background(), q)
			if err == nil || result.Complete || result.Certificate.Accepted || len(result.Journeys) != 0 || result.DeclineReason != "ranked_search_or_original_resource_guard" {
				t.Fatal("unsupported query escaped original resource guards", result, err)
			}
			verified, err := capverify.Enumerate(context.Background(), fixture.Dataset, q, capverify.Limits{MaxStates: 1000, MaxJourneys: 100, MaxScans: 10000})
			if err != nil || !verified.Complete || !reflect.DeepEqual(syntheticCapRefs(verified.Journeys), fixture.FeasibleRefs) {
				t.Fatal("decline was not a complete-empty answer; literal feasible route must remain", verified, err)
			}
		})
	}
}

func TestTREXRoleCapCandidateDestinationAbsorption(t *testing.T) {
	fixture := syntheticCapCases(t)[0]
	// This fixture has one indivisible walking component. It exercises the
	// replay boundary at level zero; recovery and frontier tests retain level one.
	fixture.Levels = 0
	fixture.Dataset = fixture.Dataset.Clone()
	fixture.Dataset.Footpaths = []model.Footpath{
		{ID: "enter-target", From: "Z", To: "T", Duration: 1, Accessible: model.Yes},
		{ID: "leave-target", From: "T", To: "A", Duration: 1, Accessible: model.Yes},
	}
	w := *fixture.Query.Walking
	fixture.Query.Walking = &w
	fixture.Query.Walking.Access = 2
	fixture.Query.MaxWalkSeconds = 902
	candidate := roleCapCandidate(t, fixture, syntheticCapConfig(fixture.Levels), syntheticCapVerifyLimits())
	result, err := candidate.Search(context.Background(), fixture.Query)
	if err == nil || result.Complete || result.Certificate.Accepted || len(result.Journeys) != 0 || result.DeclineReason != "ranked_candidate_infeasible" {
		t.Fatal("walking through absorbing target escaped replay", result, err)
	}
	verified, err := capverify.Enumerate(context.Background(), fixture.Dataset, fixture.Query, capverify.Limits{MaxStates: 1000, MaxJourneys: 100, MaxScans: 10000})
	if err != nil || !verified.Complete || !reflect.DeepEqual(syntheticCapRefs(verified.Journeys), fixture.FeasibleRefs) {
		t.Fatal("literal feasible ride must survive absorption", verified, err)
	}
}

func TestTREXRoleCapCandidateFrontierGate(t *testing.T) {
	fixture := syntheticCapCases(t)[4]
	candidate := roleCapCandidate(t, fixture, syntheticCapConfig(fixture.Levels), syntheticCapVerifyLimits())
	// Model an overaggressive transfer-pruning regression. The ranked kernel
	// completes with no route, but the unchanged independent verifier must find
	// the literal legal two-boarding journey and reject complete-empty output.
	for i := range candidate.gate.cap.index.overlays {
		overlay := &candidate.gate.cap.index.overlays[i]
		overlay.edges = nil
		for j := range overlay.starts {
			overlay.starts[j] = 0
		}
	}
	result, err := candidate.Search(context.Background(), fixture.Query)
	if err == nil || result.Complete || result.Certificate.Accepted || len(result.Journeys) != 0 || result.DeclineReason != "frontier_mismatch" || !result.VerificationComplete {
		t.Fatal("independent frontier gate did not reject missing ranked objective", result, err)
	}
}

func TestTREXRoleCapCandidateTransitRestrictions(t *testing.T) {
	for _, kind := range []string{"no_pickup", "no_dropoff", "forbidden_transfer", "minimum_transfer"} {
		t.Run(kind, func(t *testing.T) {
			fixture := syntheticCapCases(t)[5]
			if kind == "forbidden_transfer" || kind == "minimum_transfer" {
				fixture = syntheticCapCases(t)[4]
			}
			fixture.Dataset = fixture.Dataset.Clone()
			switch kind {
			case "no_pickup":
				fixture.Dataset.Connections[0].Pickup = false
			case "no_dropoff":
				fixture.Dataset.Connections[0].Dropoff = false
			case "forbidden_transfer":
				fixture.Dataset.Transfers = []model.TransferRule{{ID: "forbid", FromStop: "A", ToStop: "C", Type: 3}}
			case "minimum_transfer":
				fixture.Dataset.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "A", ToStop: "C", Type: 2, MinSeconds: 1001}}
			}
			candidate := roleCapCandidate(t, fixture, syntheticCapConfig(fixture.Levels), syntheticCapVerifyLimits())
			result, err := candidate.Search(context.Background(), fixture.Query)
			if err != nil || !result.Complete || !result.Certificate.Accepted || !result.VerificationComplete || len(result.Journeys) != 0 {
				t.Fatal("literal transit restriction did not yield independently certified empty", result, err)
			}
		})
	}
}

func TestTREXRoleCapCandidateBudgetDeclines(t *testing.T) {
	fixture := syntheticCapCases(t)[5]
	for _, kind := range []string{"ranked_work", "replay_work", "verifier_scans", "projection_scans", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			config, limits := syntheticCapConfig(fixture.Levels), syntheticCapVerifyLimits()
			ctx := context.Background()
			if kind == "ranked_work" {
				config.Limits.MaxQueryWork = 1
			} else if kind == "replay_work" {
				candidate := roleCapCandidate(t, fixture, config, limits)
				complete, err := candidate.Search(ctx, fixture.Query)
				if err != nil || complete.Certificate.ReplayWork == 0 {
					t.Fatal(complete, err)
				}
				config.Limits.MaxQueryWork = complete.Stats.Work - 1
			} else if kind == "verifier_scans" || kind == "projection_scans" {
				limits.MaxScans = 1
			}
			local := fixture
			local.Dataset = fixture.Dataset.Clone()
			if kind == "verifier_scans" {
				local.Dataset.Trips = append(local.Dataset.Trips, model.Trip{ID: "later", Accessible: model.Yes})
				local.Dataset.Connections = append(local.Dataset.Connections, model.Connection{ID: "later", TripID: "later", From: "Z", To: "T", Departure: 26300, Arrival: 26500, Pickup: true, Dropoff: true})
			}
			candidate := roleCapCandidate(t, local, config, limits)
			if kind == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := candidate.Search(ctx, local.Query)
			if err == nil || result.Complete || result.Certificate.Accepted || len(result.Journeys) != 0 {
				t.Fatal("resource decline returned a complete answer", result, err)
			}
			want := map[string]string{"ranked_work": "ranked_search_or_original_resource_guard", "replay_work": "replay_work_incomplete", "verifier_scans": "verification_incomplete", "projection_scans": "ranked_frontier_projection", "cancelled": "cancelled"}[kind]
			if result.DeclineReason != want {
				t.Fatal("wrong resource classification", result.DeclineReason, want, err)
			}
		})
	}
}
