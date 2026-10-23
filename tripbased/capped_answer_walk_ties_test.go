package tripbased_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
)

type answerTieVector struct {
	arrival int64
	boards  int
}

func answerTieMetrics(t testing.TB, d model.Dataset, q model.Query, js []model.Journey) map[answerTieVector]int64 {
	t.Helper()
	out := map[answerTieVector]int64{}
	for _, m := range reconstructed(t, expandedRuleSpec(d), q, js) {
		k := answerTieVector{m.Arrival, m.Boardings}
		if old, ok := out[k]; !ok || m.WalkingSeconds < old {
			out[k] = m.WalkingSeconds
		}
	}
	return out
}

func compareAnswerTies(t testing.TB, d model.Dataset, q model.Query, cfg tripbased.Config) (*tripbased.Index, tripbased.Result, tripbased.Result) {
	t.Helper()
	x, err := tripbased.New(context.Background(), d, cfg)
	if err != nil {
		t.Fatal(err, d, q)
	}
	base, be := x.SearchCappedWalking(context.Background(), q)
	got, ge := x.SearchCappedWalkingAnswerTies(context.Background(), q)
	if fmt.Sprint(be) != fmt.Sprint(ge) || base.Complete != got.Complete || base.Error != got.Error {
		t.Fatal("native status/budget contract changed", base, be, got, ge)
	}
	bm, gm := answerTieMetrics(t, d, q, base.Journeys), answerTieMetrics(t, d, q, got.Journeys)
	if len(bm) != len(gm) {
		t.Fatal("native vector count changed", bm, gm)
	}
	for k, walk := range bm {
		if g, ok := gm[k]; !ok || g > walk {
			t.Fatal("native vector lost or walking worsened", k, walk, gm)
		}
	}
	// Answer-only bookkeeping must not alter any native scheduling or charged
	// budget work. The sole additional reconstruction visit is disclosed.
	bs, gs := base.Stats, got.Stats
	bs.Connections, gs.Connections = 0, 0
	if bs != gs {
		t.Fatal("native reached/enqueue/transfer work changed", base.Stats, got.Stats)
	}
	return x, base, got
}

func answerTieFixture(kind string) (model.Dataset, model.Query, int64, int64) {
	d, q := data(), query()
	q.MaxBoardings, q.MaxWalkSeconds = 2, 100
	switch kind {
	case "access-and-pruned-first-arrival":
		q.Origin = "C"
		d.Footpaths = []model.Footpath{{ID: "access", From: "C", To: "B", Duration: 5, Accessible: model.Yes}}
		trip(&d, "walking-first", []string{"B", "D"}, 10, 20)
		trip(&d, "seated-tie", []string{"C", "D"}, 0, 20)
		return d, q, 5, 0
	case "egress":
		trip(&d, "first", []string{"A", "B"}, 0, 15)
		trip(&d, "second", []string{"A", "C"}, 0, 19)
		d.Footpaths = []model.Footpath{{ID: "long-exit", From: "B", To: "D", Duration: 5, Accessible: model.Yes}, {ID: "short-exit", From: "C", To: "D", Duration: 1, Accessible: model.Yes}}
		return d, q, 5, 1
	case "cumulative-transfer":
		trip(&d, "in", []string{"A", "B"}, 0, 2)
		trip(&d, "out-x", []string{"X", "D"}, 10, 20)
		trip(&d, "out-y", []string{"Y", "D"}, 10, 20)
		d.Footpaths = []model.Footpath{{ID: "long-transfer", From: "B", To: "X", Duration: 5, Accessible: model.Yes}, {ID: "short-transfer", From: "B", To: "Y", Duration: 1, Accessible: model.Yes}}
		q.Walking = &model.WalkingLimits{Transfer: 5}
		return d, q, 5, 1
	}
	panic(kind)
}

func TestCappedAnswerWalkTiesLiteralImprovement(t *testing.T) {
	for _, kind := range []string{"access-and-pruned-first-arrival", "egress", "cumulative-transfer"} {
		t.Run(kind, func(t *testing.T) {
			d, q, before, after := answerTieFixture(kind)
			_, base, got := compareAnswerTies(t, d, q, config(q))
			boards := 1
			if kind == "cumulative-transfer" {
				boards = 2
			}
			k := answerTieVector{20, boards}
			bm, gm := answerTieMetrics(t, d, q, base.Journeys), answerTieMetrics(t, d, q, got.Journeys)
			if bm[k] != before || gm[k] != after {
				t.Fatal("literal tie was not improved", bm, gm)
			}
			// These acyclic fixtures have at most three scheduled events and a
			// directed walk at most once in each vehicle gap: depth seven is
			// a complete unpruned raw-word universe, not a contributor union.
			ms, words := fullWords(d, q, 7)
			if want, actual := objectives(ms, q.MaxBoardings), objectives(reconstructed(t, d, q, got.Journeys), q.MaxBoardings); !reflect.DeepEqual(want, actual) {
				t.Fatal(want, actual)
			}
			bestWalk := inf
			for _, m := range ms {
				if m.Arrival == 20 && m.Boardings == boards {
					bestWalk = min(bestWalk, m.WalkingSeconds)
				}
			}
			if bestWalk != after {
				t.Fatal("literal independent minimum differs", bestWalk, after)
			}
			t.Logf("%s: native vector (20,%d), walking %d -> %d; %d unpruned raw words", kind, boards, before, after, words)
		})
	}
}

func TestCappedAnswerWalkTiesRetainsReachedPositionLoss(t *testing.T) {
	d, q := data(), query()
	q.Origin, q.MaxBoardings = "C", 1
	d.Footpaths = []model.Footpath{{ID: "access", From: "C", To: "B", Duration: 3, Accessible: model.Yes}}
	trip(&d, "same-trip", []string{"B", "C", "D"}, 5, 6, 7, 10)
	_, base, got := compareAnswerTies(t, d, q, config(q))
	k := answerTieVector{10, 1}
	bm, gm := answerTieMetrics(t, d, q, base.Journeys), answerTieMetrics(t, d, q, got.Journeys)
	ms, words := fullWords(d, q, 3)
	bestWalk := inf
	for _, m := range ms {
		if m.Arrival == 10 && m.Boardings == 1 {
			bestWalk = min(bestWalk, m.WalkingSeconds)
		}
	}
	if bm[k] != 3 || gm[k] != 3 || bestWalk != 0 {
		t.Fatal("known local-only limit disappeared", bm, gm, bestWalk)
	}
	if !reflect.DeepEqual(objectives(ms, q.MaxBoardings), objectives(reconstructed(t, d, q, got.Journeys), q.MaxBoardings)) {
		t.Fatal("native cap vector changed")
	}
	t.Logf("retained limitation: local walking=3, independent same-vector minimum=0; %d complete raw words", words)
}

func TestCappedAnswerWalkTiesIndependentWholeWordsAndAllCaps(t *testing.T) {
	words, cases := 0, 0
	for seed := uint64(0); seed < 96; seed++ {
		d, q := generated(seed)
		// Independently binding egress is permitted by capped TB. Preserve all
		// stop-pair rules, accessibility and pickup/dropoff adversaries.
		q.Walking.Egress = int64(seed % 4)
		q.MaxBoardings = 4
		ms, n := fullWords(expandedRuleSpec(d), q, 7)
		words += n
		want := objectives(ms, 4)
		for _, strict := range []bool{false, true} {
			cfg := config(q)
			cfg.StrictLineOrder = strict
			x, _, got := compareAnswerTies(t, d, q, cfg)
			actual := objectives(reconstructed(t, expandedRuleSpec(d), q, got.Journeys), 4)
			if !reflect.DeepEqual(actual, want) {
				t.Fatal(seed, strict, actual, want, d, q)
			}
			// Every actual boarding ceiling also preserves exact native status,
			// cap-arrival vector and weakly improved same-vector walking.
			for cap := 0; cap <= 4; cap++ {
				qc := q
				qc.MaxBoardings = cap
				base, be := x.SearchCappedWalking(context.Background(), qc)
				got, ge := x.SearchCappedWalkingAnswerTies(context.Background(), qc)
				if fmt.Sprint(be) != fmt.Sprint(ge) || !base.Complete || !got.Complete {
					t.Fatal(seed, strict, cap, be, ge)
				}
				actual := objectives(reconstructed(t, expandedRuleSpec(d), qc, got.Journeys), cap)
				if !reflect.DeepEqual(actual, want[:cap+1]) {
					t.Fatal(seed, strict, cap, actual, want[:cap+1])
				}
				bm, gm := answerTieMetrics(t, d, qc, base.Journeys), answerTieMetrics(t, d, qc, got.Journeys)
				for k, w := range bm {
					if g, ok := gm[k]; !ok || g > w {
						t.Fatal(seed, strict, cap, bm, gm)
					}
				}
				cases++
			}
		}
	}
	t.Logf("96 independent complete raw-word universes, %d unpruned words, %d actual cap/strict-order cases", words, cases)
}

// This deterministic context exposes each exact native poll boundary. It never
// uses wall-clock timing or goroutine scheduling to manufacture cancellation.
type answerTieCancelContext struct {
	context.Context
	limit, polls int
}

func (c *answerTieCancelContext) Err() error {
	c.polls++
	if c.polls >= c.limit {
		return context.Canceled
	}
	return nil
}

func TestCappedAnswerWalkTiesBudgetsAndCancellation(t *testing.T) {
	d, q, _, _ := answerTieFixture("cumulative-transfer")
	for limit := 1; limit <= 8; limit++ {
		cfg := config(q)
		cfg.MaxQueuedSegments = limit
		compareAnswerTies(t, d, q, cfg)
	}
	x, _, _ := compareAnswerTies(t, d, q, config(q))
	all := &answerTieCancelContext{Context: context.Background(), limit: 1000000}
	if r, err := x.SearchCappedWalking(all, q); err != nil || !r.Complete {
		t.Fatal(err, r)
	}
	for limit := 1; limit <= all.polls+1; limit++ {
		bc := &answerTieCancelContext{Context: context.Background(), limit: limit}
		gc := &answerTieCancelContext{Context: context.Background(), limit: limit}
		base, be := x.SearchCappedWalking(bc, q)
		got, ge := x.SearchCappedWalkingAnswerTies(gc, q)
		if fmt.Sprint(be) != fmt.Sprint(ge) || base.Complete != got.Complete || base.Error != got.Error || bc.polls != gc.polls {
			t.Fatal("cancellation boundary changed", limit, bc.polls, gc.polls, base, be, got, ge)
		}
		bv := objectives(reconstructed(t, d, q, base.Journeys), q.MaxBoardings)
		gv := objectives(reconstructed(t, d, q, got.Journeys), q.MaxBoardings)
		if !reflect.DeepEqual(bv, gv) {
			t.Fatal("partial cap vectors changed", limit, bv, gv)
		}
	}
	for _, edit := range []func(*model.Query){
		func(v *model.Query) { v.TransferBuffer++ },
		func(v *model.Query) { v.AccessibleOnly = !v.AccessibleOnly },
		func(v *model.Query) { w := *v.Walking; w.Transfer++; v.Walking = &w },
		func(v *model.Query) { v.MaxWalkSeconds = 0 },
		func(v *model.Query) { v.MaxLegs = 1 },
	} {
		v := q
		edit(&v)
		base, be := x.SearchCappedWalking(context.Background(), v)
		got, ge := x.SearchCappedWalkingAnswerTies(context.Background(), v)
		if be == nil || fmt.Sprint(be) != fmt.Sprint(ge) || base.Complete || got.Complete {
			t.Fatal("capability guard changed", be, ge, base, got)
		}
	}
}

func TestCappedAnswerWalkTiesDeterminismOwnershipAndConcurrentReuse(t *testing.T) {
	d, q, _, _ := answerTieFixture("access-and-pruned-first-arrival")
	before, _ := json.Marshal(struct {
		D model.Dataset
		Q model.Query
	}{d, q})
	x, _, got := compareAnswerTies(t, d, q, config(q))
	want, _ := json.Marshal(got)
	prep := x.Preparation()
	for i := 0; i < 5; i++ {
		r, err := x.SearchCappedWalkingAnswerTies(context.Background(), q)
		b, _ := json.Marshal(r)
		if err != nil || !reflect.DeepEqual(b, want) {
			t.Fatal("nondeterministic query", err, r)
		}
	}
	if got.Journeys[0].Legs[0].Ref != "seated-tie/0" {
		t.Fatal("wrong deterministic tie witness", got)
	}
	got.Journeys[0].Legs[0].Ref = "mutated-owned-result"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := x.SearchCappedWalkingAnswerTies(context.Background(), q)
			b, _ := json.Marshal(r)
			if err != nil || !reflect.DeepEqual(b, want) {
				t.Error("query/result ownership changed", err, r)
			}
		}()
	}
	wg.Wait()
	after, _ := json.Marshal(struct {
		D model.Dataset
		Q model.Query
	}{d, q})
	if !reflect.DeepEqual(before, after) || x.Preparation() != prep {
		t.Fatal("query mutated input or index metadata")
	}
	slices.Reverse(d.Connections)
	slices.Reverse(d.Trips)
	_, _, permuted := compareAnswerTies(t, d, q, config(q))
	b, _ := json.Marshal(permuted)
	if !reflect.DeepEqual(b, want) {
		t.Fatal("input permutation changed deterministic witness", permuted)
	}
	if x.CappedWalkingAnswerTiesCapabilities().RequiresEgressAtLeastTransfer || x.CappedWalkingAnswerTiesCapabilities().Contract == x.CappedWalkingCapabilities().Contract {
		t.Fatal("named local-only capability missing")
	}
}
