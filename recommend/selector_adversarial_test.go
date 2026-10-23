package recommend

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
)

func TestBoundedRejectsOutOfContractFeaturesAndRectangles(t *testing.T) {
	for _, feature := range []Feature{{Identity: ""}, f("a", -1, 0, 0), f("a", model.MaxServiceSecond+1, 0, 0), f("a", 0, -1, 0), f("a", 0, 1025, 0), f("a", 0, 0, -1), f("a", 0, 0, model.MaxServiceSecond+1)} {
		r, err := SelectBounded(context.Background(), []Feature{feature}, boundedConfig(true))
		if err == nil || r == nil || r.Complete || len(r.Selections) != 0 {
			t.Fatalf("invalid feature completed: %+v %v", r, err)
		}
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		cfg := boundedConfig(true)
		cfg.Bounded.MaxTransfer = bad
		if cfg.Validate() == nil {
			t.Fatalf("nonfinite rectangle accepted: %v", bad)
		}
	}
}
func TestBoundedAllComputeGuardsFailClosed(t *testing.T) {
	fs := finiteSafeTrap()
	for _, tc := range []struct {
		name   string
		change func(*ChoiceConfig)
	}{
		{"candidate", func(c *ChoiceConfig) { c.MaxCandidates = 1 }},
		{"subset", func(c *ChoiceConfig) { c.MaxSubsets = 1 }},
		{"critical_points", func(c *ChoiceConfig) { c.Bounded.MaxCriticalPoints = 4 }},
		{"matrix", func(c *ChoiceConfig) { c.Bounded.MaxMatrixBytes = 1 }},
		{"work", func(c *ChoiceConfig) { c.Bounded.MaxWork = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := boundedConfig(true)
			tc.change(&cfg)
			r, err := SelectBounded(context.Background(), fs, cfg)
			if !errors.Is(err, ErrSelectionBudget) || r == nil || r.Complete || r.Error == "" {
				t.Fatalf("guard failed open: %+v %v", r, err)
			}
			for _, s := range r.Selections {
				if s.Complete || s.SubsetOptimal || s.GlobalOptimalityClaim {
					t.Fatalf("partial answer claimed complete/optimal: %+v", s)
				}
			}
		})
	}
}
func TestFixedPoliciesPreflightLargePoolsWithoutFallback(t *testing.T) {
	safe := finiteSafeTestConfig("absolute")
	p, _, _ := finiteSafeResource(64, safe)
	if p.Feasible || p.WorkUpperBound <= 0 || p.MatrixBytesUpperBound <= 0 {
		t.Fatalf("large safe preflight invalid: %+v", p)
	}
	fs := make([]Feature, 64)
	for i := range fs {
		fs[i] = finiteSafeFeature(string(rune('a'+i)), "native", int64(i), 0, 0)
	}
	r, err := SelectFiniteSafe(context.Background(), fs, safe)
	if !errors.Is(err, ErrSelectionBudget) || r == nil || r.Complete || r.Selection != nil || r.Baseline != nil || r.FailureStage != "resource_preflight" {
		t.Fatalf("large safe pool fell back: %+v %v", r, err)
	}
	for _, k := range []int{3, 5} {
		p := hybridFillResource(1024, k, fillTestConfig(k))
		if p.Feasible || p.WorkUpperBound <= 0 || p.StatesUpperBound <= 0 || p.MatrixBytesUpperBound <= 0 {
			t.Fatalf("hybrid fill preflight overflow/bypass: %+v", p)
		}
	}
}

// Deterministically cancel only at the adapter's final check, after the pattern
// output has been computed. No goroutine or wall-clock race is involved.
type cancelOnErrContext struct {
	context.Context
	calls    int
	cancelAt int
}

func (c *cancelOnErrContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}
func TestLeanPatternLateCancellationDiscardsSelection(t *testing.T) {
	d, q, _ := runtimeFixture()
	x, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ChoiceConfig{MaxCandidates: 10, LeanDelivery: true, DeliveryPolicy: "pattern_anchors_v1", DeliveryLimits: []int{3, 5}}
	ctx := &cancelOnErrContext{Context: context.Background(), cancelAt: 2}
	r, err := x.EvaluateChoices(ctx, q, nil, cfg)
	if !errors.Is(err, context.Canceled) || len(r.Selections) != 0 {
		t.Fatalf("late cancellation published selection: %+v %v", r, err)
	}
}
