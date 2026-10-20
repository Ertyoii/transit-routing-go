package tripbased

import (
	"context"
	"fmt"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

const TREXCapCertificateMethod = "ranked_unrestricted_frontier_all_representatives_feasible_v1"

type TREXCapCertificateInfo struct {
	Binding                     model.ResultBinding `json:"binding"`
	Method                      string              `json:"method"`
	Accepted                    bool                `json:"accepted"`
	UnrestrictedRepresentatives int                 `json:"unrestricted_representatives"`
	CheckedRepresentatives      int                 `json:"checked_representatives"`
	ReplayWork                  int                 `json:"replay_work"`
}

type TREXCapCertifiedResult struct {
	TREXResult
	Certificate TREXCapCertificateInfo `json:"cap_certificate"`
}

// TREXCapCertificate keeps the original ranked kernel and certifies a sufficient
// per-query condition for binding caps. Failure returns no unchecked journeys;
// it never filters an unrestricted frontier into a claimed capped frontier.
// The object owns a read-only replay checker and is safe for concurrent queries.
type TREXCapCertificate struct {
	index   *TREXIndex
	checker *validate.Checker
}

// PrepareTREXCapCertificate supports only the original, completely prepared
// paper-model rank index. Unranked capped builders and experimental policy
// extensions need their own unrestricted-kernel qualification before composition.
// Checker preparation duplicates immutable lookup/data storage and belongs to
// startup accounting, not query timing.
func PrepareTREXCapCertificate(ctx context.Context, x *TREXIndex) (*TREXCapCertificate, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !x.Complete() {
		return nil, fmt.Errorf("T-REX cap certificate: complete index required")
	}
	if x.transferWalkLimit != nil || x.snap.Profile != "trex_positive_static_transitive_nonbinding_v1" {
		return nil, fmt.Errorf("T-REX cap certificate: qualified original unrestricted rank profile required")
	}
	return prepareTREXCapChecker(ctx, x)
}

func prepareTREXCapChecker(ctx context.Context, x *TREXIndex) (*TREXCapCertificate, error) {
	checker, e := validate.New(x.base.p.Data)
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	return &TREXCapCertificate{index: x, checker: checker}, nil
}

// Search runs the same overlay-ranked kernel as Search without the global
// sufficient walking-role certificate. It does not mutate or enlarge q's caps.
// Success requires independently replaying EVERY unrestricted representative
// under the original query, including role/aggregate/leg constraints. Since the
// unrestricted set contains the capped set, a complete unrestricted frontier
// whose representatives are all feasible under q is also a complete capped
// arrival/boarding frontier. If any representative fails, hidden capped
// alternatives may exist: return unsupported, never filtered completeness.
func (c *TREXCapCertificate) Search(ctx context.Context, q model.Query) (out TREXCapCertifiedResult, err error) {
	out.Journeys = []model.Journey{}
	out.Certificate.Method = TREXCapCertificateMethod
	if q.Walking != nil {
		w := *q.Walking
		q.Walking = &w
	}
	fail := func(e error) (TREXCapCertifiedResult, error) {
		out.Journeys = []model.Journey{}
		out.Complete = false
		out.Error = e.Error()
		return out, e
	}
	if e := ctx.Err(); e != nil {
		return fail(e)
	}
	if c == nil || c.index == nil || c.checker == nil {
		return fail(fmt.Errorf("T-REX cap certificate: unprepared checker"))
	}
	binding, e := c.checker.Binding(q)
	if e != nil {
		return fail(e)
	}
	out.Certificate.Binding = binding
	core, e := c.index.searchTREXForCertificate(ctx, q, "overlay", true)
	out.TREXResult = core
	out.Certificate.UnrestrictedRepresentatives = len(core.Journeys)
	if e != nil {
		return fail(e)
	}
	if !core.Complete {
		return fail(fmt.Errorf("T-REX cap certificate: unrestricted search incomplete"))
	}
	for _, j := range core.Journeys {
		// Reserve one replay unit per raw leg plus its query/binding check.
		// Certificate work shares the original query work budget; it is never
		// silently excluded from completion when the kernel uses that budget.
		for i := 0; i <= len(j.Legs); i++ {
			if e := ctx.Err(); e != nil {
				return fail(e)
			}
			if out.Stats.Work == c.index.cfg.Limits.MaxQueryWork {
				return fail(fmt.Errorf("T-REX cap certificate incomplete: replay work budget exhausted"))
			}
			out.Stats.Work++
			out.Certificate.ReplayWork++
		}
		out.Certificate.CheckedRepresentatives++
		if _, e := c.checker.Check(q, j); e != nil {
			return fail(fmt.Errorf("T-REX cap certificate not established: unrestricted representative fails original query: %w", e))
		}
	}
	if e := ctx.Err(); e != nil {
		return fail(e)
	}
	out.Certificate.Accepted = true
	return out, nil
}
