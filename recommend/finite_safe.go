package recommend

// Finite-safe selection is an explicitly requested supplied-pool policy.
// The historical20 profiles are constraints, never an off-grid evaluation target.
import (
	"context"
	"fmt"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
)

const finiteSafeMaxInt64 = int64(1<<63 - 1)

type FiniteSafeConfig struct {
	Baseline      ChoiceConfig  `json:"baseline"`
	Domain        BoundedConfig `json:"domain"`
	MaxCandidates int           `json:"max_candidates"`
	MaxStates     int           `json:"max_total_subset_states"`
}
type FiniteSafePreflight struct {
	CandidateCount           int   `json:"candidate_count"`
	CriticalPointsUpperBound int64 `json:"critical_points_upper_bound"`
	BaselineStatesUpperBound int64 `json:"baseline_states_upper_bound"`
	RepairStatesUpperBound   int64 `json:"repair_states_upper_bound"`
	MatrixBytesUpperBound    int64 `json:"matrix_bytes_upper_bound"`
	WorkUpperBound           int64 `json:"work_upper_bound"`
	Feasible                 bool  `json:"resource_feasible"`
}
type FiniteSafeConstraint struct {
	PreferenceID      string `json:"preference_id"`
	BaselineScore     int64  `json:"baseline_selected_score"`
	RepairScore       int64  `json:"repair_selected_score"`
	PointwiseNonworse bool   `json:"pointwise_nonworse"`
}
type FiniteSafeResult struct {
	Policy               string                 `json:"policy"`
	Config               FiniteSafeConfig       `json:"config"`
	Complete             bool                   `json:"complete"`
	Error                string                 `json:"error,omitempty"`
	FailureStage         string                 `json:"failure_stage,omitempty"`
	InputCandidates      int                    `json:"input_candidates"`
	InputPatterns        int                    `json:"input_patterns"`
	CandidateReduction   string                 `json:"candidate_reduction"`
	Preflight            FiniteSafePreflight    `json:"preflight"`
	Baseline             *Choice                `json:"baseline,omitempty"`
	BaselineComplete     bool                   `json:"baseline_complete"`
	BaselineExclusions   map[string]string      `json:"baseline_exclusions"`
	BaselineStates       int                    `json:"baseline_states"`
	RepairStates         int                    `json:"repair_states"`
	FeasibleRepairStates int                    `json:"feasible_repair_states"`
	ChargedWork          int64                  `json:"charged_work_including_conservative_baseline_and_branch_allowance"`
	CriticalPoints       int                    `json:"critical_points"`
	Selection            *ContinuousSelection   `json:"selection,omitempty"`
	Constraints          []FiniteSafeConstraint `json:"pointwise_constraints,omitempty"`
	GuaranteeScope       string                 `json:"guarantee_scope"`
}

func (c FiniteSafeConfig) Validate() error {
	if nestedDeliveryConfig(c.Baseline) {
		return fmt.Errorf("nested finite20-safe baseline is forbidden")
	}
	if err := c.Baseline.Validate(); err != nil {
		return err
	}
	if err := c.Domain.Validate(); err != nil {
		return err
	}
	if c.MaxCandidates < 1 || c.MaxCandidates > 64 || c.MaxStates < 1 || c.MaxStates > 200000 {
		return fmt.Errorf("finite-safe V1 candidate/state guard outside preregistered bounds")
	}
	if c.Domain.MinTransfer != 0 || c.Domain.MaxTransfer != 900 || c.Domain.MinWalk != 0 || c.Domain.MaxWalk != 4 || c.Domain.MaxCriticalPoints > 100000 || c.Domain.MaxWork > 50000000 || c.Domain.MaxMatrixBytes != 67108864 {
		return fmt.Errorf("finite-safe V1 domain/resource contract differs")
	}
	if c.Domain.DistinctPatterns || c.Domain.Nested || c.Domain.ExactWhenFeasible || !c.Domain.Exact || len(c.Domain.Limits) != 1 || c.Domain.Limits[0] != 3 {
		return fmt.Errorf("finite-safe V1 supports exact unconstrained subsets of at most 3 only")
	}
	if c.Baseline.Bounded != nil || !c.Baseline.LeanDelivery || c.Baseline.DeliveryPolicy != "finite_preferences_v2" || len(c.Baseline.DeliveryLimits) != 1 || c.Baseline.DeliveryLimits[0] != 3 || len(c.Baseline.Groups) != 1 || len(c.Baseline.Groups[0].Preferences) != 20 {
		return fmt.Errorf("finite-safe V1 requires historical20 finite lean limit-3 baseline")
	}
	// Require the exact preexisting order, identifiers, and integer coefficients.
	// The group description does not participate in scoring.
	pos := 0
	for _, t := range []int64{0, 150, 300, 600, 900} {
		for _, w := range []int64{0, 1, 2, 4} {
			p := c.Baseline.Groups[0].Preferences[pos]
			if p.ID != fmt.Sprintf("t%d_w%d", t, w) || p.TransferSeconds != t || p.ExtraWalkWeight != w {
				return fmt.Errorf("historical20 preference changed at index %d", pos)
			}
			pos++
		}
	}
	return nil
}
func finiteSafeChoose(n, k int) int64 {
	if k < 0 || k > n {
		return 0
	}
	if k > n-k {
		k = n - k
	}
	out := int64(1)
	// V1 n<=64,k<=3 makes intermediate arithmetic safely bounded in int64.
	for i := 1; i <= k; i++ {
		out = out * int64(n-k+i) / int64(i)
	}
	return out
}
func finiteSafeResource(n int, c FiniteSafeConfig) (p FiniteSafePreflight, baselineWork, repairBranches int64) {
	p.CandidateCount = n
	if n == 0 {
		p.Feasible = true
		return
	}
	k := min(3, n)
	pairs := finiteSafeChoose(n, 2)
	triples := finiteSafeChoose(n, 3)
	p.CriticalPointsUpperBound = 4 + 4*pairs + triples
	var states, weightedStates int64
	for s := 1; s <= k; s++ {
		count := finiteSafeChoose(n, s)
		states += count
		weightedStates += count * int64(s)
	}
	p.BaselineStatesUpperBound = states
	p.RepairStatesUpperBound = states
	// Include both continuous float64 matrices plus the int64 historical20 matrix.
	p.MatrixBytesUpperBound = 16*int64(n)*p.CriticalPointsUpperBound + 8*20*int64(n)
	branch := states * int64(n*(k+1)*(k+1))
	repairBranches = branch
	// Conservative baseline reduction, matrix, recursion and score comparisons;
	// it deliberately does not exploit representatives or early zero stopping.
	baselineWork = 3*int64(n*n) + 20*int64(n) + branch + 20*weightedStates
	geometry := 4 + 4*pairs + triples + 2*int64(n)*p.CriticalPointsUpperBound
	repair := branch + 20*int64(n) + 20*weightedStates + weightedStates*p.CriticalPointsUpperBound
	p.WorkUpperBound = baselineWork + geometry + repair
	p.Feasible = int64(n) <= int64(c.MaxCandidates) && n <= c.Baseline.MaxCandidates && 2*states <= int64(c.MaxStates) && states <= int64(c.Baseline.MaxSubsets) && p.CriticalPointsUpperBound <= int64(c.Domain.MaxCriticalPoints) && p.MatrixBytesUpperBound <= c.Domain.MaxMatrixBytes && p.WorkUpperBound <= c.Domain.MaxWork
	return
}

// SelectFiniteSafe consumes already-validated unique features. It does not route,
// recover, authenticate utility, or establish complete journey discovery.
// No caller should serve Baseline as an implicit fallback when Complete is false.
func SelectFiniteSafe(ctx context.Context, features []Feature, cfg FiniteSafeConfig) (out *FiniteSafeResult, err error) {
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	out = &FiniteSafeResult{Policy: "finite20_safe_" + cfg.Domain.Objective + "_exact_v1", Config: cfg, InputCandidates: len(features), CandidateReduction: "none: all original identities and patterns retained for repair; historical baseline reduction reported separately", BaselineExclusions: map[string]string{}, Constraints: []FiniteSafeConstraint{}, GuaranteeScope: "pointwise nonworse than exact historical20 selected scores; exact <=3 subset search on supplied validated identities and declared rectangle under float64 1e-8 score-s/1e-12 normalized comparisons; not optimization of external evaluation targets, journey completeness, authentic utility, an outside-domain guarantee, or serving latency qualification"}
	e := &boundedEngine{ctx: ctx, cfg: cfg.Domain, maxSubsets: cfg.MaxStates}
	defer func() {
		out.ChargedWork = e.work
		if err != nil {
			out.Error = err.Error()
			out.Complete = false
			out.Selection = nil
			out.Constraints = nil
		}
	}()
	out.FailureStage = "input_validation"
	if err = ctx.Err(); err != nil {
		return
	}
	if len(features) > cfg.MaxCandidates {
		out.FailureStage = "resource_preflight"
		out.Preflight.CandidateCount = len(features)
		err = fmt.Errorf("resource_preflight_infeasible: %w", ErrSelectionBudget)
		return
	}
	seen := map[string]bool{}
	patterns := map[string]bool{}
	for _, f := range features {
		if err = ctx.Err(); err != nil {
			return
		}
		if f.Identity == "" || seen[f.Identity] {
			err = fmt.Errorf("invalid duplicate/empty candidate identity")
			return
		}
		seen[f.Identity] = true
		patterns[f.Pattern] = true
		if f.Duration < 0 || f.Duration > model.MaxServiceSecond || f.Metrics.Transfers < 0 || f.Metrics.Transfers > 1024 || f.Metrics.WalkingSeconds < 0 || f.Metrics.WalkingSeconds > model.MaxServiceSecond {
			err = fmt.Errorf("out-of-contract score feature")
			return
		}
	}
	out.InputPatterns = len(patterns)
	out.FailureStage = "resource_preflight"
	var baselineWork, repairBranches int64
	out.Preflight, baselineWork, repairBranches = finiteSafeResource(len(features), cfg)
	if !out.Preflight.Feasible {
		err = fmt.Errorf("resource_preflight_infeasible: %w", ErrSelectionBudget)
		return
	}
	fs := append([]Feature{}, features...)
	sort.Slice(fs, func(i, j int) bool { return fs[i].Identity < fs[j].Identity })
	e.fs = fs
	if len(fs) == 0 {
		baseline := describeChoice(fs, cfg.Baseline.Groups[0].Preferences, []int{}, "preference_minimax", 3)
		baseline.Complete = true
		baseline.PoolOptimal = true
		out.Baseline = &baseline
		out.BaselineComplete = true
		out.Selection = &ContinuousSelection{Limit: 3, Selected: []string{}, Method: "finite20_safe_empty_pool", Complete: true, SubsetOptimal: true}
		out.Complete = true
		out.FailureStage = ""
		return
	}
	// Baseline/branch work is conservatively charged in advance, not guessed after
	// a guard hit. Preflight bounds every possible baseline and repair subset.
	if err = e.charge(baselineWork + repairBranches); err != nil {
		return
	}
	prefs := cfg.Baseline.Groups[0].Preferences
	out.FailureStage = "historical_baseline"
	reps := choiceRepresentatives(fs, out.BaselineExclusions)
	var baseIDs []int
	baseIDs, out.BaselineStates, err = minimax(ctx, reps, prefs, 3, cfg.Baseline.MaxSubsets)
	if err != nil {
		return
	}
	baseline := describeChoice(reps, prefs, baseIDs, "preference_minimax", 3)
	baseline.Complete = true
	baseline.PoolOptimal = true
	baseline.EvaluatedSubsets = out.BaselineStates
	out.Baseline = &baseline
	out.BaselineComplete = true
	if err = e.charge(20 * int64(len(fs))); err != nil {
		return
	}
	scores := make([][]int64, len(fs))
	floor := make([]int64, len(prefs))
	for p := range floor {
		floor[p] = finiteSafeMaxInt64
	}
	for _, i := range baseIDs {
		for p, pref := range prefs {
			floor[p] = min(floor[p], preferenceCost(reps[i], pref))
		}
	}
	for i, f := range fs {
		if err = ctx.Err(); err != nil {
			return
		}
		scores[i] = make([]int64, len(prefs))
		for p, pref := range prefs {
			scores[i][p] = preferenceCost(f, pref)
		}
	}
	out.FailureStage = "critical_geometry"
	if err = e.critical(); err != nil {
		return
	}
	out.CriticalPoints = len(e.points)
	var best []int
	var bestValue boundedValue
	chosen := []int{}
	out.FailureStage = "exact_repair"
	for size := 1; size <= min(3, len(fs)); size++ {
		var visit func(int) error
		visit = func(start int) error {
			if z := ctx.Err(); z != nil {
				return z
			}
			if len(chosen) == size {
				if out.BaselineStates+out.RepairStates >= cfg.MaxStates {
					return ErrSelectionBudget
				}
				out.RepairStates++
				if z := e.charge(int64(size * len(prefs))); z != nil {
					return z
				}
				for p := range prefs {
					got := int64(finiteSafeMaxInt64)
					for _, i := range chosen {
						got = min(got, scores[i][p])
					}
					if got > floor[p] {
						return nil
					}
				}
				out.FeasibleRepairStates++
				value, z := e.evaluate(chosen)
				if z != nil {
					return z
				}
				if best == nil || e.better(value, bestValue) {
					best = append([]int{}, chosen...)
					bestValue = value
				}
				return nil
			}
			for i := start; i <= len(fs)-(size-len(chosen)); i++ {
				chosen = append(chosen, i)
				if z := visit(i + 1); z != nil {
					return z
				}
				chosen = chosen[:len(chosen)-1]
			}
			return nil
		}
		if err = visit(0); err != nil {
			return
		}
	}
	if best == nil {
		err = fmt.Errorf("finite-safe invariant: historical baseline is feasible but no repair subset found")
		return
	}
	s := &ContinuousSelection{Limit: 3, Selected: []string{}, Method: "finite20_safe_exhaustive_subset", Complete: true, SubsetOptimal: true, GlobalOptimalityClaim: false, WorstAbsolute: &bestValue.a, WorstNormalized: &bestValue.r, EvaluatedSubsets: out.RepairStates}
	aw, rw := e.points[bestValue.ai], e.points[bestValue.ri]
	s.AbsoluteWitness = &aw
	s.NormalizedWitness = &rw
	ps := map[string]bool{}
	for _, i := range best {
		s.Selected = append(s.Selected, fs[i].Identity)
		ps[fs[i].Pattern] = true
	}
	s.DistinctPatternCount = len(ps)
	for p, pref := range prefs {
		got := int64(finiteSafeMaxInt64)
		for _, i := range best {
			got = min(got, scores[i][p])
		}
		if got > floor[p] {
			err = fmt.Errorf("finite-safe invariant: completed subset violates historical profile")
			return
		}
		out.Constraints = append(out.Constraints, FiniteSafeConstraint{PreferenceID: pref.ID, BaselineScore: floor[p], RepairScore: got, PointwiseNonworse: true})
	}
	if err = ctx.Err(); err != nil {
		return
	}
	out.Selection = s
	out.Complete = true
	out.FailureStage = ""
	return
}
