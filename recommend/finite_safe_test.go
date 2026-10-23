package recommend

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
)

func finiteSafeTestConfig(objective string) FiniteSafeConfig {
	prefs := []Preference{}
	for _, t := range []int64{0, 150, 300, 600, 900} {
		for _, w := range []int64{0, 1, 2, 4} {
			prefs = append(prefs, Preference{ID: fmt.Sprintf("t%d_w%d", t, w), TransferSeconds: t, ExtraWalkWeight: w})
		}
	}
	return FiniteSafeConfig{MaxCandidates: 64, MaxStates: 200000, Baseline: ChoiceConfig{MaxCandidates: 1024, MaxSubsets: 200000, LeanDelivery: true, DeliveryPolicy: "finite_preferences_v2", DeliveryLimits: []int{3}, Groups: []PreferenceGroup{{Name: "explicit_twenty_hypotheses", Preferences: prefs}}}, Domain: BoundedConfig{MinTransfer: 0, MaxTransfer: 900, MinWalk: 0, MaxWalk: 4, Objective: objective, Limits: []int{3}, Exact: true, MaxCriticalPoints: 100000, MaxWork: 50000000, MaxMatrixBytes: 67108864}}
}
func finiteSafeFeature(id, pattern string, d int64, t int, w int64) Feature {
	return Feature{Identity: id, Pattern: pattern, Duration: d, Metrics: model.Metrics{Arrival: d, Transfers: t, Boardings: t + 1, WalkingSeconds: w}}
}
func finiteSafeTrap() []Feature {
	return []Feature{finiteSafeFeature("a", "a", 1900, 0, 300), finiteSafeFeature("b", "b", 1600, 2, 0), finiteSafeFeature("c", "c", 2500, 0, 0), finiteSafeFeature("r", "r", 1800, 1, 100)}
}
func TestFiniteSafePointwiseAndBaselineParity(t *testing.T) {
	for _, objective := range []string{"absolute", "normalized"} {
		cfg := finiteSafeTestConfig(objective)
		fs := finiteSafeTrap()
		got, err := SelectFiniteSafe(context.Background(), fs, cfg)
		if err != nil || got == nil || !got.Complete || got.Selection == nil || !got.Selection.SubsetOptimal {
			t.Fatalf("incomplete %v %+v", err, got)
		}
		exclusions := map[string]string{}
		pool := choiceRepresentatives(fs, exclusions)
		ids, _, err := minimax(context.Background(), pool, cfg.Baseline.Groups[0].Preferences, 3, 200000)
		if err != nil {
			t.Fatal(err)
		}
		want := describeChoice(pool, cfg.Baseline.Groups[0].Preferences, ids, "preference_minimax", 3)
		if !reflect.DeepEqual(want.Selected, got.Baseline.Selected) {
			t.Fatalf("historical baseline changed: %+v %+v", want.Selected, got.Baseline.Selected)
		}
		if len(got.Constraints) != 20 {
			t.Fatal("missing original20")
		}
		for _, c := range got.Constraints {
			if !c.PointwiseNonworse || c.RepairScore > c.BaselineScore {
				t.Fatalf("historical profile regression %+v", c)
			}
		}
		if got.RepairStates != 14 || got.Preflight.RepairStatesUpperBound != 14 {
			t.Fatalf("not exhaustive over4 choose<=3: %+v", got)
		}
	}
}
func TestFiniteSafeCanonicalPermutations(t *testing.T) {
	cfg := finiteSafeTestConfig("absolute")
	fs := finiteSafeTrap()
	a, err := SelectFiniteSafe(context.Background(), fs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(fs)-1; i < j; i, j = i+1, j-1 {
		fs[i], fs[j] = fs[j], fs[i]
	}
	b, err := SelectFiniteSafe(context.Background(), fs, cfg)
	if err != nil || !reflect.DeepEqual(a.Selection.Selected, b.Selection.Selected) || !reflect.DeepEqual(a.Baseline.Selected, b.Baseline.Selected) {
		t.Fatalf("permutation changes canonical choice %v", err)
	}
}
func TestFiniteSafeKeepsNativePatternAlternatives(t *testing.T) {
	cfg := finiteSafeTestConfig("absolute")
	fs := []Feature{finiteSafeFeature("a", "a", 100, 0, 0), finiteSafeFeature("b", "b", 100, 0, 0), finiteSafeFeature("c", "c", 200, 1, 20)}
	r, err := SelectFiniteSafe(context.Background(), fs, cfg)
	if err != nil || r.InputCandidates != 3 || r.InputPatterns != 3 || r.Preflight.CandidateCount != 3 || r.RepairStates != 7 || len(r.BaselineExclusions) != 2 {
		t.Fatalf("native alternatives hidden: %+v %v", r, err)
	}
}
func TestFiniteSafeNoFallbackOnGuardsOrCancel(t *testing.T) {
	cfg := finiteSafeTestConfig("absolute")
	cfg.Domain.MaxWork = 1
	r, err := SelectFiniteSafe(context.Background(), finiteSafeTrap(), cfg)
	if !errors.Is(err, ErrSelectionBudget) || r == nil || r.Complete || r.Selection != nil || r.Baseline != nil || r.FailureStage != "resource_preflight" {
		t.Fatalf("hidden guard fallback: %+v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err = SelectFiniteSafe(ctx, finiteSafeTrap(), finiteSafeTestConfig("absolute"))
	if !errors.Is(err, context.Canceled) || r == nil || r.Complete || r.Selection != nil {
		t.Fatalf("cancel falsely complete: %+v %v", r, err)
	}
}
func TestFiniteSafeEmptyAndRejectedContracts(t *testing.T) {
	cfg := finiteSafeTestConfig("absolute")
	r, err := SelectFiniteSafe(context.Background(), nil, cfg)
	if err != nil || !r.Complete || r.Selection == nil || len(r.Selection.Selected) != 0 || !r.BaselineComplete {
		t.Fatalf("empty contract: %+v %v", r, err)
	}
	f := finiteSafeFeature("a", "a", 0, 0, 0)
	r, err = SelectFiniteSafe(context.Background(), []Feature{f, f}, cfg)
	if err == nil || r == nil || r.Complete || r.Selection != nil {
		t.Fatalf("duplicate accepted: %+v %v", r, err)
	}
	cfg.Domain.DistinctPatterns = true
	if _, err = SelectFiniteSafe(context.Background(), []Feature{f}, cfg); err == nil {
		t.Fatal("unpreregistered distinct policy accepted")
	}
	cfg = finiteSafeTestConfig("absolute")
	cfg.Baseline.Groups[0].Preferences[0].TransferSeconds = 1
	if _, err = SelectFiniteSafe(context.Background(), []Feature{f}, cfg); err == nil {
		t.Fatal("changed historical20 accepted")
	}
}
func TestFiniteSafeRejectsRecursiveBaseline(t *testing.T) {
	cfg := finiteSafeTestConfig("absolute")
	cfg.Baseline.FiniteSafe = &cfg
	if _, err := SelectFiniteSafe(context.Background(), nil, cfg); err == nil {
		t.Fatal("recursive safe baseline accepted")
	}
}
