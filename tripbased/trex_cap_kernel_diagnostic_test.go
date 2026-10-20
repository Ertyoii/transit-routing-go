// Copyright (c) 2026 Ertyoii
// SPDX-License-Identifier: MIT

package tripbased

import (
	"context"
	"reflect"
	"testing"

	"github.com/ertyoii/transit-routing-go/validate"
)

// This observation needs the existing private kernel because the public
// certificate intentionally clears all unchecked witnesses on failure. It adds
// no production export and never presents a raw witness as a certified answer.
func TestTREXCapKernelHiddenAlternatives(t *testing.T) {
	ctx := context.Background()
	for _, fixture := range syntheticCapCases(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			index, err := BuildTREXStaticRulesCandidate(ctx, fixture.Dataset, syntheticCapConfig(fixture.Levels))
			if err != nil || !index.Complete() {
				t.Fatal("construction incomplete", err)
			}
			// Every role/aggregate/boarding/leg cap remains the original value.
			raw, err := index.searchTREXForCertificate(ctx, fixture.Query, "overlay", true)
			if err != nil || !raw.Complete {
				t.Fatal("raw search incomplete", err)
			}
			if got := syntheticCapRefs(raw.Journeys); !reflect.DeepEqual(got, fixture.RawRefs) {
				t.Fatalf("raw witnesses: got %v, want %v", got, fixture.RawRefs)
			}
			// Frontier projection below describes the unchecked raw word only;
			// original-query feasibility is established independently just after it.
			rawPoints := syntheticCapFrontier(t, fixture.Dataset, fixture.Query, raw.Journeys)
			if !reflect.DeepEqual(rawPoints, fixture.RawPoints) {
				t.Fatal("literal raw objective mismatch", rawPoints)
			}
			checker, err := validate.New(fixture.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			for _, journey := range raw.Journeys {
				_, replayErr := checker.Check(fixture.Query, journey)
				if fixture.Accepted != (replayErr == nil) {
					t.Fatal("unexpected raw witness feasibility", replayErr)
				}
			}
			if fixture.Name == "access_equal_objective_alternative" {
				if !reflect.DeepEqual(rawPoints, fixture.FeasiblePoints) {
					t.Fatal("tie case must have identical raw and capped objectives")
				}
			} else if !fixture.Accepted && reflect.DeepEqual(rawPoints, fixture.FeasiblePoints) {
				t.Fatal("hidden-alternative case must have distinct raw and capped objectives")
			}
		})
	}
}
