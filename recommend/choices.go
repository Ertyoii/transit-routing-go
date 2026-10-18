package recommend

import (
	"context"
	"errors"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
)

// Preferences are explicit score hypotheses, never an inferred user model.
// Score = elapsed + transfer_seconds*transfers + extra_walk_weight*walking.
type Preference struct {
	ID              string `json:"id"`
	TransferSeconds int64  `json:"transfer_seconds"`
	ExtraWalkWeight int64  `json:"extra_walk_weight"`
}
type PreferenceGroup struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Preferences []Preference `json:"preferences"`
}
type ChoiceConfig struct {
	MaxSubsets    int               `json:"max_subsets"`
	MaxCandidates int               `json:"max_candidates"`
	Groups        []PreferenceGroup `json:"groups"`
}

func (c ChoiceConfig) Validate() error {
	if c.MaxSubsets < 1 || c.MaxSubsets > 10000000 || c.MaxCandidates < 1 || c.MaxCandidates > 10000 || len(c.Groups) < 1 || len(c.Groups) > 8 {
		return fmt.Errorf("invalid choice configuration bounds")
	}
	seen := map[string]bool{}
	for _, g := range c.Groups {
		if g.Name == "" || seen[g.Name] || len(g.Preferences) < 1 || len(g.Preferences) > 128 {
			return fmt.Errorf("invalid preference group")
		}
		seen[g.Name] = true
		ids := map[string]bool{}
		for _, p := range g.Preferences {
			if p.ID == "" || ids[p.ID] || p.TransferSeconds < 0 || p.TransferSeconds > model.MaxServiceSecond || p.ExtraWalkWeight < 0 || p.ExtraWalkWeight > 1000 {
				return fmt.Errorf("invalid preference: %s", p.ID)
			}
			ids[p.ID] = true
		}
	}
	return nil
}

var ErrSelectionBudget = errors.New("selection budget exhausted")

type PreferenceScore struct {
	PreferenceID  string `json:"preference_id"`
	CandidateBest *int64 `json:"candidate_best"`
	SelectedBest  *int64 `json:"selected_best"`
	Regret        *int64 `json:"pool_relative_regret"`
}
type Choice struct {
	Method                string            `json:"method"`
	Limit                 int               `json:"limit"`
	Selected              []string          `json:"selected"`
	Complete              bool              `json:"complete"`
	PoolOptimal           bool              `json:"pool_optimal"`
	GlobalOptimalityClaim bool              `json:"global_optimality_claim"`
	WorstRegret           *int64            `json:"worst_pool_regret"`
	TotalRegret           *int64            `json:"total_pool_regret"`
	CoveredScenarios      int               `json:"covered_scenarios"`
	EvaluatedSubsets      int               `json:"evaluated_subsets"`
	Scores                []PreferenceScore `json:"scores"`
}
type GroupChoices struct {
	Group      PreferenceGroup `json:"group"`
	Selections []Choice        `json:"selections"`
}
type Choices struct {
	Policy          string            `json:"policy"`
	Complete        bool              `json:"complete"`
	Error           string            `json:"error,omitempty"`
	InputCandidates int               `json:"input_candidates"`
	Representatives []string          `json:"representatives"`
	Exclusions      map[string]string `json:"exclusions"`
	Groups          []GroupChoices    `json:"groups"`
}

// EvaluateChoices validates complete itineraries before either selector sees
// features. Legacy envelope results remain labeled controls in Result.Selections.
// Optimality refers only to the supplied pool and finite explicit score groups.
func (x *Index) EvaluateChoices(ctx context.Context, q model.Query, js []model.Journey, cfg ChoiceConfig) (out Result, err error) {
	if err = cfg.Validate(); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	out, err = x.Evaluate(q, js)
	if err != nil {
		return
	}
	choices := &Choices{Policy: "finite_preferences_v2", InputCandidates: len(out.Features), Representatives: []string{}, Exclusions: map[string]string{}, Groups: []GroupChoices{}}
	out.Choices = choices
	defer func() {
		if err != nil {
			choices.Error = err.Error()
			choices.Complete = false
			choices.Groups = []GroupChoices{}
		}
	}()
	if len(out.Features) > cfg.MaxCandidates {
		err = ErrSelectionBudget
		return
	}
	pool := choiceRepresentatives(out.Features, choices.Exclusions)
	for _, f := range pool {
		choices.Representatives = append(choices.Representatives, f.Identity)
	}
	for _, g := range cfg.Groups {
		if err = ctx.Err(); err != nil {
			return
		}
		group := GroupChoices{Group: g, Selections: []Choice{}}
		for _, k := range []int{3, 5} {
			ids := unboundedAnchors(pool, k)
			control := describeChoice(pool, g.Preferences, ids, "no_envelope_anchors", k)
			control.Complete = true
			group.Selections = append(group.Selections, control)
			var selected []int
			var states int
			selected, states, err = minimax(ctx, pool, g.Preferences, k, cfg.MaxSubsets)
			if err != nil {
				return
			}
			exact := describeChoice(pool, g.Preferences, selected, "preference_minimax", k)
			exact.Complete = true
			exact.PoolOptimal = true
			exact.EvaluatedSubsets = states
			group.Selections = append(group.Selections, exact)
		}
		choices.Groups = append(choices.Groups, group)
	}
	if err = ctx.Err(); err != nil {
		return
	}
	choices.Complete = true
	return
}

// Equal objective vectors retain one identity; strict monotone dominance is safe
// only for the three fields and nonnegative score coefficients declared here.
func choiceRepresentatives(fs []Feature, excluded map[string]string) []Feature {
	ordered := append([]Feature{}, fs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Identity < ordered[j].Identity })
	seen := map[[3]int64]bool{}
	out := []Feature{}
	for _, f := range ordered {
		v := [3]int64{f.Duration, int64(f.Metrics.Transfers), f.Metrics.WalkingSeconds}
		if seen[v] {
			excluded[f.Identity] = "equal_score_vector"
			continue
		}
		seen[v] = true
		dominated := false
		for _, o := range fs {
			a := [3]int64{o.Duration, int64(o.Metrics.Transfers), o.Metrics.WalkingSeconds}
			if a[0] <= v[0] && a[1] <= v[1] && a[2] <= v[2] && a != v {
				dominated = true
				break
			}
		}
		if dominated {
			excluded[f.Identity] = "monotone_score_vector_dominated"
			continue
		}
		out = append(out, f)
	}
	return out
}
func preferenceCost(f Feature, p Preference) int64 {
	return f.Duration + p.TransferSeconds*int64(f.Metrics.Transfers) + p.ExtraWalkWeight*f.Metrics.WalkingSeconds
}
func unboundedAnchors(pool []Feature, k int) []int {
	out := []int{}
	if len(pool) == 0 {
		return out
	}
	for _, metric := range []func(Feature) int64{score, func(f Feature) int64 { return f.Duration }, func(f Feature) int64 { return f.Metrics.WalkingSeconds }, func(f Feature) int64 { return int64(f.Metrics.Transfers) }} {
		best := 0
		for i := 1; i < len(pool); i++ {
			if metric(pool[i]) < metric(pool[best]) || (metric(pool[i]) == metric(pool[best]) && less(pool[i], pool[best])) {
				best = i
			}
		}
		found := false
		for _, i := range out {
			found = found || i == best
		}
		if !found && len(out) < k {
			out = append(out, best)
		}
	}
	return out
}
func describeChoice(pool []Feature, prefs []Preference, selected []int, method string, k int) Choice {
	c := Choice{Method: method, Limit: k, Selected: []string{}, Scores: []PreferenceScore{}}
	selected = append([]int{}, selected...)
	sort.Slice(selected, func(i, j int) bool {
		a, b := pool[selected[i]], pool[selected[j]]
		x, y := preferenceCost(a, prefs[0]), preferenceCost(b, prefs[0])
		if x != y {
			return x < y
		}
		return a.Identity < b.Identity
	})
	for _, i := range selected {
		c.Selected = append(c.Selected, pool[i].Identity)
	}
	var worst, sum int64
	for _, p := range prefs {
		s := PreferenceScore{PreferenceID: p.ID}
		for _, f := range pool {
			value := preferenceCost(f, p)
			if s.CandidateBest == nil || value < *s.CandidateBest {
				v := value
				s.CandidateBest = &v
			}
		}
		for _, i := range selected {
			value := preferenceCost(pool[i], p)
			if s.SelectedBest == nil || value < *s.SelectedBest {
				v := value
				s.SelectedBest = &v
			}
		}
		if s.CandidateBest != nil && s.SelectedBest != nil {
			v := *s.SelectedBest - *s.CandidateBest
			s.Regret = &v
			worst = max(worst, v)
			sum += v
			if v == 0 {
				c.CoveredScenarios++
			}
		}
		c.Scores = append(c.Scores, s)
	}
	if len(pool) > 0 && len(selected) > 0 {
		c.WorstRegret = &worst
		c.TotalRegret = &sum
	}
	return c
}
func minimax(ctx context.Context, pool []Feature, prefs []Preference, k, limit int) ([]int, int, error) {
	if len(pool) == 0 {
		return []int{}, 0, ctx.Err()
	}
	costs := make([][]int64, len(pool))
	opt := make([]int64, len(prefs))
	for i, f := range pool {
		costs[i] = make([]int64, len(prefs))
		for j, p := range prefs {
			costs[i][j] = preferenceCost(f, p)
			if i == 0 || costs[i][j] < opt[j] {
				opt[j] = costs[i][j]
			}
		}
	}
	var best []int
	var bestMax, bestSum int64
	states := 0
	// Size-first, lexicographic exhaustive subsets. A first zero-regret set is
	// optimal in worst/total regret and cardinality over the supplied pool.
	// Identity ties are canonical only among these retained representatives.
	for size := 1; size <= min(k, len(pool)); size++ {
		chosen := []int{}
		zero := false
		var visit func(int) error
		visit = func(start int) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			if len(chosen) == size {
				if states == limit {
					return ErrSelectionBudget
				}
				states++
				var worst, sum int64
				for j := range prefs {
					value := costs[chosen[0]][j]
					for _, i := range chosen[1:] {
						value = min(value, costs[i][j])
					}
					regret := value - opt[j]
					worst = max(worst, regret)
					sum += regret
				}
				if best == nil || worst < bestMax || (worst == bestMax && sum < bestSum) {
					best = append([]int{}, chosen...)
					bestMax = worst
					bestSum = sum
				}
				if worst == 0 {
					zero = true
				}
				return nil
			}
			for i := start; i <= len(pool)-(size-len(chosen)); i++ {
				chosen = append(chosen, i)
				if e := visit(i + 1); e != nil {
					return e
				}
				chosen = chosen[:len(chosen)-1]
				if zero {
					return nil
				}
			}
			return nil
		}
		if e := visit(0); e != nil {
			return nil, states, e
		}
		if zero {
			return best, states, nil
		}
	}
	return best, states, nil
}
