package recommend

// This is a separately named delivery policy, never a silent
// change to bounded hybrid. Every successful filled display contains its base.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

type HybridFillConfig struct {
	Base               ChoiceConfig `json:"base"`
	Finite20           ChoiceConfig `json:"finite20"`
	MaxFillSubsets     int          `json:"max_fill_subsets"`
	MaxFillWork        int64        `json:"max_fill_work"`
	MaxFillMatrixBytes int64        `json:"max_fill_matrix_bytes"`
}
type HybridFillPreflight struct {
	CandidateCount        int   `json:"candidate_count"`
	Limit                 int   `json:"limit"`
	StatesUpperBound      int64 `json:"states_upper_bound"`
	WorkUpperBound        int64 `json:"work_upper_bound"`
	MatrixBytesUpperBound int64 `json:"matrix_bytes_upper_bound"`
	Feasible              bool  `json:"resource_feasible"`
}
type FillAddition struct {
	Identity string `json:"identity"`
	Source   string `json:"source"`
}
type HybridFilledSelection struct {
	Limit                     int            `json:"limit"`
	BaseSelected              []string       `json:"base_selected"`
	Added                     []FillAddition `json:"added"`
	Selected                  []string       `json:"selected"`
	Complete                  bool           `json:"complete"`
	SetInclusionVerified      bool           `json:"set_inclusion_verified"`
	RequestedCardinality      int            `json:"requested_cardinality"`
	FillCardinalitySatisfied  bool           `json:"fill_cardinality_satisfied"`
	BaseSubsetOptimal         bool           `json:"base_subset_optimal"`
	FilledSubsetOptimalClaim  bool           `json:"filled_subset_optimal_claim"`
	BasePatterns              int            `json:"base_patterns"`
	AddedPatterns             int            `json:"added_patterns"`
	FinalPatterns             int            `json:"final_patterns"`
	WorstAbsoluteUpperBound   *float64       `json:"worst_pool_absolute_regret_upper_bound,omitempty"`
	WorstNormalizedUpperBound *float64       `json:"worst_pool_normalized_regret_upper_bound,omitempty"`
}
type HybridFillResult struct {
	Policy                       string                 `json:"policy"`
	Config                       HybridFillConfig       `json:"config"`
	ConfigValueCanonicalJSON     string                 `json:"config_value_canonical_json"`
	ConfigValueSHA256            string                 `json:"config_value_sha256"`
	BaseConfigValueCanonicalJSON string                 `json:"base_config_value_canonical_json"`
	BaseConfigValueSHA256        string                 `json:"base_config_value_sha256"`
	ConfigHashScope              string                 `json:"config_hash_scope"`
	Complete                     bool                   `json:"complete"`
	Error                        string                 `json:"error,omitempty"`
	FailureStage                 string                 `json:"failure_stage,omitempty"`
	InputCandidates              int                    `json:"input_candidates"`
	InputPatterns                int                    `json:"input_patterns"`
	Base                         *ContinuousChoices     `json:"base"`
	FillRequired                 bool                   `json:"fill_required"`
	Preflight                    *HybridFillPreflight   `json:"fill_preflight,omitempty"`
	ChargedFillWork              int64                  `json:"charged_fill_work_upper_bound"`
	Finite20                     *Choice                `json:"finite20_control,omitempty"`
	Pattern                      *Selection             `json:"pattern_control,omitempty"`
	Selection                    *HybridFilledSelection `json:"selection,omitempty"`
	GuaranteeScope               string                 `json:"guarantee_scope"`
}

func (c HybridFillConfig) Validate() error {
	if c.Base.FiniteSafe != nil || c.Base.HybridFill != nil || c.Finite20.FiniteSafe != nil || c.Finite20.HybridFill != nil {
		return fmt.Errorf("hybrid fill recursive/mixed base or control is forbidden")
	}
	if err := c.Base.Validate(); err != nil {
		return err
	}
	if err := c.Finite20.Validate(); err != nil {
		return err
	}
	b := c.Base.Bounded
	if b == nil || c.Base.LeanDelivery || len(c.Base.Groups) != 0 || b.Objective != "absolute" || b.MinTransfer != 0 || b.MaxTransfer != 900 || b.MinWalk != 0 || b.MaxWalk != 4 || b.Exact || !b.ExactWhenFeasible || b.Nested || b.DistinctPatterns || len(b.Limits) != 1 || (b.Limits[0] != 3 && b.Limits[0] != 5) {
		return fmt.Errorf("hybrid fill requires the unchanged single-limit absolute resource-hybrid base")
	}
	if b.MaxWork != 50000000 || b.MaxCriticalPoints != 100000 || b.MaxMatrixBytes != 67108864 || c.Base.MaxSubsets != 200000 || c.Base.MaxCandidates != 1024 {
		return fmt.Errorf("hybrid fill base guard changed; no historical-base parity assumed")
	}
	f := c.Finite20
	if f.Bounded != nil || !f.LeanDelivery || f.DeliveryPolicy != "finite_preferences_v2" || len(f.DeliveryLimits) != 1 || f.DeliveryLimits[0] != b.Limits[0] || len(f.Groups) != 1 || len(f.Groups[0].Preferences) != 20 || f.MaxCandidates != c.Base.MaxCandidates || f.MaxSubsets != c.MaxFillSubsets {
		return fmt.Errorf("hybrid fill historical finite20 control differs")
	}
	pos := 0
	for _, t := range []int64{0, 150, 300, 600, 900} {
		for _, w := range []int64{0, 1, 2, 4} {
			p := f.Groups[0].Preferences[pos]
			if p.ID != fmt.Sprintf("t%d_w%d", t, w) || p.TransferSeconds != t || p.ExtraWalkWeight != w {
				return fmt.Errorf("hybrid fill historical20 changed")
			}
			pos++
		}
	}
	if c.MaxFillSubsets < 1 || c.MaxFillSubsets > 200000 || c.MaxFillWork < 1 || c.MaxFillWork > 20000000 || c.MaxFillMatrixBytes < 1 || c.MaxFillMatrixBytes > 8388608 {
		return fmt.Errorf("hybrid fill resource bounds outside preregistration")
	}
	return nil
}
func hybridFillChoose(n, k int) int64 {
	if k < 0 || k > n {
		return 0
	}
	if k > n-k {
		k = n - k
	}
	v := int64(1)
	for i := 1; i <= k; i++ {
		v = v * int64(n-k+i) / int64(i)
	}
	return v
}
func hybridFillResource(n, k int, c HybridFillConfig) HybridFillPreflight {
	var states, weighted int64
	for s := 1; s <= min(n, k); s++ {
		count := hybridFillChoose(n, s)
		states += count
		weighted += count * int64(s)
	}
	// n<=1024,k<=5 keeps these conservative integer bounds below int64 overflow.
	work := 7*int64(n*n) + 20*int64(n) + states*int64(n*(k+1)*(k+1)) + 20*weighted + 4*int64(n)
	matrix := 8 * 20 * int64(n)
	return HybridFillPreflight{CandidateCount: n, Limit: k, StatesUpperBound: states, WorkUpperBound: work, MatrixBytesUpperBound: matrix, Feasible: states <= int64(c.MaxFillSubsets) && work <= c.MaxFillWork && matrix <= c.MaxFillMatrixBytes}
}
func hybridFillCanonical(v any) (string, string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", "", err
	}
	var value any
	if err = json.Unmarshal(raw, &value); err != nil {
		return "", "", err
	}
	raw, err = json.Marshal(value)
	if err != nil {
		return "", "", err
	}
	hash := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(hash[:]), nil
}

// SelectHybridFill measures all base/control/fill work in the caller's request.
// Failure never silently publishes an otherwise complete base as a filled answer.
func SelectHybridFill(ctx context.Context, features []Feature, cfg HybridFillConfig) (out *HybridFillResult, err error) {
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	out = &HybridFillResult{Policy: "bounded_absolute_hybrid_fill_v1", Config: cfg, InputCandidates: len(features), ConfigHashScope: "SHA256 of recursively key-sorted Go JSON value; emitted canonical strings are independently normalizable to requested config, not raw config-file hashes", GuaranteeScope: "completed filled set contains every unchanged hybrid base identity and at most requested3/5 native candidates; therefore min affine score cannot worsen at any weight versus its own base; no finite20 dominance, authentic utility, reliability, new filled-set exact maximum, global journey, or latency claim"}
	defer func() {
		if err != nil {
			out.Error = err.Error()
			out.Complete = false
			out.Selection = nil
		}
	}()
	out.FailureStage = "config_hash"
	out.ConfigValueCanonicalJSON, out.ConfigValueSHA256, err = hybridFillCanonical(cfg)
	if err != nil {
		return
	}
	out.BaseConfigValueCanonicalJSON, out.BaseConfigValueSHA256, err = hybridFillCanonical(cfg.Base)
	if err != nil {
		return
	}
	out.FailureStage = "unchanged_hybrid_base"
	out.Base, err = SelectBounded(ctx, features, cfg.Base)
	if err != nil {
		return
	}
	if out.Base == nil || !out.Base.Complete || len(out.Base.Selections) != 1 || !out.Base.Selections[0].Complete {
		err = fmt.Errorf("hybrid fill base did not complete exactly requested selection")
		return
	}
	base := out.Base.Selections[0]
	k := cfg.Base.Bounded.Limits[0]
	if base.Limit != k {
		err = fmt.Errorf("hybrid fill base limit invariant")
		return
	}
	out.InputPatterns = out.Base.InputPatterns
	fs := append([]Feature{}, features...)
	by := map[string]Feature{}
	for _, f := range fs {
		by[f.Identity] = f
	}
	target := min(k, len(fs))
	ids := append([]string{}, base.Selected...)
	seen := map[string]bool{}
	basePatterns := map[string]bool{}
	for _, id := range ids {
		f, ok := by[id]
		if !ok || seen[id] {
			err = fmt.Errorf("hybrid fill base membership/uniqueness invariant")
			return
		}
		seen[id] = true
		basePatterns[f.Pattern] = true
	}
	if len(ids) > target {
		err = fmt.Errorf("hybrid fill base exceeds requested cardinality")
		return
	}
	selection := &HybridFilledSelection{Limit: k, BaseSelected: append([]string{}, base.Selected...), Added: []FillAddition{}, Selected: ids, RequestedCardinality: target, BaseSubsetOptimal: base.SubsetOptimal, FilledSubsetOptimalClaim: false, BasePatterns: len(basePatterns), WorstAbsoluteUpperBound: base.WorstAbsolute, WorstNormalizedUpperBound: base.WorstNormalized}
	out.FillRequired = len(ids) < target
	if out.FillRequired {
		out.FailureStage = "fill_resource_preflight"
		p := hybridFillResource(len(fs), k, cfg)
		out.Preflight = &p
		if !p.Feasible {
			err = fmt.Errorf("hybrid_fill_resource_preflight_infeasible: %w", ErrSelectionBudget)
			return
		}
		out.ChargedFillWork = p.WorkUpperBound
		if err = ctx.Err(); err != nil {
			return
		}
		out.FailureStage = "finite20_control"
		excluded := map[string]string{}
		pool := choiceRepresentatives(fs, excluded)
		prefs := cfg.Finite20.Groups[0].Preferences
		var chosen []int
		var states int
		chosen, states, err = minimax(ctx, pool, prefs, k, cfg.MaxFillSubsets)
		if err != nil {
			return
		}
		f := describeChoice(pool, prefs, chosen, "preference_minimax", k)
		f.Complete = true
		f.PoolOptimal = true
		f.EvaluatedSubsets = states
		out.Finite20 = &f
		if err = ctx.Err(); err != nil {
			return
		}
		out.FailureStage = "pattern_control"
		pattern := selectFeatures(fs, k, 300)
		out.Pattern = &pattern
		out.FailureStage = "deterministic_fill"
		sort.Slice(fs, func(i, j int) bool { return less(fs[i], fs[j]) })
		remaining := []string{}
		for _, f := range fs {
			remaining = append(remaining, f.Identity)
		}
		for _, stream := range []struct {
			source string
			ids    []string
		}{{"historical_finite20", f.Selected}, {"pattern_anchor_300", pattern.Selected}, {"default_feature_order", remaining}} {
			for _, id := range stream.ids {
				if err = ctx.Err(); err != nil {
					return
				}
				if len(selection.Selected) >= target {
					break
				}
				if seen[id] {
					continue
				}
				if _, ok := by[id]; !ok {
					err = fmt.Errorf("hybrid fill priority stream has foreign identity")
					return
				}
				seen[id] = true
				selection.Selected = append(selection.Selected, id)
				selection.Added = append(selection.Added, FillAddition{Identity: id, Source: stream.source})
			}
			if len(selection.Selected) >= target {
				break
			}
		}
	}
	out.FailureStage = "final_inclusion_check"
	if err = ctx.Err(); err != nil {
		return
	}
	if len(selection.Selected) != target {
		err = fmt.Errorf("hybrid fill did not satisfy requested native cardinality")
		return
	}
	finalPatterns := map[string]bool{}
	addedPatterns := map[string]bool{}
	for _, id := range selection.Selected {
		finalPatterns[by[id].Pattern] = true
	}
	for _, a := range selection.Added {
		addedPatterns[by[a.Identity].Pattern] = true
	}
	for i, id := range base.Selected {
		if selection.Selected[i] != id {
			err = fmt.Errorf("hybrid fill replaced or reordered a base identity")
			return
		}
	}
	selection.FinalPatterns = len(finalPatterns)
	selection.AddedPatterns = len(addedPatterns)
	selection.SetInclusionVerified = true
	selection.FillCardinalitySatisfied = true
	selection.Complete = true
	out.Selection = selection
	out.Complete = true
	out.FailureStage = ""
	return
}
