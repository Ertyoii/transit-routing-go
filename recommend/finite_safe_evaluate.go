package recommend

// Additive adapter for the explicitly selected finite-safe policy.
import (
	"context"
	"fmt"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
)

func (x *Index) EvaluateFiniteSafe(ctx context.Context, q model.Query, js []model.Journey, cfg ChoiceConfig) (out Result, err error) {
	if err = cfg.Validate(); err != nil {
		return
	}
	if cfg.FiniteSafe == nil {
		return out, fmt.Errorf("missing finite20-safe config")
	}
	// Reject accidental legacy constructor use, rather than silently paying for a
	// second checker/recovery indexes and calling it the qualified lean endpoint.
	if !x.lean {
		return out, fmt.Errorf("finite20-safe requires shared-checker lean index")
	}
	out = Result{Policy: "finite20_safe_" + cfg.FiniteSafe.Domain.Objective + "_exact_v1", FeatureMode: "validated_objective_pattern_slack_no_recovery", Features: []Feature{}, Selections: []Selection{}}
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
	// The historical finite20 exact baseline and all pointwise guard work execute
	// here inside the actual request's measured quality/validation/response scope.
	out.FiniteSafeChoices, err = SelectFiniteSafe(ctx, out.Features, *cfg.FiniteSafe)
	return
}
