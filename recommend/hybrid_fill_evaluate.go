package recommend

// All base, control, fill and hash work remains inside this adapter.
import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
)

func (x *Index) EvaluateHybridFill(ctx context.Context, q model.Query, js []model.Journey, cfg ChoiceConfig) (out Result, err error) {
	if err = cfg.Validate(); err != nil {
		return
	}
	if cfg.HybridFill == nil {
		return out, fmt.Errorf("missing absolute-hybrid fill config")
	}
	if !x.lean {
		return out, fmt.Errorf("absolute-hybrid fill requires shared-checker lean index")
	}
	out = Result{Policy: "bounded_absolute_hybrid_fill_v1", FeatureMode: "validated_objective_pattern_slack_no_recovery", Features: []Feature{}, Selections: []Selection{}}
	if _, err = x.checker.Binding(q); err != nil {
		return
	}
	seen := map[string]bool{}
	for _, j := range js {
		if err = ctx.Err(); err != nil {
			return
		}
		var v model.Validated
		v, err = x.checker.Check(q, j)
		if err != nil {
			return
		}
		if seen[v.Identity] {
			continue
		}
		seen[v.Identity] = true
		var f Feature
		f, err = x.measure(q, v, false)
		if err != nil {
			return
		}
		out.Features = append(out.Features, f)
	}
	sort.Slice(out.Features, func(i, j int) bool { return out.Features[i].Identity < out.Features[j].Identity })
	out.HybridFillChoices, err = SelectHybridFill(ctx, out.Features, *cfg.HybridFill)
	return
}
