package recommend

// This selector has no training/evaluation preference grid. Its specification is
// a bounded two-dimensional rectangle of hypothetical nonnegative weights.
import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
)

type BoundedConfig struct {
	MinTransfer       float64 `json:"min_transfer_seconds"`
	MaxTransfer       float64 `json:"max_transfer_seconds"`
	MinWalk           float64 `json:"min_extra_walk_weight"`
	MaxWalk           float64 `json:"max_extra_walk_weight"`
	Objective         string  `json:"objective"`
	Limits            []int   `json:"limits"`
	DistinctPatterns  bool    `json:"distinct_patterns"`
	Nested            bool    `json:"nested"`
	MaxCriticalPoints int     `json:"max_critical_points"`
	MaxWork           int64   `json:"max_work"`
	MaxMatrixBytes    int64   `json:"max_matrix_bytes,omitempty"`
	ExactWhenFeasible bool    `json:"exact_when_feasible,omitempty"`
	Exact             bool    `json:"exact_subset_search"`
}

func (c BoundedConfig) Validate() error {
	if math.IsNaN(c.MinTransfer) || math.IsNaN(c.MaxTransfer) || math.IsNaN(c.MinWalk) || math.IsNaN(c.MaxWalk) || c.MinTransfer < 0 || c.MaxTransfer < c.MinTransfer || c.MaxTransfer > float64(model.MaxServiceSecond) || c.MinWalk < 0 || c.MaxWalk < c.MinWalk || c.MaxWalk > 1000 {
		return fmt.Errorf("invalid continuous weight rectangle")
	}
	if c.Objective != "absolute" && c.Objective != "normalized" {
		return fmt.Errorf("bounded objective must be absolute or normalized")
	}
	if len(c.Limits) < 1 || len(c.Limits) > 4 || c.MaxCriticalPoints < 4 || c.MaxCriticalPoints > 1000000 || c.MaxWork < 1 || c.MaxWork > 10000000000 || c.MaxMatrixBytes < 0 || c.MaxMatrixBytes > 268435456 {
		return fmt.Errorf("invalid continuous compute bounds")
	}
	if c.Exact && c.ExactWhenFeasible {
		return fmt.Errorf("exact and auto-exact subset modes are mutually exclusive")
	}
	prev := 0
	for _, k := range c.Limits {
		if k < 1 || k > 10 || k <= prev {
			return fmt.Errorf("limits must be increasing in [1,10]")
		}
		prev = k
	}
	return nil
}

type WeightPoint struct {
	Transfer float64 `json:"transfer_seconds"`
	Walk     float64 `json:"extra_walk_weight"`
}
type ContinuousSelection struct {
	PredictedExactWork    int64        `json:"predicted_exact_total_work_upper_bound,omitempty"`
	PredictedExactSubsets int64        `json:"predicted_exact_additional_subsets_upper_bound,omitempty"`
	ExactResourceFeasible bool         `json:"exact_resource_feasible,omitempty"`
	ConditionalOnPrevious bool         `json:"conditional_on_previous_requested_selection"`
	Limit                 int          `json:"limit"`
	Selected              []string     `json:"selected"`
	Method                string       `json:"method"`
	Complete              bool         `json:"complete"`
	SubsetOptimal         bool         `json:"subset_optimal"`
	GlobalOptimalityClaim bool         `json:"global_optimality_claim"`
	WorstAbsolute         *float64     `json:"worst_pool_absolute_regret"`
	WorstNormalized       *float64     `json:"worst_pool_normalized_regret"`
	AbsoluteWitness       *WeightPoint `json:"absolute_worst_weight"`
	NormalizedWitness     *WeightPoint `json:"normalized_worst_weight"`
	DistinctPatternCount  int          `json:"distinct_pattern_count"`
	EvaluatedSubsets      int          `json:"evaluated_subsets"`
}
type ContinuousChoices struct {
	NormalizedDenominator string                `json:"normalized_denominator"`
	Policy                string                `json:"policy"`
	Config                BoundedConfig         `json:"config"`
	Complete              bool                  `json:"complete"`
	Error                 string                `json:"error,omitempty"`
	GuaranteeScope        string                `json:"guarantee_scope"`
	InputCandidates       int                   `json:"input_candidates"`
	InputPatterns         int                   `json:"input_patterns"`
	RetainedCandidates    int                   `json:"retained_candidates"`
	Exclusions            map[string]string     `json:"exclusions"`
	CriticalPoints        int                   `json:"critical_points"`
	Work                  int64                 `json:"work"`
	Selections            []ContinuousSelection `json:"selections"`
}

// EvaluateBounded is the lean delivery path. It does not compute recovery search,
// the legacy envelope controls, finite20 choices, or an unrequested k5 output.
// Every original validated identity/pattern remains in Features for diagnostics.
func (x *Index) EvaluateBounded(ctx context.Context, q model.Query, js []model.Journey, cfg ChoiceConfig) (out Result, err error) {
	if err = cfg.Validate(); err != nil {
		return
	}
	if cfg.Bounded == nil {
		return out, fmt.Errorf("missing bounded config")
	}
	out = Result{Policy: "bounded_continuous_regret_v3", FeatureMode: "validated_objective_pattern_slack_no_recovery", Features: []Feature{}, Selections: []Selection{}}
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
	out.ContinuousChoices, err = SelectBounded(ctx, out.Features, cfg)
	return
}

type boundedEngine struct {
	ctx        context.Context
	cfg        BoundedConfig
	fs         []Feature
	points     []WeightPoint
	abs, rel   [][]float64
	work       int64
	subsets    int
	maxSubsets int
}

func (e *boundedEngine) charge(n int64) error {
	e.work += n
	if e.work > e.cfg.MaxWork {
		return ErrSelectionBudget
	}
	return e.ctx.Err()
}
func vector(f Feature) [3]int64 {
	return [3]int64{f.Duration, int64(f.Metrics.Transfers), f.Metrics.WalkingSeconds}
}
func (e *boundedEngine) boundedPool(fs []Feature, distinct bool, excluded map[string]string) ([]Feature, error) {
	out := []Feature{}
	for _, f := range fs {
		v := vector(f)
		duplicate := false
		for _, o := range out {
			if err := e.charge(1); err != nil {
				return nil, err
			}
			if vector(o) == v && (!distinct || o.Pattern == f.Pattern) {
				duplicate = true
				break
			}
		}
		if duplicate {
			excluded[f.Identity] = "equal_score_vector_within_constraint"
			continue
		}
		dominated := false
		if !distinct {
			for _, o := range fs {
				if err := e.charge(1); err != nil {
					return nil, err
				}
				a := vector(o)
				if a != v && a[0] <= v[0] && a[1] <= v[1] && a[2] <= v[2] {
					dominated = true
					break
				}
			}
		}
		if dominated {
			excluded[f.Identity] = "monotone_vector_dominated_score_only"
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// critical constructs every possible vertex of a selected affine lower-envelope
// cell: rectangle corners, two-plane equality at a rectangle edge, and three-plane
// equality in its interior. On a cell, additive regret is a max of affine maps;
// normalized regret/(1+pool best score) is a max of positive-denominator
// linear-fractional maps (one-score-second smoothing avoids zero singularity).
// Each reaches its maximum at a cell vertex. The set is valid for EVERY subset,
// not only the complete-pool lower envelope. A corners-only set is not sufficient.
func (e *boundedEngine) critical() error {
	c := e.cfg
	seen := map[[2]uint64]bool{}
	add := func(t, w float64) error {
		if math.IsNaN(t) || math.IsNaN(w) || math.IsInf(t, 0) || math.IsInf(w, 0) || t < c.MinTransfer-1e-8 || t > c.MaxTransfer+1e-8 || w < c.MinWalk-1e-8 || w > c.MaxWalk+1e-8 {
			return nil
		}
		t = math.Max(c.MinTransfer, math.Min(c.MaxTransfer, t))
		w = math.Max(c.MinWalk, math.Min(c.MaxWalk, w))
		key := [2]uint64{math.Float64bits(t), math.Float64bits(w)}
		if !seen[key] {
			if len(e.points) >= c.MaxCriticalPoints {
				return ErrSelectionBudget
			}
			seen[key] = true
			e.points = append(e.points, WeightPoint{t, w})
		}
		return nil
	}
	for _, t := range []float64{c.MinTransfer, c.MaxTransfer} {
		for _, w := range []float64{c.MinWalk, c.MaxWalk} {
			if err := add(t, w); err != nil {
				return err
			}
		}
	}
	for i, a := range e.fs {
		for j := i + 1; j < len(e.fs); j++ {
			if err := e.charge(1); err != nil {
				return err
			}
			b := e.fs[j]
			da := float64(a.Duration - b.Duration)
			dt := float64(a.Metrics.Transfers - b.Metrics.Transfers)
			dw := float64(a.Metrics.WalkingSeconds - b.Metrics.WalkingSeconds)
			if dw != 0 {
				for _, t := range []float64{c.MinTransfer, c.MaxTransfer} {
					if err := add(t, (-da-dt*t)/dw); err != nil {
						return err
					}
				}
			}
			if dt != 0 {
				for _, w := range []float64{c.MinWalk, c.MaxWalk} {
					if err := add((-da-dw*w)/dt, w); err != nil {
						return err
					}
				}
			}
			for k := j + 1; k < len(e.fs); k++ {
				if err := e.charge(1); err != nil {
					return err
				}
				z := e.fs[k]
				ea := float64(a.Duration - z.Duration)
				et := float64(a.Metrics.Transfers - z.Metrics.Transfers)
				ew := float64(a.Metrics.WalkingSeconds - z.Metrics.WalkingSeconds)
				det := dt*ew - et*dw
				if det == 0 {
					continue
				}
				if err := add((-da*ew+ea*dw)/det, (-dt*ea+et*da)/det); err != nil {
					return err
				}
			}
		}
	}
	matrixLimit := e.cfg.MaxMatrixBytes
	if matrixLimit == 0 {
		matrixLimit = 64 * 1024 * 1024
	}
	elements := int64(len(e.fs)) * int64(len(e.points))
	if elements > matrixLimit/16 || elements > e.cfg.MaxWork-e.work {
		return ErrSelectionBudget
	}
	e.abs = make([][]float64, len(e.fs))
	e.rel = make([][]float64, len(e.fs))
	for i := range e.fs {
		if err := e.ctx.Err(); err != nil {
			return err
		}
		e.abs[i] = make([]float64, len(e.points))
		e.rel[i] = make([]float64, len(e.points))
	}
	for p, w := range e.points {
		if err := e.charge(int64(len(e.fs))); err != nil {
			return err
		}
		opt := math.Inf(1)
		for _, f := range e.fs {
			opt = math.Min(opt, float64(f.Duration)+w.Transfer*float64(f.Metrics.Transfers)+w.Walk*float64(f.Metrics.WalkingSeconds))
		}
		for i, f := range e.fs {
			reg := math.Max(0, float64(f.Duration)+w.Transfer*float64(f.Metrics.Transfers)+w.Walk*float64(f.Metrics.WalkingSeconds)-opt)
			e.abs[i][p] = reg
			e.rel[i][p] = reg / (1 + opt)
		}
	}
	return nil
}

type boundedValue struct {
	a, r   float64
	ai, ri int
}

func (e *boundedEngine) evaluate(s []int) (v boundedValue, err error) {
	if e.subsets >= e.maxSubsets {
		return v, ErrSelectionBudget
	}
	e.subsets++
	if err = e.charge(int64(len(s) * len(e.points))); err != nil {
		return
	}
	for p := range e.points {
		a, r := math.Inf(1), math.Inf(1)
		for _, i := range s {
			a = math.Min(a, e.abs[i][p])
			r = math.Min(r, e.rel[i][p])
		}
		if a > v.a {
			v.a = a
			v.ai = p
		}
		if r > v.r {
			v.r = r
			v.ri = p
		}
	}
	return
}
func (e *boundedEngine) better(a, b boundedValue) bool {
	if e.cfg.Objective == "normalized" {
		if math.Abs(a.r-b.r) > 1e-12 {
			return a.r < b.r
		}
		return a.a < b.a-1e-8
	}
	if math.Abs(a.a-b.a) > 1e-8 {
		return a.a < b.a
	}
	return a.r < b.r-1e-12
}
func (e *boundedEngine) allowed(s []int, i int) bool {
	for _, j := range s {
		if j == i || e.cfg.DistinctPatterns && e.fs[i].Pattern == e.fs[j].Pattern {
			return false
		}
	}
	return true
}
func (e *boundedEngine) greedy(k int, forced []int) ([]int, boundedValue, error) {
	s := append([]int{}, forced...)
	var v boundedValue
	for len(s) < k {
		pick := -1
		var best boundedValue
		for i := range e.fs {
			if !e.allowed(s, i) {
				continue
			}
			cur, err := e.evaluate(append(append([]int{}, s...), i))
			if err != nil {
				return nil, v, err
			}
			if pick < 0 || e.better(cur, best) {
				pick = i
				best = cur
			}
		}
		if pick < 0 {
			break
		}
		s = append(s, pick)
		v = best
	}
	if len(s) == len(forced) {
		var err error
		v, err = e.evaluate(s)
		if err != nil {
			return nil, v, err
		}
	}
	// Deterministic best-improvement swaps, with forced nested identities locked.
	for {
		var swap []int
		best := v
		for pos := len(forced); pos < len(s); pos++ {
			rest := append([]int{}, s[:pos]...)
			rest = append(rest, s[pos+1:]...)
			for i := range e.fs {
				if !e.allowed(rest, i) {
					continue
				}
				trial := append(append([]int{}, s...), 0)
				trial = trial[:len(s)]
				trial[pos] = i
				cur, err := e.evaluate(trial)
				if err != nil {
					return nil, v, err
				}
				if e.better(cur, best) {
					best = cur
					swap = trial
				}
			}
		}
		if swap == nil {
			break
		}
		s = swap
		v = best
	}
	return s, v, nil
}
func (e *boundedEngine) exact(k int, forced []int) ([]int, boundedValue, error) {
	var best []int
	var value boundedValue
	chosen := append([]int{}, forced...)
	for size := max(1, len(forced)); size <= k; size++ {
		var visit func(int) error
		visit = func(start int) error {
			if len(chosen) == size {
				v, err := e.evaluate(chosen)
				if err != nil {
					return err
				}
				if best == nil || e.better(v, value) {
					best = append([]int{}, chosen...)
					value = v
				}
				return nil
			}
			for i := start; i < len(e.fs); i++ {
				if !e.allowed(chosen, i) {
					continue
				}
				chosen = append(chosen, i)
				if err := visit(i + 1); err != nil {
					return err
				}
				chosen = chosen[:len(chosen)-1]
			}
			return nil
		}
		if err := visit(0); err != nil {
			return nil, value, err
		}
	}
	return best, value, nil
}

// exactResourceBound predicts before beginning exhaustive search. It includes
// all already charged pool/geometry/matrix work plus every size1..k subset's
// score comparisons, with conservative additional branch-comparison allowance.
// Pattern constraints can only reduce this bound. It never uses quality outcomes
// or preference-grid results, and is not a fallback after a guard is exhausted.
func (e *boundedEngine) exactResourceBound(k int, forced []int) (work, states int64) {
	work = e.work
	n := len(e.fs) - len(forced)
	cap := int64(e.maxSubsets) + 1
	choose := func(n, k int) int64 {
		if k < 0 || k > n {
			return 0
		}
		if k > n-k {
			k = n - k
		}
		v := int64(1)
		for i := 1; i <= k; i++ {
			v = v * int64(n-k+i) / int64(i)
			if v > cap {
				return cap
			}
		}
		return v
	}
	for size := max(1, len(forced)); size <= k; size++ {
		count := choose(n, size-len(forced))
		states += count
		per := int64(size*len(e.points) + len(e.fs)*(size+1)*(size+1))
		work += count * per
		if work > e.cfg.MaxWork || states > int64(e.maxSubsets-e.subsets) {
			return work, states
		}
	}
	return work, states
}

// SelectBounded accepts already validated features for replay/offline evaluation.
// Its only exactness claim is supplied-pool score regret over the declared domain;
// IEEE-754 geometry is tested at tolerance, not a formal real-arithmetic proof.
func SelectBounded(ctx context.Context, fs []Feature, cfg ChoiceConfig) (out *ContinuousChoices, err error) {
	if err = cfg.Validate(); err != nil {
		return
	}
	if cfg.Bounded == nil {
		return nil, fmt.Errorf("missing bounded config")
	}
	c := *cfg.Bounded
	if c.MaxMatrixBytes == 0 {
		c.MaxMatrixBytes = 64 * 1024 * 1024
	}
	out = &ContinuousChoices{Policy: "bounded_continuous_regret_v3", NormalizedDenominator: "1 score-second + minimum supplied-pool score at this weight", Config: c, InputCandidates: len(fs), Exclusions: map[string]string{}, Selections: []ContinuousSelection{}, GuaranteeScope: "supplied validated pool, conditional on declared pattern/nesting constraints; continuous hypothetical rectangle only; float64 geometry tolerance 1e-8 score-s/1e-12 relative; not journey completeness or authentic rider utility"}
	e := &boundedEngine{ctx: ctx, cfg: c, maxSubsets: cfg.MaxSubsets}
	defer func() {
		out.Work = e.work
		if err != nil {
			out.Error = err.Error()
			out.Complete = false
			for i := range out.Selections {
				out.Selections[i].Complete = false
				out.Selections[i].SubsetOptimal = false
			}
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	if len(fs) > cfg.MaxCandidates {
		err = ErrSelectionBudget
		return
	}
	fs = append([]Feature{}, fs...)
	sort.Slice(fs, func(i, j int) bool { return fs[i].Identity < fs[j].Identity })
	pats := map[string]bool{}
	identities := map[string]bool{}
	for _, f := range fs {
		if f.Identity == "" || identities[f.Identity] {
			err = fmt.Errorf("invalid duplicate/empty candidate identity")
			return
		}
		identities[f.Identity] = true
		if f.Duration < 0 || f.Duration > model.MaxServiceSecond || f.Metrics.Transfers < 0 || f.Metrics.Transfers > 1024 || f.Metrics.WalkingSeconds < 0 || f.Metrics.WalkingSeconds > model.MaxServiceSecond {
			err = fmt.Errorf("out-of-contract score feature")
			return
		}
		pats[f.Pattern] = true
	}
	out.InputPatterns = len(pats)
	e.fs, err = e.boundedPool(fs, c.DistinctPatterns, out.Exclusions)
	if err != nil {
		return
	}
	out.RetainedCandidates = len(e.fs)
	if len(e.fs) == 0 {
		for _, k := range c.Limits {
			out.Selections = append(out.Selections, ContinuousSelection{Limit: k, Selected: []string{}, Method: "empty_pool", Complete: true, SubsetOptimal: true})
		}
		out.Complete = true
		return
	}
	if err = e.critical(); err != nil {
		return
	}
	out.CriticalPoints = len(e.points)
	forced := []int{}
	for _, limit := range c.Limits {
		k := min(limit, len(e.fs))
		if c.DistinctPatterns {
			k = min(k, len(pats))
		}
		before := e.subsets
		var selected []int
		var v boundedValue
		method := "greedy_best_swap_local"
		exact := false
		predictedWork, predictedStates := e.exactResourceBound(k, forced)
		feasible := predictedWork <= c.MaxWork && predictedStates <= int64(e.maxSubsets-e.subsets)
		if c.Exact || c.ExactWhenFeasible && feasible {
			selected, v, err = e.exact(k, forced)
			method = "exhaustive_subset"
			if c.ExactWhenFeasible {
				method = "exhaustive_subset_resource_feasible"
			}
			exact = true
		} else {
			selected, v, err = e.greedy(k, forced)
		}
		if err != nil {
			return
		}
		sort.Ints(selected)
		s := ContinuousSelection{PredictedExactWork: predictedWork, PredictedExactSubsets: predictedStates, ExactResourceFeasible: feasible, ConditionalOnPrevious: len(forced) > 0, Limit: limit, Selected: []string{}, Method: method, Complete: true, SubsetOptimal: exact, WorstAbsolute: &v.a, WorstNormalized: &v.r, EvaluatedSubsets: e.subsets - before}
		aw, rw := e.points[v.ai], e.points[v.ri]
		s.AbsoluteWitness = &aw
		s.NormalizedWitness = &rw
		ps := map[string]bool{}
		for _, i := range selected {
			s.Selected = append(s.Selected, e.fs[i].Identity)
			ps[e.fs[i].Pattern] = true
		}
		s.DistinctPatternCount = len(ps)
		out.Selections = append(out.Selections, s)
		if c.Nested {
			forced = append([]int{}, selected...)
		}
	}
	out.Complete = true
	return
}
