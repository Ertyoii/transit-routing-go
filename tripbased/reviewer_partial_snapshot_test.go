package tripbased_test

import (
	"context"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"testing"
)

func TestReviewerPartialSemanticSnapshotMustNotPanic(t *testing.T) {
	d := data()
	d.Stops = []model.Stop{d.Stops[0], d.Stops[1], d.Stops[3]}
	trip(&d, "in", []string{"A", "B"}, 0, 1)
	trip(&d, "out", []string{"B", "D"}, 2, 3)
	cfg := ruleConfig(1)
	full, e := tripbased.BuildFlash(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	work := full.Snapshot().Stats.Work
	for _, method := range []string{"FLASH", "TP"} {
		t.Run(method, func(t *testing.T) {
			for budget := 1; budget <= work; budget++ {
				c := cfg
				c.Limits.MaxWork = budget
				var buildError error
				var snapshotComplete func() bool
				if method == "FLASH" {
					x, err := tripbased.BuildFlash(context.Background(), d, c)
					buildError = err
					snapshotComplete = func() bool { return x.Snapshot().Complete }
				} else {
					pc := rulePatternConfig()
					pc.Profile = c
					x, err := tripbased.BuildPatterns(context.Background(), d, pc)
					buildError = err
					snapshotComplete = func() bool { return x.Snapshot().Complete }
				}
				if buildError == nil {
					continue
				}
				func() {
					defer func() {
						if p := recover(); p != nil {
							t.Fatalf("incomplete %s Snapshot panicked at MaxWork=%d: build_error=%v panic=%v", method, budget, buildError, p)
						}
					}()
					if snapshotComplete() {
						t.Fatal("partial snapshot marked complete", budget)
					}
				}()
			}
		})
	}
}
