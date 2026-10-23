package tripbased

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

type nativeAnswerTieVector struct {
	Arrival   int64
	Boardings int
}

func nativeAnswerTieWalking(t testing.TB, d model.Dataset, q model.Query, js []model.Journey) map[nativeAnswerTieVector]int64 {
	t.Helper()
	checker, err := validate.New(d)
	if err != nil {
		t.Fatal(err)
	}
	out := map[nativeAnswerTieVector]int64{}
	for _, j := range js {
		v, err := checker.Check(q, j)
		if err != nil {
			t.Fatal(err, j)
		}
		k := nativeAnswerTieVector{v.Metrics.Arrival, v.Metrics.Boardings}
		if old, ok := out[k]; !ok || v.Metrics.WalkingSeconds < old {
			out[k] = v.Metrics.WalkingSeconds
		}
	}
	return out
}

func TestCappedAnswerWalkTiesFrozenNativeAdversarialDifferential(t *testing.T) {
	type fixture struct {
		Dataset  model.Dataset `json:"dataset"`
		Query    model.Query   `json:"query"`
		Expected []*int64      `json:"expected"`
	}
	cases := []fixture{}
	for _, c := range capCases(t) {
		cases = append(cases, fixture{c.Dataset, c.Query, c.Expected})
		for _, p := range c.Probes {
			q := c.Query
			q.Origin, q.Departure = p.Origin, p.Departure
			cases = append(cases, fixture{c.Dataset, q, p.Expected})
		}
	}
	raw, err := os.ReadFile("testdata/exact_storage_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var storage []fixture
	if err := json.Unmarshal(raw, &storage); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, storage...)
	boundaries := 0
	for i, c := range cases {
		for _, strict := range []bool{false, true} {
			cfg := Config{StrictLineOrder: strict, TransferWalkingLimit: c.Query.Walking.Transfer, TransferBuffer: c.Query.TransferBuffer, AccessibleOnly: c.Query.AccessibleOnly, MaxTransfers: 100000, MaxQueuedSegments: 100000}
			x, err := New(context.Background(), c.Dataset, cfg)
			if err != nil {
				t.Fatal(i, strict, err)
			}
			base, be := x.SearchCappedWalking(context.Background(), c.Query)
			got, ge := x.SearchCappedWalkingAnswerTies(context.Background(), c.Query)
			if be != nil || ge != nil || !base.Complete || !got.Complete {
				t.Fatal(i, strict, be, ge)
			}
			bv, gv := capVector(t, c.Dataset, c.Query, base.Journeys), capVector(t, c.Dataset, c.Query, got.Journeys)
			if !reflect.DeepEqual(bv, gv) || c.Expected != nil && !reflect.DeepEqual(gv, c.Expected) {
				t.Fatal(i, strict, "native/frozen cap vector mismatch", bv, gv, c.Expected)
			}
			bm, gm := nativeAnswerTieWalking(t, c.Dataset, c.Query, base.Journeys), nativeAnswerTieWalking(t, c.Dataset, c.Query, got.Journeys)
			if len(bm) != len(gm) {
				t.Fatal(i, strict, bm, gm)
			}
			for k, w := range bm {
				if g, ok := gm[k]; !ok || g > w {
					t.Fatal(i, strict, "walking worsened", bm, gm)
				}
			}
			// Same accepted-segment count implies an identical exact failure
			// boundary. Keep actual partial outputs and statuses, not just success.
			for _, limit := range []int{max(1, base.Stats.QueuedSegments-1), max(1, base.Stats.QueuedSegments), base.Stats.QueuedSegments + 1} {
				view := *x
				view.cfg.MaxQueuedSegments = limit
				b, be := view.SearchCappedWalking(context.Background(), c.Query)
				g, ge := view.SearchCappedWalkingAnswerTies(context.Background(), c.Query)
				if fmt.Sprint(be) != fmt.Sprint(ge) || b.Complete != g.Complete || b.Error != g.Error || !reflect.DeepEqual(capVector(t, c.Dataset, c.Query, b.Journeys), capVector(t, c.Dataset, c.Query, g.Journeys)) {
					t.Fatal(i, strict, limit, "budget/status/vector boundary changed", b, be, g, ge)
				}
				bs, gs := b.Stats, g.Stats
				bs.Connections, gs.Connections = 0, 0
				if bs != gs {
					t.Fatal(i, strict, limit, "native charged work changed", b.Stats, g.Stats)
				}
				boundaries++
			}
		}
	}
	t.Logf("%d frozen cap/storage/probe cases, both line orders, %d exact queued-segment boundaries", len(cases), boundaries)
}
