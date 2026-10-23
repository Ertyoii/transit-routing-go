package raptor

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/profilequery"
	"github.com/ertyoii/transit-routing-go/transfer"
)

var rangeTestLimits = profilequery.Limits{MaxLabels: 100000, MaxConnectionVisits: 100000, MaxDepartures: 10000}

// Compare the whole native result, not only objective vectors: this includes
// latest-ready ordering, exact tied witnesses, counters, partial output, errors
// and completion flags. The frozen native control is not a correctness oracle.
func rangeStorageEqual(t testing.TB, x *Index, q profilequery.Query, polls int, limits profilequery.Limits) profilequery.Result {
	t.Helper()
	a, ae := x.frozenNativeRange(denseContext(polls), q, limits)
	b, be := x.SearchRange(denseContext(polls), q, limits)
	if !reflect.DeepEqual(a, b) || fmt.Sprint(ae) != fmt.Sprint(be) {
		t.Fatalf("range storage mismatch polls=%d q=%+v limits=%+v\nold=%+v %v\nnew=%+v %v", polls, q, limits, a, ae, b, be)
	}
	if a.Complete {
		for ready := q.Base.Departure; ready <= q.End; ready++ {
			aj, ae := a.At(ready)
			bj, be := b.At(ready)
			if !reflect.DeepEqual(aj, bj) || fmt.Sprint(ae) != fmt.Sprint(be) {
				t.Fatalf("materialized exact witness difference at ready=%d", ready)
			}
		}
	}
	return b
}

func TestRangeDenseStorageExactNativeAdversarial(t *testing.T) {
	for seed := uint64(0); seed < 96; seed++ {
		for rule := 0; rule < 9; rule++ {
			d, base := indexedFixture(seed, rule)
			x, err := New(d)
			if err != nil {
				t.Fatal(err)
			}
			q := profilequery.Query{Base: base, End: 15}
			q.Base.AccessibleOnly = seed%2 == 0
			rangeStorageEqual(t, x, q, -1, rangeTestLimits)
			for _, cap := range []int64{0, 1, 3, 5} {
				for _, buffer := range []int64{0, 2, 10} {
					v := q
					v.Base.TransferBuffer = buffer
					v.Base.Walking = &model.WalkingLimits{Access: cap, Transfer: 5 - cap, Egress: cap, Direct: 5}
					rangeStorageEqual(t, x, v, -1, rangeTestLimits)
				}
			}
			for _, boards := range []int{0, 1, 2, 4} {
				v := q
				v.Base.MaxBoardings = boards
				rangeStorageEqual(t, x, v, -1, rangeTestLimits)
			}
			for _, bounds := range [][2]int64{{0, 0}, {2, 2}, {0, 3}, {4, 9}, {15, 15}, {0, 40}} {
				v := q
				v.Base.Departure, v.End = bounds[0], bounds[1]
				rangeStorageEqual(t, x, v, -1, rangeTestLimits)
			}
			for _, pair := range [][2]string{{"Z", "D"}, {"A", "Z"}, {"A", "A"}, {"B", "C"}, {"missing", "D"}} {
				v := q
				v.Base.Origin, v.Base.Destination = pair[0], pair[1]
				rangeStorageEqual(t, x, v, -1, rangeTestLimits)
			}
		}
	}
}

type rangePollCounter struct {
	context.Context
	polls int
}

func (c *rangePollCounter) Err() error { c.polls++; return nil }

func TestRangeDenseStorageEveryCancellationAndBudgetBoundary(t *testing.T) {
	for _, seed := range []uint64{13, 82, 193} {
		d, base := indexedFixture(seed, 5)
		x, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		q := profilequery.Query{Base: base, End: 15}
		count := &rangePollCounter{Context: context.Background()}
		full, err := x.frozenNativeRange(count, q, rangeTestLimits)
		if err != nil {
			t.Fatal(err)
		}
		for polls := 1; polls <= count.polls+1; polls++ {
			rangeStorageEqual(t, x, q, polls, rangeTestLimits)
		}
		for budget := 0; budget <= full.Stats.Labels+1; budget++ {
			limits := rangeTestLimits
			limits.MaxLabels = budget
			rangeStorageEqual(t, x, q, -1, limits)
		}
		for budget := 0; budget <= full.Stats.Connections+1; budget++ {
			limits := rangeTestLimits
			limits.MaxConnectionVisits = budget
			rangeStorageEqual(t, x, q, -1, limits)
		}
		for budget := 0; budget <= full.Stats.Departures+1; budget++ {
			limits := rangeTestLimits
			limits.MaxDepartures = budget
			rangeStorageEqual(t, x, q, -1, limits)
		}
		t.Logf("seed=%d all %d cancellation polls, labels 0..%d, connections 0..%d, departures 0..%d", seed, count.polls+1, full.Stats.Labels+1, full.Stats.Connections+1, full.Stats.Departures+1)
	}
}

func TestRangeDenseStorageOwnershipConcurrentReuseAndStopPermutation(t *testing.T) {
	d, base := indexedFixture(82, 5)
	x, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	q := profilequery.Query{Base: base, End: 15}
	before := rangeStorageEqual(t, x, q, -1, rangeTestLimits)
	d.Stops[0].ID = "mutated"
	d.Connections[0].From = "mutated"
	d.Transfers[0].MinSeconds = 999
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := q
			v.Base.TransferBuffer = int64(i)
			v.Base.Walking = &model.WalkingLimits{Access: 5, Transfer: int64(i % 6), Egress: 5, Direct: 5}
			v.Base.Departure = int64(i % 4)
			rangeStorageEqual(t, x, v, -1, rangeTestLimits)
		}(i)
	}
	wg.Wait()
	after := rangeStorageEqual(t, x, q, -1, rangeTestLimits)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reused index or returned results mutated")
	}
	d2, _ := indexedFixture(82, 5)
	slices.Reverse(d2.Stops)
	y, err := New(d2)
	if err != nil {
		t.Fatal(err)
	}
	permuted := rangeStorageEqual(t, y, q, -1, rangeTestLimits)
	if !reflect.DeepEqual(before, permuted) {
		t.Fatal("stop input order changed exact profile")
	}
}

func TestRangeDenseStorageLazyRuleErrorsAndInvalidQueries(t *testing.T) {
	d, base := indexedFixture(82, 5)
	x, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	q := profilequery.Query{Base: base, End: 15}
	for _, end := range []int64{-1, 41} {
		v := q
		v.End = end
		rangeStorageEqual(t, x, v, -1, rangeTestLimits)
	}
	for _, limits := range []profilequery.Limits{{}, {MaxLabels: -1, MaxConnectionVisits: 1, MaxDepartures: 1}} {
		rangeStorageEqual(t, x, q, -1, limits)
	}
	v := q
	v.Base.MaxWalkSeconds = 0
	rangeStorageEqual(t, x, v, -1, rangeTestLimits)
	v = q
	v.Base.MaxLegs = 1
	rangeStorageEqual(t, x, v, -1, rangeTestLimits)
	// Constructor rejection normally prevents ambiguous rules. Inject only for
	// operational parity of the first lazy error and its partial output/stats.
	d.Transfers = []model.TransferRule{{ID: "conflict-a", FromStop: "B", ToStop: "C", Type: 0}, {ID: "conflict-b", FromStop: "B", ToStop: "C", Type: 3}}
	x.p.Rules = transfer.New(d)
	rangeStorageEqual(t, x, q, -1, rangeTestLimits)
}

func FuzzRangeDenseStorage(f *testing.F) {
	for _, seed := range []uint64{0, 1, 13, 82, 193} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		d, base := indexedFixture(seed, int(seed%9))
		x, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		rangeStorageEqual(t, x, profilequery.Query{Base: base, End: 15}, -1, rangeTestLimits)
	})
}

func BenchmarkRangeDenseStorage(b *testing.B) {
	d, base := indexedFixture(82, 5)
	x, err := New(d)
	if err != nil {
		b.Fatal(err)
	}
	q := profilequery.Query{Base: base, End: 15}
	for _, optimized := range []bool{false, true} {
		name := "frozen_native"
		if optimized {
			name = "dense_persistent"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if optimized {
					_, err = x.SearchRange(context.Background(), q, rangeTestLimits)
				} else {
					_, err = x.frozenNativeRange(context.Background(), q, rangeTestLimits)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
