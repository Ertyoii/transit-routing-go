// Package selection handles result contracts, not route search or feasibility.
package selection

import (
	"cmp"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"slices"
)

type Mode string

const (
	Earliest   Mode = "earliest_arrival"
	Pareto     Mode = "pareto"
	TopK       Mode = "strict_top_k"
	Candidates Mode = "bounded_candidates"
)

type Spec struct {
	Mode           Mode     `json:"mode"`
	K              int      `json:"k,omitempty"`
	CandidateCap   int      `json:"candidate_cap,omitempty"`
	ParetoCriteria []string `json:"pareto_criteria,omitempty"`
}
type Result struct {
	Mode          Mode              `json:"mode"`
	Exact         bool              `json:"exact"`
	InputComplete bool              `json:"input_complete"`
	Capped        bool              `json:"capped"`
	Journeys      []model.Validated `json:"journeys"`
}

func Compare(a, b model.Validated) int {
	for _, v := range []int{cmp.Compare(a.Metrics.Arrival, b.Metrics.Arrival), cmp.Compare(a.Metrics.Transfers, b.Metrics.Transfers), cmp.Compare(a.Metrics.WalkingSeconds, b.Metrics.WalkingSeconds), cmp.Compare(a.Metrics.LegCount, b.Metrics.LegCount), cmp.Compare(a.Identity, b.Identity)} {
		if v != 0 {
			return v
		}
	}
	return 0
}
func metric(m model.Metrics, c string) int64 {
	switch c {
	case "arrival":
		return m.Arrival
	case "boardings":
		return int64(m.Boardings)
	case "transfers":
		return int64(m.Transfers)
	case "walking_seconds":
		return m.WalkingSeconds
	}
	panic("validated criterion")
}
func Select(input []model.Validated, complete bool, s Spec) (Result, error) {
	r := Result{Mode: s.Mode, InputComplete: complete, Journeys: []model.Validated{}}
	if s.Mode != Earliest && s.Mode != Pareto && s.Mode != TopK && s.Mode != Candidates {
		return r, fmt.Errorf("unknown mode %q", s.Mode)
	}
	if s.Mode != Candidates && !complete {
		return r, fmt.Errorf("exact selection requires a complete universe")
	}
	if (s.Mode == TopK && s.K <= 0) || (s.Mode == Candidates && s.CandidateCap <= 0) {
		return r, fmt.Errorf("positive k/cap required")
	}
	if s.Mode == Pareto {
		seen := map[string]bool{}
		for _, c := range s.ParetoCriteria {
			if seen[c] || (c != "arrival" && c != "boardings" && c != "transfers" && c != "walking_seconds") {
				return r, fmt.Errorf("invalid/duplicate Pareto criterion %q", c)
			}
			seen[c] = true
		}
		if len(seen) < 2 || !seen["arrival"] {
			return r, fmt.Errorf("Pareto requires arrival and at least one other criterion")
		}
	}
	seen := map[string]bool{}
	for _, j := range input {
		if j.Identity == "" || seen[j.Identity] {
			return r, fmt.Errorf("empty/duplicate journey identity")
		}
		seen[j.Identity] = true
	}
	js := append([]model.Validated{}, input...)
	slices.SortFunc(js, Compare)
	r.Exact = s.Mode != Candidates
	switch s.Mode {
	case Earliest:
		if len(js) > 1 {
			js = js[:1]
		}
	case TopK:
		if len(js) > s.K {
			js = js[:s.K]
		}
	case Candidates:
		if len(js) > s.CandidateCap {
			js = js[:s.CandidateCap]
			r.Capped = true
		}
	case Pareto:
		front := []model.Validated{}
		for i, j := range js {
			dominated := false
			for n, k := range js {
				if i == n {
					continue
				}
				all, strict := true, false
				for _, c := range s.ParetoCriteria {
					a, b := metric(k.Metrics, c), metric(j.Metrics, c)
					all = all && a <= b
					strict = strict || a < b
				}
				if all && strict {
					dominated = true
					break
				}
			}
			if !dominated {
				front = append(front, j)
			}
		}
		js = front
	}
	r.Journeys = js
	return r, nil
}
