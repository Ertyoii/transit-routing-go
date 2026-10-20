// Copyright (c) 2026 Ertyoii
// SPDX-License-Identifier: MIT

package tripbased

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ertyoii/transit-routing-go/internal/capverify"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

// These package-local helpers are test code only. Certification assertions below
// use public APIs. Only the separate kernel diagnostic test calls a private API.
type syntheticCapPoint struct {
	Arrival   int64 `json:"arrival"`
	Boardings int   `json:"boardings"`
}

type syntheticCapCase struct {
	Name           string              `json:"name"`
	Levels         int                 `json:"levels"`
	Dataset        model.Dataset       `json:"dataset"`
	Query          model.Query         `json:"query"`
	RawRefs        [][]string          `json:"expected_raw_refs"`
	RawPoints      []syntheticCapPoint `json:"expected_raw_points"`
	FeasibleRefs   [][]string          `json:"expected_feasible_refs"`
	FeasiblePoints []syntheticCapPoint `json:"expected_feasible_points"`
	Accepted       bool                `json:"expected_certificate_accepted"`
}

func syntheticCapCases(t *testing.T) []syntheticCapCase {
	t.Helper()
	b, err := os.ReadFile("testdata/trex_cap_certification/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Schema   string             `json:"schema"`
		License  string             `json:"license"`
		Fixtures []syntheticCapCase `json:"fixtures"`
	}
	if err := json.Unmarshal(b, &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != "trex-cap-synthetic-fixtures-v1" || document.License != "CC0-1.0" || len(document.Fixtures) != 6 {
		t.Fatal("unexpected synthetic fixture document")
	}
	return document.Fixtures
}

func syntheticCapConfig(levels int) TREXConfig {
	return TREXConfig{ExactStorage: true, Levels: levels, MaxBoardings: 4, Limits: TREXLimits{
		MaxStops: 10, MaxTrips: 10, MaxInputEdges: 20, MaxClosurePaths: 100,
		MaxWalkSettled: 100, MaxTransfers: 100, MaxRuns: 100, MaxWork: 100000,
		MaxWitnesses: 100, MaxOverlayEntries: 200, MaxQueuedSegments: 1000, MaxQueryWork: 10000,
	}}
}

func syntheticCapVerifyLimits() TREXStaticCapVerifyLimits {
	return TREXStaticCapVerifyLimits{MaxStates: 1000, MaxJourneys: 100, MaxScans: 10000}
}

func syntheticCapRefs(journeys []model.Journey) [][]string {
	refs := make([][]string, len(journeys))
	for i, journey := range journeys {
		for _, leg := range journey.Legs {
			refs[i] = append(refs[i], leg.Ref)
		}
	}
	return refs
}

func syntheticCapFrontier(t *testing.T, data model.Dataset, query model.Query, journeys []model.Journey) []syntheticCapPoint {
	t.Helper()
	points, _, err := capverify.Frontier(context.Background(), data, query, journeys, 10000)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]syntheticCapPoint, len(points))
	for i, point := range points {
		out[i] = syntheticCapPoint{point.Arrival, point.Boardings}
	}
	return out
}

// The positive control must finish mandatory independent verification. Every
// declined case must clear unchecked results and must not masquerade as no route.
// Literal fixtures also demonstrate that feasible capped routes still exist.
func TestTREXCapCertificationBoundary(t *testing.T) {
	ctx := context.Background()
	for _, fixture := range syntheticCapCases(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			queryBefore, err := json.Marshal(fixture.Query)
			if err != nil {
				t.Fatal(err)
			}
			index, err := BuildTREXStaticRulesCandidate(ctx, fixture.Dataset, syntheticCapConfig(fixture.Levels))
			if err != nil || !index.Complete() {
				t.Fatal("construction incomplete", err)
			}
			certificate, err := PrepareTREXStaticCapCertificate(ctx, index, syntheticCapVerifyLimits())
			if err != nil {
				t.Fatal(err)
			}
			result, searchErr := certificate.Search(ctx, fixture.Query)
			if fixture.Accepted {
				if searchErr != nil || !result.Complete || !result.Certificate.Accepted || !result.VerificationComplete || result.VerificationStates == 0 {
					t.Fatal("acceptance requires complete independent verification", result, searchErr)
				}
				if got := syntheticCapRefs(result.Journeys); !reflect.DeepEqual(got, fixture.FeasibleRefs) {
					t.Fatal("accepted result differs from literal expected witness", got)
				}
			} else {
				if searchErr == nil || result.Complete || result.Certificate.Accepted || len(result.Journeys) != 0 || result.DeclineReason != "unrestricted_representative_infeasible" || result.VerificationStates != 0 || result.VerificationScans != 0 {
					t.Fatal("unsafe or misclassified cap rejection", result, searchErr)
				}
			}
			queryAfter, err := json.Marshal(fixture.Query)
			if err != nil {
				t.Fatal(err)
			}
			if string(queryBefore) != string(queryAfter) {
				t.Fatal("original query mutated")
			}

			// Independent bounded enumeration shares no search/pruning code with
			// T-REX. Both route words and objective vectors have literal expectations.
			verified, err := capverify.Enumerate(ctx, fixture.Dataset, fixture.Query, capverify.Limits{MaxStates: 1000, MaxJourneys: 100, MaxScans: 10000})
			if err != nil || !verified.Complete {
				t.Fatal("enumeration inconclusive", err)
			}
			if got := syntheticCapRefs(verified.Journeys); !reflect.DeepEqual(got, fixture.FeasibleRefs) {
				t.Fatalf("feasible witnesses: got %v, want %v", got, fixture.FeasibleRefs)
			}
			if got := syntheticCapFrontier(t, fixture.Dataset, fixture.Query, verified.Journeys); !reflect.DeepEqual(got, fixture.FeasiblePoints) {
				t.Fatalf("capped frontier: got %v, want %v", got, fixture.FeasiblePoints)
			}
			checker, err := validate.New(fixture.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			for _, journey := range verified.Journeys {
				if _, err := checker.Check(fixture.Query, journey); err != nil {
					t.Fatal("independent full witness replay", err)
				}
			}
		})
	}
}

func TestTREXCapCertificationBudgetOutcomes(t *testing.T) {
	ctx := context.Background()
	var fixture syntheticCapCase
	for _, candidate := range syntheticCapCases(t) {
		if candidate.Name == "accepted_without_walking" {
			fixture = candidate
		}
	}
	if fixture.Name == "" {
		t.Fatal("positive control missing")
	}
	for _, kind := range []string{"ranked_work", "verification_scans", "verification_projection_scans"} {
		t.Run(kind, func(t *testing.T) {
			data := fixture.Dataset.Clone()
			config, limits := syntheticCapConfig(fixture.Levels), syntheticCapVerifyLimits()
			if kind == "ranked_work" {
				config.Limits.MaxQueryWork = 1
			} else {
				limits.MaxScans = 1
			}
			if kind == "verification_scans" {
				// A second, dominated ride forces a second enumeration scan. It
				// leaves the single feasible optimum of the positive control intact.
				data.Trips = append(data.Trips, model.Trip{ID: "later", Accessible: model.Yes})
				data.Connections = append(data.Connections, model.Connection{ID: "later", TripID: "later", From: "Z", To: "T", Departure: 26300, Arrival: 26500, Pickup: true, Dropoff: true})
			}
			index, err := BuildTREXStaticRulesCandidate(ctx, data, config)
			if err != nil || !index.Complete() {
				t.Fatal("construction incomplete", err)
			}
			certificate, err := PrepareTREXStaticCapCertificate(ctx, index, limits)
			if err != nil {
				t.Fatal(err)
			}
			result, err := certificate.Search(ctx, fixture.Query)
			if err == nil || result.Complete || result.Certificate.Accepted || len(result.Journeys) != 0 || result.DeclineReason == "frontier_mismatch" {
				t.Fatal("budget failure claimed complete or mismatched", result, err)
			}
			switch kind {
			case "ranked_work":
				if result.DeclineReason != "ranked_search_or_original_resource_guard" || !strings.Contains(err.Error(), "query work budget exhausted") || result.VerificationScans != 0 || result.VerificationComplete {
					t.Fatal(result, err)
				}
			case "verification_scans":
				if result.DeclineReason != "verification_incomplete" || !strings.Contains(err.Error(), "enumeration limit reached: scans") || result.VerificationScans != 1 || result.VerificationComplete {
					t.Fatal(result, err)
				}
			case "verification_projection_scans":
				if result.DeclineReason != "ranked_frontier_projection" || !strings.Contains(err.Error(), "projection scans") || !result.VerificationComplete {
					t.Fatal(result, err)
				}
			}
		})
	}
}
