package tripbased

import (
	"context"
	"fmt"

	"github.com/ertyoii/transit-routing-go/internal/capverify"
	"github.com/ertyoii/transit-routing-go/model"
)

const TREXRoleCapCandidateMethod = "role_cap_filtered_overlay_ranked_bounded_exhaustive_verified_candidate_v1"

type TREXRoleCapCandidateInfo struct {
	Binding                model.ResultBinding `json:"binding"`
	Method                 string              `json:"method"`
	Accepted               bool                `json:"accepted"`
	RankedRepresentatives  int                 `json:"ranked_representatives"`
	CheckedRepresentatives int                 `json:"checked_representatives"`
	ReplayWork             int                 `json:"replay_work"`
}

type TREXRoleCapCandidateResult struct {
	TREXResult
	Certificate          TREXRoleCapCandidateInfo `json:"candidate_certificate"`
	VerificationComplete bool                     `json:"verification_complete"`
	VerificationStates   int                      `json:"verification_states"`
	VerificationScans    int                      `json:"verification_scans"`
	DeclineReason        string                   `json:"decline_reason,omitempty"`
}

// TREXRoleCapCandidate is an additive experiment, not a rank-preservation proof.
// Original role caps filter the overlay-ranked query before scalar dominance.
// Every answer still requires full original-query replay and unchanged complete
// independent enumeration. Enumeration supplies no returned journeys. Aggregate
// resources, absorbing destinations and rank-induced omissions can still decline.
type TREXRoleCapCandidate struct {
	gate *TREXStaticCapCertificate
}

func PrepareTREXRoleCapCandidate(ctx context.Context, x *TREXIndex, limits TREXStaticCapVerifyLimits) (*TREXRoleCapCandidate, error) {
	gate, err := PrepareTREXStaticCapCertificate(ctx, x, limits)
	if err != nil {
		return nil, err
	}
	return &TREXRoleCapCandidate{gate: gate}, nil
}

func (c *TREXRoleCapCandidate) Search(ctx context.Context, q model.Query) (out TREXRoleCapCandidateResult, err error) {
	out.Journeys = []model.Journey{}
	out.Certificate.Method = TREXRoleCapCandidateMethod
	fail := func(reason string, e error) (TREXRoleCapCandidateResult, error) {
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
	if c == nil || c.gate == nil || c.gate.cap == nil {
		return fail("unprepared", fmt.Errorf("T-REX role-cap candidate: unprepared"))
	}
	if q.Walking != nil {
		w := *q.Walking
		q.Walking = &w
	}
	checker, index, limits := c.gate.cap.checker, c.gate.cap.index, c.gate.limits
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
		return fail("ranked_search_incomplete", fmt.Errorf("T-REX role-cap candidate: ranked search incomplete"))
	}
	for _, journey := range core.Journeys {
		for i := 0; i <= len(journey.Legs); i++ {
			if e := ctx.Err(); e != nil {
				return fail("cancelled", e)
			}
			if out.Stats.Work == index.cfg.Limits.MaxQueryWork {
				return fail("replay_work_incomplete", fmt.Errorf("T-REX role-cap candidate: replay work budget exhausted"))
			}
			out.Stats.Work++
			out.Certificate.ReplayWork++
		}
		out.Certificate.CheckedRepresentatives++
		if _, e := checker.Check(q, journey); e != nil {
			return fail("ranked_candidate_infeasible", fmt.Errorf("T-REX role-cap candidate fails original query: %w", e))
		}
	}
	verified, e := capverify.Enumerate(ctx, index.base.p.Data, q, capverify.Limits{MaxStates: limits.MaxStates, MaxJourneys: limits.MaxJourneys, MaxScans: limits.MaxScans})
	out.VerificationStates, out.VerificationScans = verified.States, verified.Scans
	if e != nil || !verified.Complete {
		if e == nil {
			e = fmt.Errorf("T-REX role-cap candidate: independent verification incomplete")
		}
		return fail("verification_incomplete", e)
	}
	out.VerificationComplete = true
	a, work, e := capverify.Frontier(ctx, index.base.p.Data, q, core.Journeys, limits.MaxScans-out.VerificationScans)
	out.VerificationScans += work
	if e != nil {
		return fail("ranked_frontier_projection", e)
	}
	b, work, e := capverify.Frontier(ctx, index.base.p.Data, q, verified.Journeys, limits.MaxScans-out.VerificationScans)
	out.VerificationScans += work
	if e != nil {
		return fail("reference_frontier_projection", e)
	}
	if len(a) != len(b) {
		return fail("frontier_mismatch", fmt.Errorf("T-REX role-cap candidate: ranked/reference frontier mismatch"))
	}
	for i := range a {
		if a[i] != b[i] {
			return fail("frontier_mismatch", fmt.Errorf("T-REX role-cap candidate: ranked/reference frontier mismatch"))
		}
	}
	if e := ctx.Err(); e != nil {
		return fail("cancelled", e)
	}
	out.Certificate.Accepted = true
	return out, nil
}
