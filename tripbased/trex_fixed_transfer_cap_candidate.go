package tripbased

import (
	"context"
	"fmt"

	"github.com/ertyoii/transit-routing-go/internal/capverify"
	"github.com/ertyoii/transit-routing-go/model"
)

const TREXFixedTransferCapCandidateMethod = "fixed_transfer_cap_overlay_ranked_actual_probe_verified_candidate_v1"

type TREXFixedTransferCapCandidateInfo struct {
	Binding                 model.ResultBinding `json:"binding"`
	Method                  string              `json:"method"`
	Accepted                bool                `json:"accepted"`
	RankedRepresentatives   int                 `json:"ranked_representatives"`
	CheckedRepresentatives  int                 `json:"checked_representatives"`
	ReplayWork              int                 `json:"replay_work"`
	FixedTransferWalkingCap int64               `json:"fixed_transfer_walking_cap"`
	VerifierPolicy          string              `json:"verifier_policy"`
}

type TREXFixedTransferCapCandidateResult struct {
	TREXResult
	Certificate              TREXFixedTransferCapCandidateInfo `json:"candidate_certificate"`
	VerificationComplete     bool                              `json:"verification_complete"`
	VerificationStates       int                               `json:"verification_states"`
	VerificationProbes       int                               `json:"verification_probes"`
	DeclineReason            string                            `json:"decline_reason,omitempty"`
	VerifierStats            capverify.ProbeStats              `json:"verifier_stats"`
	ProjectionScans          int                               `json:"projection_scans"`
	ShadowLegacyScans        int64                             `json:"shadow_legacy_scans"`
	ShadowLegacyWouldDecline bool                              `json:"shadow_legacy_would_decline"`
	ShadowLegacyComplete     bool                              `json:"shadow_legacy_complete"`
	ShadowLegacySaturated    bool                              `json:"shadow_legacy_saturated"`
}

// TREXFixedTransferCapCandidate uses actual ranked overlays customized under
// the query's exact fixed transfer cap. The separately named actual-probe raw
// verifier supplies no returned journeys. Complete fresh frontier equality and
// every original-query witness replay remain mandatory; this is not a general
// rank-preservation theorem or a legacy logical-budget-equivalence claim.
type TREXFixedTransferCapCandidate struct {
	gate *TREXStaticCapCertificate
	raw  *capverify.Prepared
}

type TREXRawVerifierPreparationLimits = capverify.PreparationLimits
type TREXRawVerifierPreparationStats = capverify.PreparationStats

func PrepareTREXFixedTransferCapCandidate(ctx context.Context, x *TREXIndex, limits TREXStaticCapVerifyLimits, preparation TREXRawVerifierPreparationLimits) (*TREXFixedTransferCapCandidate, TREXRawVerifierPreparationStats, error) {
	var empty TREXRawVerifierPreparationStats
	if e := ctx.Err(); e != nil {
		return nil, empty, e
	}
	if x == nil || !x.Complete() || !x.staticRulesCandidate || x.transferWalkLimit == nil || x.snap.Profile != "trex_static_stop_pair_fixed_transfer_cap_ranked_candidate_v1" {
		return nil, empty, fmt.Errorf("fixed transfer cap certificate requires its complete ranked index")
	}
	if limits.MaxStates <= 0 || limits.MaxJourneys <= 0 || limits.MaxScans <= 0 {
		return nil, empty, fmt.Errorf("positive verification limits required")
	}
	cap, e := prepareTREXCapChecker(ctx, x)
	if e != nil {
		return nil, empty, e
	}
	raw, stats, e := capverify.PrepareRaw(ctx, x.base.p.Data, preparation)
	if e != nil {
		return nil, stats, e
	}
	return &TREXFixedTransferCapCandidate{gate: &TREXStaticCapCertificate{cap: cap, limits: limits}, raw: raw}, stats, nil
}

func (c *TREXFixedTransferCapCandidate) Search(ctx context.Context, q model.Query) (out TREXFixedTransferCapCandidateResult, err error) {
	out.Journeys = []model.Journey{}
	out.Certificate.Method = TREXFixedTransferCapCandidateMethod
	out.Certificate.VerifierPolicy = capverify.ActualProbePolicy
	fail := func(reason string, e error) (TREXFixedTransferCapCandidateResult, error) {
		out.Journeys = []model.Journey{}
		out.Complete = false
		out.Certificate.Accepted = false
		out.Error = e.Error()
		out.DeclineReason = reason
		return out, e
	}
	if e := ctx.Err(); e != nil {
		return fail("cancelled", e)
	}
	if c == nil || c.gate == nil || c.gate.cap == nil || c.raw == nil {
		return fail("unprepared", fmt.Errorf("T-REX fixed-transfer-cap candidate: unprepared"))
	}
	if q.Walking != nil {
		w := *q.Walking
		q.Walking = &w
	}
	checker, index, limits := c.gate.cap.checker, c.gate.cap.index, c.gate.limits
	out.Certificate.FixedTransferWalkingCap = *index.transferWalkLimit
	if q.Walking == nil || q.Walking.Transfer != *index.transferWalkLimit {
		return fail("fixed_transfer_cap_mismatch", fmt.Errorf("query transfer walking cap must equal prepared cap %d", *index.transferWalkLimit))
	}
	binding, e := checker.Binding(q)
	if e != nil {
		return fail("original_query_binding", e)
	}
	out.Certificate.Binding = binding
	core, e := index.searchTREXForCertificate(ctx, q, "overlay-role-caps", true)
	out.TREXResult = core
	out.Certificate.RankedRepresentatives = len(core.Journeys)
	if e != nil {
		return fail("ranked_search_or_original_resource_guard", e)
	}
	if !core.Complete {
		return fail("ranked_search_incomplete", fmt.Errorf("T-REX fixed-transfer-cap candidate: ranked search incomplete"))
	}
	for _, journey := range core.Journeys {
		for i := 0; i <= len(journey.Legs); i++ {
			if e := ctx.Err(); e != nil {
				return fail("cancelled", e)
			}
			if out.Stats.Work == index.cfg.Limits.MaxQueryWork {
				return fail("replay_work_incomplete", fmt.Errorf("T-REX fixed-transfer-cap candidate: replay work budget exhausted"))
			}
			out.Stats.Work++
			out.Certificate.ReplayWork++
		}
		out.Certificate.CheckedRepresentatives++
		if _, e := checker.Check(q, journey); e != nil {
			return fail("ranked_candidate_infeasible", fmt.Errorf("T-REX fixed-transfer-cap candidate fails original query: %w", e))
		}
	}
	verified, e := c.raw.Enumerate(ctx, q, capverify.Limits{MaxStates: limits.MaxStates, MaxJourneys: limits.MaxJourneys, MaxScans: limits.MaxScans})
	out.VerificationStates, out.VerificationProbes = verified.States, verified.Scans
	out.VerifierStats = verified.ProbeStats
	out.ShadowLegacyScans = verified.ProbeStats.LegacyScans
	out.ShadowLegacySaturated = verified.ProbeStats.LegacySaturated
	out.ShadowLegacyWouldDecline = verified.ProbeStats.LegacyWouldDecline
	if e != nil || !verified.Complete {
		if e == nil {
			e = fmt.Errorf("T-REX fixed-transfer-cap candidate: independent verification incomplete")
		}
		return fail("verification_incomplete", e)
	}
	out.VerificationComplete = true
	a, work, e := capverify.Frontier(ctx, index.base.p.Data, q, core.Journeys, limits.MaxScans-out.VerificationProbes)
	out.VerificationProbes += work
	out.ProjectionScans += work
	out.addProjectionShadow(work, limits.MaxScans, false)
	if e != nil {
		return fail("ranked_frontier_projection", e)
	}
	b, work, e := capverify.Frontier(ctx, index.base.p.Data, q, verified.Journeys, limits.MaxScans-out.VerificationProbes)
	out.VerificationProbes += work
	out.ProjectionScans += work
	out.addProjectionShadow(work, limits.MaxScans, e == nil)
	if e != nil {
		return fail("reference_frontier_projection", e)
	}
	if len(a) != len(b) {
		return fail("frontier_mismatch", fmt.Errorf("T-REX fixed-transfer-cap candidate: ranked/reference frontier mismatch"))
	}
	for i := range a {
		if a[i] != b[i] {
			return fail("frontier_mismatch", fmt.Errorf("T-REX fixed-transfer-cap candidate: ranked/reference frontier mismatch"))
		}
	}
	if e := ctx.Err(); e != nil {
		return fail("cancelled", e)
	}
	out.Certificate.Accepted = true
	return out, nil
}

// addProjectionShadow changes only diagnostic legacy accounting. Its complete
// scope is fresh enumeration plus both successful independent projections.
func (out *TREXFixedTransferCapCandidateResult) addProjectionShadow(work, limit int, scopeComplete bool) {
	shadow := capverify.ProbeStats{LegacyScans: out.ShadowLegacyScans, LegacySaturated: out.ShadowLegacySaturated}
	shadow.AddProjectionScans(work)
	out.ShadowLegacyScans = shadow.LegacyScans
	out.ShadowLegacySaturated = shadow.LegacySaturated
	out.ShadowLegacyWouldDecline = shadow.LegacySaturated || shadow.LegacyScans > int64(limit)
	out.ShadowLegacyComplete = out.VerificationComplete && scopeComplete && !shadow.LegacySaturated
}
