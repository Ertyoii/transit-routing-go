package tripbased_test

import (
	"context"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"reflect"
	"slices"
	"sync"
	"testing"
)

func flashConfig(cells int) tripbased.FlashConfig {
	return tripbased.FlashConfig{Cells: cells, MaxBoardings: 4, Limits: tripbased.FlashLimits{MaxStops: 100, MaxTrips: 100, MaxClosurePaths: 10000, MaxWalkSettled: 10000, MaxTransfers: 10000, MaxRuns: 10000, MaxWork: 10000000, MaxFlagBytes: 1000000, MaxWitnesses: 10000, MaxQuerySegments: 100000}}
}
func flashQuery(q model.Query) model.Query {
	q.TransferBuffer = 0
	q.AccessibleOnly = false
	q.Walking = &model.WalkingLimits{Access: 30, Egress: 30, Transfer: 30, Direct: 30}
	q.MaxWalkSeconds = 300
	q.MaxLegs = 128
	return q
}
func flashCompare(t testing.TB, d model.Dataset, x *tripbased.FlashIndex, q model.Query, want []int64) {
	t.Helper()
	for name, fn := range map[string]func(context.Context, model.Query) (tripbased.Result, error){"flags": x.Search, "cp": x.SearchUnflagged, "baseline": x.SearchBaseline} {
		r, e := fn(context.Background(), q)
		if e != nil || !r.Complete {
			t.Fatalf("%s failed: %v dataset=%+v query=%+v", name, e, d, q)
		}
		got := objectives(reconstructed(t, d, q, r.Journeys), q.MaxBoardings)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s want %v got %v data=%+v query=%+v snapshot=%+v", name, want, got, d, q, x.Snapshot())
		}
	}
}
func TestFlashIndependentWholeWords(t *testing.T) {
	words := 0
	for seed := uint64(0); seed < 320; seed++ {
		d, q := generated(seed)
		d.Transfers = nil
		for i := range d.Connections {
			d.Connections[i].Pickup = true
			d.Connections[i].Dropoff = true
		}
		q = flashQuery(q)
		ms, n := fullWords(d, q, 7)
		words += n
		for _, cells := range []int{1, 3, len(d.Stops)} {
			x, e := tripbased.BuildFlash(context.Background(), d, flashConfig(cells))
			if e != nil {
				t.Fatalf("seed%d %v dataset=%+v", seed, e, d)
			}
			flashCompare(t, d, x, q, objectives(ms, q.MaxBoardings))
		}
	}
	t.Logf("320 complete whole-word universes; %d words; three partitions x three controls", words)
}
func TestFlashUsefulFlags(t *testing.T) {
	d, q := data(), flashQuery(query())
	trip(&d, "in", []string{"A", "B"}, 0, 2)
	trip(&d, "out", []string{"B", "D"}, 3, 5)
	trip(&d, "branch", []string{"B", "X"}, 3, 6)
	x, e := tripbased.BuildFlash(context.Background(), d, flashConfig(len(d.Stops)))
	if e != nil {
		t.Fatal(e)
	}
	flashCompare(t, d, x, q, []int64{inf, inf, 5, 5})
	if x.Snapshot().Stats.SetFlags == 0 {
		t.Fatal("flags never set")
	}
	r, e := x.Search(context.Background(), q)
	if e != nil || r.Stats.FlagRejected == 0 {
		t.Fatal("irrelevant branch was not filtered", e, r)
	}
}
func FuzzFlashIndependent(f *testing.F) {
	for _, s := range []uint64{0, 1, 71, 319} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		d, q := generated(seed)
		d.Transfers = nil
		for i := range d.Connections {
			d.Connections[i].Pickup = true
			d.Connections[i].Dropoff = true
		}
		q = flashQuery(q)
		ms, _ := fullWords(d, q, 7)
		x, e := tripbased.BuildFlash(context.Background(), d, flashConfig(3))
		if e != nil {
			t.Fatal(e)
		}
		flashCompare(t, d, x, q, objectives(ms, q.MaxBoardings))
	})
}

func TestFlashCanonicalLiteralsAndFailures(t *testing.T) {
	cases := []model.Dataset{}
	d := data()
	trip(&d, "local", []string{"A", "B", "C"}, 0, 1, 2, 5)
	trip(&d, "out", []string{"C", "D"}, 6, 7)
	d.Footpaths = []model.Footpath{{ID: "shortcut-walk", From: "B", To: "C", Duration: 1, Accessible: model.Yes}}
	cases = append(cases, d)
	d = data()
	trip(&d, "blue", []string{"A", "B"}, 0, 1)
	trip(&d, "red", []string{"C", "X"}, 2, 3)
	trip(&d, "green", []string{"Y", "D"}, 4, 5)
	trip(&d, "purple", []string{"B", "D"}, 7, 8)
	trip(&d, "later", []string{"C", "D"}, 3, 9)
	d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 1}, {ID: "cy", From: "C", To: "Y", Duration: 1}, {ID: "xy", From: "X", To: "Y", Duration: 1}}
	for i := range d.Footpaths {
		d.Footpaths[i].Accessible = model.Yes
	}
	cases = append(cases, d)
	d = data()
	trip(&d, "early", []string{"A", "D", "B", "D"}, 0, 1, 2, 3, 5, 6)
	trip(&d, "late", []string{"A", "D", "B", "D"}, 10, 11, 12, 13, 15, 16)
	d.Footpaths = []model.Footpath{{ID: "xa", From: "X", To: "A", Duration: 2, Accessible: model.Yes}, {ID: "xb", From: "X", To: "B", Duration: 4, Accessible: model.Yes}}
	cases = append(cases, d)
	for ci, d := range cases {
		x, e := tripbased.BuildFlash(context.Background(), d, flashConfig(3))
		if e != nil {
			t.Fatal(ci, e)
		}
		for _, s := range x.Snapshot().Transfers {
			q := flashQuery(query())
			q.Origin = s.Source
			q.Destination = s.Target
			q.Departure = s.Departure
			q.MaxBoardings = 2
			reconstructed(t, d, q, []model.Journey{s.Candidate})
		}
		for _, s := range x.Snapshot().Witnesses {
			q := flashQuery(query())
			q.Origin = s.Source
			q.Destination = s.Target
			q.Departure = s.Departure
			q.MaxBoardings = 4
			reconstructed(t, d, q, []model.Journey{s.Journey})
		}
		if ci == 0 {
			found := false
			for _, tr := range x.Snapshot().Transfers {
				if tr.FromTrip == "local" && tr.ToTrip == "out" {
					if tr.Exit != 1 {
						t.Fatal("latest-exit conflict", tr)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("canonical early exit absent")
			}
		}
	}
	d = data()
	trip(&d, "first", []string{"A", "B"}, 0, 1)
	trip(&d, "second", []string{"B", "D"}, 2, 3)
	trip(&d, "third", []string{"B", "X"}, 2, 4)
	for _, kind := range []string{"runs", "work", "closure", "settled", "transfers", "flags", "witnesses", "network"} {
		c := flashConfig(3)
		switch kind {
		case "runs":
			c.Limits.MaxRuns = 1
		case "work":
			c.Limits.MaxWork = 1
		case "closure":
			c.Limits.MaxClosurePaths = 1
		case "settled":
			c.Limits.MaxWalkSettled = 1
		case "transfers":
			c.Limits.MaxTransfers = 1
		case "flags":
			c.Limits.MaxFlagBytes = 1
		case "witnesses":
			c.Limits.MaxWitnesses = 1
		case "network":
			c.Limits.MaxStops = 1
		}
		x, e := tripbased.BuildFlash(context.Background(), d, c)
		if e == nil || x.Complete() {
			t.Fatal("budget accepted", kind)
		}
		if r, e := x.Search(context.Background(), flashQuery(query())); e == nil || r.Complete {
			t.Fatal("partial query", kind)
		}
	}
	x, e := tripbased.BuildFlash(context.Background(), d, flashConfig(3))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q := flashQuery(query())
	q.Destination = q.Origin
	if r, e := x.Search(ctx, q); e == nil || r.Complete || len(r.Journeys) > 0 {
		t.Fatal("cancelled self accepted")
	}
	for _, kind := range []string{"buffer", "accessibility", "boardings", "total", "legs"} {
		q := flashQuery(query())
		switch kind {
		case "buffer":
			q.TransferBuffer = 1
		case "accessibility":
			q.AccessibleOnly = true
		case "boardings":
			q.MaxBoardings = 5
		case "total":
			q.MaxWalkSeconds = 0
		case "legs":
			q.MaxLegs = 1
		}
		if r, e := x.Search(context.Background(), q); e == nil || r.Complete {
			t.Fatal("unsupported accepted", kind)
		}
	}
}

func TestFlashPermutationTimeAndConcurrentCalls(t *testing.T) {
	d, q := data(), flashQuery(query())
	trip(&d, "in", []string{"A", "B"}, 0, 2)
	trip(&d, "out", []string{"C", "D"}, 4, 5)
	trip(&d, "branch", []string{"C", "X"}, 4, 6)
	d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 1, Accessible: model.Yes}}
	x, e := tripbased.BuildFlash(context.Background(), d, flashConfig(3))
	if e != nil {
		t.Fatal(e)
	}
	r, e := x.Search(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	perm := d.Clone()
	slices.Reverse(perm.Stops)
	slices.Reverse(perm.Trips)
	slices.Reverse(perm.Connections)
	slices.Reverse(perm.Footpaths)
	y, e := tripbased.BuildFlash(context.Background(), perm, flashConfig(3))
	if e != nil {
		t.Fatal(e)
	}
	sx, sy := x.Snapshot(), y.Snapshot()
	sx.PhasesNS = nil
	sy.PhasesNS = nil
	if !reflect.DeepEqual(sx, sy) {
		t.Fatal("input-order changed canonical index")
	}
	shifted := d.Clone()
	for i := range shifted.Connections {
		shifted.Connections[i].Departure += 90000
		shifted.Connections[i].Arrival += 90000
	}
	z, e := tripbased.BuildFlash(context.Background(), shifted, flashConfig(3))
	if e != nil {
		t.Fatal(e)
	}
	sq := q
	sq.Departure += 90000
	sq.LatestArrival += 90000
	flashCompare(t, shifted, z, sq, []int64{inf, inf, 90005, 90005})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, e := x.Search(context.Background(), q)
			if e != nil || !reflect.DeepEqual(got, r) {
				t.Error("concurrent index changed", e)
			}
		}()
	}
	wg.Wait()
	// Snapshot exports own their mutable slices, including witnesses and flags.
	snap := x.Snapshot()
	snap.Transfers[0].Cells[0] = 999
	snap.Witnesses[0].Journey.Legs[0].Ref = "bad"
	got, e := x.Search(context.Background(), q)
	if e != nil || !reflect.DeepEqual(got, r) {
		t.Fatal("snapshot mutation changed index")
	}
	for _, kind := range []string{"access", "egress", "direct", "transfer"} {
		bad := q
		wl := *q.Walking
		bad.Walking = &wl
		switch kind {
		case "access":
			bad.Origin = "B"
			wl.Access = 0
		case "egress":
			bad.Destination = "C"
			wl.Egress = 0
		case "direct":
			bad.Origin = "B"
			bad.Destination = "C"
			wl.Direct = 0
		case "transfer":
			wl.Transfer = 0
		}
		if r, e := x.Search(context.Background(), bad); e == nil || r.Complete {
			t.Fatal("binding cap accepted", kind)
		}
	}
	for _, kind := range []string{"pickup", "dropoff", "rules", "zero"} {
		bad := d.Clone()
		switch kind {
		case "pickup":
			bad.Connections[0].Pickup = false
		case "dropoff":
			bad.Connections[0].Dropoff = false
		case "rules":
			bad.Transfers = []model.TransferRule{{ID: "r", FromStop: "B", ToStop: "C", Type: 3}}
		case "zero":
			bad.Footpaths[0].Duration = 0
		}
		if x, e := tripbased.BuildFlash(context.Background(), bad, flashConfig(3)); e == nil || x.Complete() {
			t.Fatal("unsupported input accepted", kind)
		}
	}
	c := flashConfig(3)
	c.Limits.MaxQuerySegments = 1
	limited, e := tripbased.BuildFlash(context.Background(), d, c)
	if e != nil {
		t.Fatal(e)
	}
	if r, e := limited.Search(context.Background(), q); e == nil || r.Complete {
		t.Fatal("queued query budget accepted")
	}
}

func TestFlashSelfPruningNeeded(t *testing.T) {
	d, q := data(), flashQuery(query())
	trip(&d, "early", []string{"A", "B"}, 1, 2)
	trip(&d, "late", []string{"A", "B"}, 3, 4)
	trip(&d, "last", []string{"B", "D"}, 5, 6)
	q.Departure = 1
	x, e := tripbased.BuildFlash(context.Background(), d, flashConfig(3))
	if e != nil {
		t.Fatal(e)
	}
	flashCompare(t, d, x, q, []int64{inf, inf, 6, 6})
	if x.Snapshot().Stats.EqualArrivalRepairs == 0 {
		t.Fatal("equal-old arrival repair not exercised")
	}
}
