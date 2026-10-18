package selection

import (
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"slices"
)

// ReserveArrivalBoarding keeps one raw-pool Pareto representative per strict
// arrival improvement with more boardings, then fills by ordinary arrival order.
// It makes no claim about paths absent from the supplied pool.
func ReserveArrivalBoarding(input []model.Validated, cap int) (Result, error) {
	r, e := Select(input, false, Spec{Mode: Candidates, CandidateCap: max(cap, len(input))})
	if e != nil {
		return r, e
	}
	if cap <= 0 {
		return r, fmt.Errorf("positive candidate cap required")
	}
	ordered := append([]model.Validated{}, r.Journeys...)
	slices.SortFunc(ordered, func(a, b model.Validated) int {
		if a.Metrics.Boardings < b.Metrics.Boardings {
			return -1
		}
		if a.Metrics.Boardings > b.Metrics.Boardings {
			return 1
		}
		return Compare(a, b)
	})
	chosen := []model.Validated{}
	seen := map[string]bool{}
	best := int64(1 << 62)
	for _, v := range ordered {
		if v.Metrics.Arrival < best {
			best = v.Metrics.Arrival
			chosen = append(chosen, v)
			seen[v.Identity] = true
		}
	}
	if len(chosen) > cap {
		return r, fmt.Errorf("candidate cap %d cannot retain %d arrival/boarding representatives", cap, len(chosen))
	}
	for _, v := range r.Journeys {
		if len(chosen) == cap {
			break
		}
		if !seen[v.Identity] {
			seen[v.Identity] = true
			chosen = append(chosen, v)
		}
	}
	slices.SortFunc(chosen, Compare)
	r.Journeys = chosen
	r.Capped = len(chosen) < len(input)
	return r, nil
}
