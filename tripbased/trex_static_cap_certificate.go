package tripbased

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/internal/capverify"
	"github.com/ertyoii/transit-routing-go/model"
	"strings"
)

const TREXStaticCapCertificateMethod = "static_rules_ranked_caps_bounded_exhaustive_verified_v1"

type TREXStaticCapVerifyLimits struct {
	MaxStates   int `json:"max_states"`
	MaxJourneys int `json:"max_journeys"`
	MaxScans    int `json:"max_scans"`
}
type TREXStaticCapResult struct {
	TREXCapCertifiedResult
	VerificationComplete bool   `json:"verification_complete"`
	VerificationStates   int    `json:"verification_states"`
	VerificationScans    int    `json:"verification_scans"`
	DeclineReason        string `json:"decline_reason,omitempty"`
}

// TREXStaticCapCertificate is an experimental composition. It returns only the
// ranked kernel's journeys and requires both original-query feasibility and a
// complete independent bounded enumeration with identical objective vectors.
// Enumeration is a correctness gate, not an accelerated fallback or a proof of
// static-rule rank preservation. Its separate budgets/costs must be reported.
// Preparation owns the index's immutable dataset; callers cannot mutate it.
type TREXStaticCapCertificate struct {
	cap    *TREXCapCertificate
	limits TREXStaticCapVerifyLimits
}

func PrepareTREXStaticCapCertificate(ctx context.Context, x *TREXIndex, limits TREXStaticCapVerifyLimits) (*TREXStaticCapCertificate, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !x.Complete() {
		return nil, fmt.Errorf("static cap certificate: complete index required")
	}
	if !x.staticRulesCandidate || x.transferWalkLimit != nil || x.snap.Profile != "trex_static_stop_pair_nonbinding_candidate_v1" {
		return nil, fmt.Errorf("static cap certificate: static stop-pair unrestricted candidate profile required")
	}
	if limits.MaxStates <= 0 || limits.MaxJourneys <= 0 || limits.MaxScans <= 0 {
		return nil, fmt.Errorf("static cap certificate: positive verification limits required")
	}
	c, e := prepareTREXCapChecker(ctx, x)
	if e != nil {
		return nil, e
	}
	return &TREXStaticCapCertificate{cap: c, limits: limits}, nil
}

func (c *TREXStaticCapCertificate) Search(ctx context.Context, q model.Query) (out TREXStaticCapResult, err error) {
	out.Certificate.Method = TREXStaticCapCertificateMethod
	fail := func(reason string, e error) (TREXStaticCapResult, error) {
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
	if c == nil || c.cap == nil {
		return fail("unprepared", fmt.Errorf("static cap certificate: unprepared"))
	}
	if q.Walking != nil {
		w := *q.Walking
		q.Walking = &w
	}
	ranked, e := c.cap.Search(ctx, q)
	out.TREXCapCertifiedResult = ranked
	out.Certificate.Method = TREXStaticCapCertificateMethod
	// Inner certificate is provisional: no caller sees Accepted before the
	// independent completeness gate has finished.
	out.Certificate.Accepted = false
	if e != nil {
		reason := "ranked_search_or_original_resource_guard"
		if strings.Contains(e.Error(), "unrestricted representative fails original query") {
			reason = "unrestricted_representative_infeasible"
		}
		return fail(reason, e)
	}
	verified, e := capverify.Enumerate(ctx, c.cap.index.base.p.Data, q, capverify.Limits{MaxStates: c.limits.MaxStates, MaxJourneys: c.limits.MaxJourneys, MaxScans: c.limits.MaxScans})
	out.VerificationStates = verified.States
	out.VerificationScans = verified.Scans
	if e != nil || !verified.Complete {
		if e == nil {
			e = fmt.Errorf("static cap certificate: independent verification incomplete")
		}
		return fail("verification_incomplete", e)
	}
	out.VerificationComplete = true
	a, work, e := capverify.Frontier(ctx, c.cap.index.base.p.Data, q, ranked.Journeys, c.limits.MaxScans-out.VerificationScans)
	out.VerificationScans += work
	if e != nil {
		return fail("ranked_frontier_projection", e)
	}
	b, work, e := capverify.Frontier(ctx, c.cap.index.base.p.Data, q, verified.Journeys, c.limits.MaxScans-out.VerificationScans)
	out.VerificationScans += work
	if e != nil {
		return fail("reference_frontier_projection", e)
	}
	if len(a) != len(b) {
		return fail("frontier_mismatch", fmt.Errorf("static cap certificate: ranked/reference frontier mismatch"))
	}
	for i := range a {
		if a[i] != b[i] {
			return fail("frontier_mismatch", fmt.Errorf("static cap certificate: ranked/reference frontier mismatch"))
		}
	}
	if e := ctx.Err(); e != nil {
		return fail("cancelled", e)
	}
	out.Certificate.Accepted = true
	return out, nil
}
