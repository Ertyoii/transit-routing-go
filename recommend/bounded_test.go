package recommend

import (
	"context"
	"errors"
	"github.com/ertyoii/transit-routing-go/model"
	"math"
	"math/rand"
	"reflect"
	"testing"
)

func f(id string, d int64, t int, w int64) Feature {
	return Feature{Identity: id, Pattern: id, Duration: d, Metrics: model.Metrics{Transfers: t, WalkingSeconds: w}}
}
func boundedConfig(exact bool) ChoiceConfig {
	return ChoiceConfig{MaxCandidates: 100, MaxSubsets: 1000000, Bounded: &BoundedConfig{MaxTransfer: 900, MaxWalk: 4, Objective: "absolute", Limits: []int{3, 5}, MaxCriticalPoints: 100000, MaxWork: 100000000, Exact: exact}}
}
func TestContinuousInteriorMaximum(t *testing.T) {
	cfg := boundedConfig(false)
	e := boundedEngine{ctx: context.Background(), cfg: *cfg.Bounded, maxSubsets: 100}
	e.fs = []Feature{f("A", 1900, 0, 300), f("B", 1600, 2, 0), f("C", 2500, 0, 0), f("R", 1800, 1, 100)}
	if err := e.critical(); err != nil {
		t.Fatal(err)
	}
	v, err := e.evaluate([]int{0, 1, 2})
	if err != nil || math.Abs(v.a-50) > 1e-8 {
		t.Fatalf("interior regret missing: %+v %v", v, err)
	}
	if e.points[v.ai] != (WeightPoint{450, 2}) {
		t.Fatal(e.points[v.ai])
	}
}
func TestCriticalSetDominatesDenseOffgrid(t *testing.T) {
	rng := rand.New(rand.NewSource(1949))
	for trial := 0; trial < 30; trial++ {
		cfg := boundedConfig(false)
		e := boundedEngine{ctx: context.Background(), cfg: *cfg.Bounded, maxSubsets: 100}
		for i := 0; i < 8; i++ {
			e.fs = append(e.fs, f(string(rune('a'+i)), int64(500+rng.Intn(4000)), rng.Intn(4), int64(rng.Intn(600))))
		}
		if err := e.critical(); err != nil {
			t.Fatal(err)
		}
		for _, s := range [][]int{{0}, {0, 1}, {0, 2, 4}, {1, 3, 5, 7}} {
			v, err := e.evaluate(s)
			if err != nil {
				t.Fatal(err)
			}
			for p := 0; p < 1000; p++ {
				a, b := rng.Float64()*900, rng.Float64()*4
				opt, sel := math.Inf(1), math.Inf(1)
				for _, f := range e.fs {
					opt = math.Min(opt, float64(f.Duration)+a*float64(f.Metrics.Transfers)+b*float64(f.Metrics.WalkingSeconds))
				}
				for _, i := range s {
					f := e.fs[i]
					sel = math.Min(sel, float64(f.Duration)+a*float64(f.Metrics.Transfers)+b*float64(f.Metrics.WalkingSeconds))
				}
				if sel-opt > v.a+1e-7 || (sel-opt)/(1+opt) > v.r+1e-10 {
					t.Fatalf("critical set misses dense off-grid: trial%d %+v %f %f", trial, v, sel-opt, (sel-opt)/(1+opt))
				}
			}
		}
	}
}
func TestBoundedPermutationExactNestedAndCanonical(t *testing.T) {
	fs := []Feature{f("A", 1900, 0, 300), f("B", 1600, 2, 0), f("C", 2500, 0, 0), f("R", 1800, 1, 100)}
	cfg := boundedConfig(true)
	cfg.Bounded.Nested = true
	a, err := SelectBounded(context.Background(), fs, cfg)
	if err != nil || !a.Complete || !a.Selections[0].SubsetOptimal {
		t.Fatalf("%+v %v", a, err)
	}
	for _, id := range a.Selections[0].Selected {
		found := false
		for _, x := range a.Selections[1].Selected {
			found = found || x == id
		}
		if !found {
			t.Fatal("nesting lost")
		}
	}
	for i, j := 0, len(fs)-1; i < j; i, j = i+1, j-1 {
		fs[i], fs[j] = fs[j], fs[i]
	}
	b, err := SelectBounded(context.Background(), fs, cfg)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("permutation changed output: %v", err)
	}
	cfg.Bounded.Limits = []int{5}
	out, err := SelectBounded(context.Background(), []Feature{f("a", 100, 0, 0), f("b", 100, 0, 0)}, cfg)
	if err != nil || len(out.Selections[0].Selected) != 1 {
		t.Fatalf("redundant vectors not canonical: %+v %v", out, err)
	}
}
func TestDistinctPatternsRetainEqualAndDominatedVectors(t *testing.T) {
	cfg := boundedConfig(true)
	cfg.Bounded.DistinctPatterns = true
	fs := []Feature{f("a", 100, 0, 0), f("b", 100, 0, 0), f("c", 200, 1, 10)}
	out, err := SelectBounded(context.Background(), fs, cfg)
	if err != nil || out.RetainedCandidates != 3 {
		t.Fatalf("pattern candidates collapsed: %+v %v", out, err)
	}
	cfg.Bounded.DistinctPatterns = false
	out, err = SelectBounded(context.Background(), fs, cfg)
	if err != nil || out.RetainedCandidates != 1 {
		t.Fatalf("score pruning failed: %+v %v", out, err)
	}
}
func TestContinuousNarrowZeroAndGuards(t *testing.T) {
	for _, bounds := range [][4]float64{{1.25, 1.25, .2, 3.8}, {12.123456, 12.123457, 2.1, 2.100001}, {0, 900, 0, 4}} {
		cfg := boundedConfig(true)
		cfg.Bounded.MinTransfer = bounds[0]
		cfg.Bounded.MaxTransfer = bounds[1]
		cfg.Bounded.MinWalk = bounds[2]
		cfg.Bounded.MaxWalk = bounds[3]
		out, err := SelectBounded(context.Background(), []Feature{f("zero", 0, 0, 0)}, cfg)
		if err != nil || !out.Complete || *out.Selections[0].WorstNormalized != 0 {
			t.Fatalf("narrow/zero: %+v %v", out, err)
		}
	}
	cfg := boundedConfig(true)
	cfg.Bounded.MaxWork = 1
	out, err := SelectBounded(context.Background(), []Feature{f("a", 100, 1, 0), f("b", 120, 0, 10)}, cfg)
	if !errors.Is(err, ErrSelectionBudget) || out.Complete {
		t.Fatalf("budget falsely complete: %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err = SelectBounded(ctx, []Feature{f("a", 100, 0, 0)}, boundedConfig(false))
	if !errors.Is(err, context.Canceled) || out.Complete {
		t.Fatal("cancel ignored")
	}
}

func TestBoundedRejectsDuplicateAndMemoryPreflight(t *testing.T) {
	cfg := boundedConfig(false)
	cfg.Bounded.MaxMatrixBytes = 1
	out, err := SelectBounded(context.Background(), []Feature{f("a", 100, 1, 0), f("b", 120, 0, 10)}, cfg)
	if !errors.Is(err, ErrSelectionBudget) || out.Complete {
		t.Fatal("matrix guard failed")
	}
	cfg = boundedConfig(false)
	out, err = SelectBounded(context.Background(), []Feature{f("a", 100, 1, 0), f("a", 120, 0, 10)}, cfg)
	if err == nil || out.Complete {
		t.Fatal("duplicate identities accepted")
	}
	cfg.LeanDelivery = true
	if cfg.Validate() == nil {
		t.Fatal("ambiguous lean/bounded config accepted")
	}
}
func TestAutoExactResourceDecisionIndependentAndDeterministic(t *testing.T) {
	fs := []Feature{f("A", 1900, 0, 300), f("B", 1600, 2, 0), f("C", 2500, 0, 0), f("R", 1800, 1, 100)}
	cfg := boundedConfig(false)
	cfg.Bounded.ExactWhenFeasible = true
	auto, err := SelectBounded(context.Background(), fs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !auto.Selections[0].SubsetOptimal || auto.Selections[0].Method != "exhaustive_subset_resource_feasible" {
		t.Fatalf("tiny feasible pool did not use exact: %+v", auto)
	}
	cfg.Bounded.ExactWhenFeasible = false
	cfg.Bounded.Exact = true
	exact, err := SelectBounded(context.Background(), fs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(auto.Selections[0].Selected, exact.Selections[0].Selected) || *auto.Selections[0].WorstAbsolute != *exact.Selections[0].WorstAbsolute {
		t.Fatal("auto differs exact")
	}
	// A many-label synthetic pool forces conservative fallback before the search.
	fs = nil
	for i := 0; i < 16; i++ {
		fs = append(fs, f(string(rune('a'+i)), int64(1000+80*i), 15-i, int64(i*i*2)))
	}
	cfg = boundedConfig(false)
	cfg.Bounded.ExactWhenFeasible = true
	cfg.Bounded.Limits = []int{3}
	cfg.Bounded.MaxWork = 100000
	auto, err = SelectBounded(context.Background(), fs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if auto.Selections[0].SubsetOptimal || auto.Selections[0].Method != "greedy_best_swap_local" || auto.Selections[0].ExactResourceFeasible {
		t.Fatalf("resource-infeasible exact decision wrong: %+v", auto.Selections[0])
	}
	cfg.Bounded.ExactWhenFeasible = false
	greedy, err := SelectBounded(context.Background(), fs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(auto.Selections[0].Selected, greedy.Selections[0].Selected) {
		t.Fatal("preflight fallback changes greedy output")
	}
}
