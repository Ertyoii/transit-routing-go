package recommend

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func fillTestConfig(k int) HybridFillConfig {
	safe := finiteSafeTestConfig("absolute")
	finite := safe.Baseline
	finite.DeliveryLimits = []int{k}
	base := ChoiceConfig{MaxCandidates: 1024, MaxSubsets: 200000, Bounded: &BoundedConfig{MinTransfer: 0, MaxTransfer: 900, MinWalk: 0, MaxWalk: 4, Objective: "absolute", Limits: []int{k}, MaxCriticalPoints: 100000, MaxWork: 50000000, MaxMatrixBytes: 67108864, ExactWhenFeasible: true}}
	return HybridFillConfig{Base: base, Finite20: finite, MaxFillSubsets: 200000, MaxFillWork: 20000000, MaxFillMatrixBytes: 8388608}
}
func fillTestFeatures() []Feature {
	return []Feature{finiteSafeFeature("a", "a", 100, 0, 0), finiteSafeFeature("b", "b", 200, 1, 10), finiteSafeFeature("c", "c", 300, 2, 20)}
}
func TestHybridFillPreservesUnchangedBaseAndRequestedCardinality(t *testing.T) {
	for _, k := range []int{3, 5} {
		cfg := fillTestConfig(k)
		fs := fillTestFeatures()
		base, err := SelectBounded(context.Background(), fs, cfg.Base)
		if err != nil {
			t.Fatal(err)
		}
		got, err := SelectHybridFill(context.Background(), fs, cfg)
		if err != nil || !got.Complete || !got.FillRequired || got.Selection == nil || len(got.Selection.Selected) != 3 || !got.Selection.SetInclusionVerified || !got.Selection.FillCardinalitySatisfied || got.Selection.FilledSubsetOptimalClaim {
			t.Fatalf("fill failed %+v %v", got, err)
		}
		if !reflect.DeepEqual(base.Selections[0].Selected, got.Base.Selections[0].Selected) || !reflect.DeepEqual(base.Selections[0].Selected, got.Selection.BaseSelected) {
			t.Fatal("base changed")
		}
		for i, id := range base.Selections[0].Selected {
			if got.Selection.Selected[i] != id {
				t.Fatal("base identity dropped/reordered")
			}
		}
		if got.Selection.WorstAbsoluteUpperBound == nil || *got.Selection.WorstAbsoluteUpperBound != *base.Selections[0].WorstAbsolute {
			t.Fatal("base upper bound corrupted")
		}
		if got.ConfigValueSHA256 == "" || got.BaseConfigValueSHA256 == "" || got.ConfigValueCanonicalJSON == "" {
			t.Fatal("missing config provenance")
		}
	}
}
func TestHybridFillSetInclusionWorksOutsideRectangle(t *testing.T) {
	cfg := fillTestConfig(3)
	fs := fillTestFeatures()
	r, err := SelectHybridFill(context.Background(), fs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Feature{}
	for _, f := range fs {
		by[f.Identity] = f
	}
	for _, w := range []WeightPoint{{0, 0}, {1800, 8}, {-500, -10}, {0, -100}} {
		scoreAt := func(ids []string) float64 {
			best := 1e30
			for _, id := range ids {
				f := by[id]
				v := float64(f.Duration) + w.Transfer*float64(f.Metrics.Transfers) + w.Walk*float64(f.Metrics.WalkingSeconds)
				if v < best {
					best = v
				}
			}
			return best
		}
		if scoreAt(r.Selection.Selected) > scoreAt(r.Selection.BaseSelected) {
			t.Fatal("set inclusion inequality failed")
		}
	}
}
func TestHybridFillNoFallbackAndConditionalWork(t *testing.T) {
	cfg := fillTestConfig(3)
	cfg.MaxFillWork = 1
	r, err := SelectHybridFill(context.Background(), fillTestFeatures(), cfg)
	if !errors.Is(err, ErrSelectionBudget) || r == nil || r.Complete || r.Selection != nil || r.Base == nil || !r.Base.Complete {
		t.Fatalf("hidden base fallback %+v %v", r, err)
	}
	r, err = SelectHybridFill(context.Background(), fillTestFeatures()[:1], cfg)
	if err != nil || !r.Complete || r.FillRequired || r.Preflight != nil || r.Finite20 != nil || r.Pattern != nil || r.ChargedFillWork != 0 {
		t.Fatalf("unrequested fill controls evaluated %+v %v", r, err)
	}
	r, err = SelectHybridFill(context.Background(), nil, cfg)
	if err != nil || !r.Complete || len(r.Selection.Selected) != 0 {
		t.Fatalf("empty failed %+v %v", r, err)
	}
}
func TestHybridFillPermutationCancelAndRecursiveRejection(t *testing.T) {
	cfg := fillTestConfig(3)
	fs := fillTestFeatures()
	a, err := SelectHybridFill(context.Background(), fs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fs[0], fs[2] = fs[2], fs[0]
	b, err := SelectHybridFill(context.Background(), fs, cfg)
	if err != nil || !reflect.DeepEqual(a.Selection.Selected, b.Selection.Selected) {
		t.Fatal("permutation changed fill")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := SelectHybridFill(ctx, fs, cfg)
	if !errors.Is(err, context.Canceled) || r == nil || r.Complete || r.Selection != nil {
		t.Fatal("cancel falsely complete")
	}
	cfg.Base.HybridFill = &cfg
	if _, err = SelectHybridFill(context.Background(), fs, cfg); err == nil {
		t.Fatal("recursive base accepted")
	}
}
