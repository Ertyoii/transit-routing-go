package tripbased_test

import (
	"context"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"testing"
)

// The independent full-word evaluator checks both role classification and
// optimum. Endpoint caps remain 600 even when an intermediate walk is longer.
func TestFlashIndependentEndpointCaps(t *testing.T) {
	for name, intermediate := range map[string]bool{"disconnected": false, "intermediate": true} {
		t.Run(name, func(t *testing.T) {
			d := data()
			if intermediate {
				trip(&d, "in", []string{"A", "B"}, 0, 1)
				trip(&d, "out", []string{"C", "D"}, 752, 753)
			} else {
				trip(&d, "direct", []string{"A", "D"}, 0, 1)
			}
			q := query()
			q.LatestArrival = 800
			q.MaxBoardings = 3
			q.MaxLegs = 128
			q.MaxWalkSeconds = 10000
			q.Walking = &model.WalkingLimits{Access: 600, Egress: 600, Direct: 600, Transfer: 750}
			for _, walk := range []bool{false, true} {
				if walk {
					d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 750, Accessible: model.Yes}}
				}
				want, _ := fullWords(d, q, 4)
				for _, cells := range []int{1, 3, len(d.Stops)} {
					x, e := tripbased.BuildFlash(context.Background(), d, flashConfig(cells))
					if e != nil {
						t.Fatal(e)
					}
					flashCompare(t, d, x, q, objectives(want, q.MaxBoardings))
				}
			}
		})
	}
}
