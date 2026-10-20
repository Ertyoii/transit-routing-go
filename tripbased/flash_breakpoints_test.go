package tripbased

import (
	"context"
	"errors"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

func TestRuleBreakpointStreamingMatchesDense(t *testing.T) {
	for seed := int64(0); seed < 128; seed++ {
		rng := rand.New(rand.NewSource(seed))
		n := 1 + rng.Intn(12)
		x := &FlashIndex{cfg: FlashConfig{Limits: FlashLimits{MaxWork: 1000000}}, ids: map[string]int{}, cp: &Index{}, walks: make([][]paper.Path, n)}
		for s := 0; s < n; s++ {
			name := fmt.Sprintf("s%02d", s)
			x.names = append(x.names, name)
			x.ids[name] = s
		}
		for i := 0; i < 48; i++ {
			s := rng.Intn(n)
			dep := int64(rng.Intn(32))
			if i%5 == 0 {
				dep += 90000
			}
			x.cp.trips = append(x.cp.trips, trip{cs: []model.Connection{{From: x.names[s], Departure: dep, Arrival: dep + 1, Pickup: i%7 != 0}}})
		}
		for s := 0; s < n; s++ {
			for v := 0; v < n; v++ {
				if s == v || rng.Intn(3) == 0 {
					duration := int64(rng.Intn(40))
					if s == v {
						duration = 0
					}
					x.walks[s] = append(x.walks[s], paper.Path{To: x.names[v], Duration: duration})
				}
			}
		}
		if seed%2 == 0 {
			x.base = &Index{p: &paper.Profile{Data: model.Dataset{Window: &model.TimeWindow{Start: 5, End: 25}}}}
		}
		lower, upper := int64(0), int64(7*24*60*60)
		if seed%2 == 0 {
			lower, upper = 5, 25
		}
		w := &flashWork{context.Background(), x}
		lists, e := x.ruleDepartureLists(w)
		if e != nil {
			t.Fatal(e)
		}
		for source := 0; source < n; source++ {
			expected := map[int64]bool{}
			for _, p := range x.walks[source] {
				for _, tr := range x.cp.trips {
					for _, c := range tr.cs {
						if c.Pickup && c.From == p.To && c.Arrival <= upper && c.Departure-p.Duration >= lower && c.Departure-p.Duration <= upper {
							expected[c.Departure-p.Duration] = true
						}
					}
				}
			}
			want := []int64{}
			for d := range expected {
				want = append(want, d)
			}
			sort.Slice(want, func(i, j int) bool { return want[i] > want[j] })
			got := []int64{}
			if e = x.eachRuleBreakpoint(source, lists, w, func(d int64) error { got = append(got, d); return nil }); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed%d source%d want%v got%v", seed, source, want, got)
			}
			sentinel := errors.New("stop streaming")
			count := 0
			e = x.eachRuleBreakpoint(source, lists, w, func(int64) error { count++; return sentinel })
			if len(want) > 0 && (!errors.Is(e, sentinel) || count != 1) {
				t.Fatal("callback failure did not stop stream", e, count)
			}
		}
	}
	t.Log("128 independent dense-vs-streaming layouts; duplicates, negative adjusted times, restricted entries and >24h departures")
}
