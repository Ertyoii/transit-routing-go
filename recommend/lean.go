package recommend

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
)

// EvaluateLeanChoices is an equal-requested-output control. It preserves legacy
// finite-grid minimax and pattern-anchor selections but omits unrequested k5,
// legacy anchor controls and recovery enumeration. It is not a new quality model.
func (x *Index) EvaluateLeanChoices(ctx context.Context, q model.Query, js []model.Journey, cfg ChoiceConfig) (out Result, err error) {
	if err = cfg.Validate(); err != nil {
		return
	}
	if !cfg.LeanDelivery || nestedDeliveryConfig(cfg) || cfg.Bounded != nil {
		return out, fmt.Errorf("missing legacy lean delivery config")
	}
	if err = ctx.Err(); err != nil {
		return
	}
	out = Result{Policy: "finite_preferences_v2_lean_delivery", FeatureMode: "validated_objective_pattern_slack_no_recovery", Features: []Feature{}, Selections: []Selection{}}
	defer func() {
		if err != nil {
			// Pattern selections have no completion flag, so never publish them
			// alongside a failed or canceled lean request.
			out.Selections = []Selection{}
		}
	}()
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
	if len(out.Features) > cfg.MaxCandidates {
		return out, ErrSelectionBudget
	}
	limits := cfg.DeliveryLimits
	if len(limits) == 0 {
		limits = []int{3}
	}
	if cfg.DeliveryPolicy == "pattern_anchors_v1" {
		out.Policy = "pattern_anchors_v1_lean_delivery"
		for _, k := range limits {
			out.Selections = append(out.Selections, selectFeatures(out.Features, k, 300))
		}
		err = ctx.Err()
		return
	}
	choices := &Choices{Policy: out.Policy, InputCandidates: len(out.Features), Representatives: []string{}, Exclusions: map[string]string{}, Groups: []GroupChoices{}}
	out.Choices = choices
	defer func() {
		if err != nil {
			choices.Error = err.Error()
			choices.Complete = false
			choices.Groups = []GroupChoices{}
		}
	}()
	pool := choiceRepresentatives(out.Features, choices.Exclusions)
	for _, f := range pool {
		choices.Representatives = append(choices.Representatives, f.Identity)
	}
	for _, g := range cfg.Groups {
		group := GroupChoices{Group: g, Selections: []Choice{}}
		for _, k := range limits {
			var selected []int
			var states int
			selected, states, err = minimax(ctx, pool, g.Preferences, k, cfg.MaxSubsets)
			if err != nil {
				return
			}
			c := describeChoice(pool, g.Preferences, selected, "preference_minimax", k)
			c.Complete = true
			c.PoolOptimal = true
			c.EvaluatedSubsets = states
			group.Selections = append(group.Selections, c)
		}
		choices.Groups = append(choices.Groups, group)
	}
	if err = ctx.Err(); err != nil {
		return
	}
	choices.Complete = true
	return
}
