package tripbased_test

import (
	"context"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"math/rand/v2"
	"reflect"
	"slices"
	"sync"
	"testing"
)

func config(q model.Query) tripbased.Config {
	return tripbased.Config{TransferWalkingLimit: q.Walking.Transfer, TransferBuffer: q.TransferBuffer, AccessibleOnly: q.AccessibleOnly, MaxTransfers: 10000, MaxQueuedSegments: 10000}
}
func reconstructed(t testing.TB, d model.Dataset, q model.Query, js []model.Journey) []model.Metrics {
	t.Helper()
	out := []model.Metrics{}
	seen := map[string]bool{}
	for _, j := range js {
		word := []int{}
		for _, leg := range j.Legs {
			idx := -1
			for i, c := range d.Connections {
				if leg.Kind == "ride" && leg.Ref == c.ID {
					idx = i
				}
			}
			for i, w := range d.Footpaths {
				if leg.Kind == "walk" && leg.Ref == w.ID {
					idx = len(d.Connections) + i
				}
			}
			if idx < 0 {
				t.Fatal("unknown event", leg)
			}
			word = append(word, idx)
		}
		expected, m, ok := assess(d, q, word)
		if !ok || !reflect.DeepEqual(j, expected) {
			t.Fatalf("invalid Trip-Based witness: %+v expected %+v", j, expected)
		}
		id := identity(j)
		if seen[id] {
			t.Fatal("duplicate representative")
		}
		seen[id] = true
		out = append(out, m)
	}
	return out
}
func fullWords(d model.Dataset, q model.Query, limit int) ([]model.Metrics, int) {
	out := []model.Metrics{}
	count := 0
	var visit func([]int)
	visit = func(w []int) {
		count++
		_, m, ok := assess(d, q, w)
		if ok {
			out = append(out, m)
		}
		if len(w) == limit {
			return
		}
		for a := 0; a < len(d.Connections)+len(d.Footpaths); a++ {
			visit(append(w, a))
		}
	}
	visit(nil)
	return out, count
}
func check(t testing.TB, d model.Dataset, q model.Query, want []int64) *tripbased.Index {
	t.Helper()
	idx, e := tripbased.New(context.Background(), d, config(q))
	if e != nil {
		t.Fatal(e)
	}
	r, e := idx.Search(context.Background(), q)
	if e != nil || !r.Complete {
		t.Fatalf("Trip-Based failed: %+v %v", r, e)
	}
	got := objectives(reconstructed(t, d, q, r.Journeys), q.MaxBoardings)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v got %v data=%+v query=%+v results=%+v", want, got, d, q, r)
	}
	prior := inf
	for _, m := range reconstructed(t, d, q, r.Journeys) {
		if m.Arrival >= prior {
			t.Fatal("non-Pareto representative retained")
		}
		prior = m.Arrival
	}
	return idx
}
func generated(seed uint64) (model.Dataset, model.Query) {
	rng := rand.New(rand.NewPCG(seed, seed+93))
	d := data()
	q := query()
	q.MaxBoardings = 1 + rng.IntN(3)
	q.MaxLegs = 32
	q.Departure = int64(rng.IntN(5))
	q.LatestArrival = 30
	q.TransferBuffer = int64(rng.IntN(4))
	q.Walking = &model.WalkingLimits{Access: int64(rng.IntN(5)), Transfer: int64(rng.IntN(4)), Direct: int64(rng.IntN(5))}
	q.Walking.Egress = q.Walking.Transfer + int64(rng.IntN(4))
	q.AccessibleOnly = rng.IntN(5) == 0
	q.Destination = []string{"C", "D", "A"}[rng.IntN(3)]
	dep := int64(rng.IntN(6))
	arr := dep + 1 + int64(rng.IntN(5))
	dep2 := arr + int64(rng.IntN(3))
	trip(&d, "through", []string{"A", "B", "C"}, dep, arr, dep2, dep2+1+int64(rng.IntN(5)))
	f := []string{"A", "B", "C"}[rng.IntN(3)]
	dep = int64(rng.IntN(16))
	trip(&d, "link", []string{f, "D"}, dep, dep+1+int64(rng.IntN(5)))
	for i := range d.Connections {
		d.Connections[i].Pickup = rng.IntN(5) != 0
		d.Connections[i].Dropoff = rng.IntN(5) != 0
	}
	from := []string{"A", "B", "C"}[rng.IntN(3)]
	d.Footpaths = []model.Footpath{{ID: "walk", From: from, To: "D", Duration: 1 + int64(rng.IntN(4)), Accessible: model.Yes}}
	typ := []int{0, 2, 3}[rng.IntN(3)]
	r := model.TransferRule{ID: "rule", FromStop: "B", ToStop: f, Type: typ}
	if typ == 2 {
		r.MinSeconds = int64(rng.IntN(7))
	}
	d.Transfers = []model.TransferRule{r}
	if rng.IntN(4) == 0 {
		d.Stops[1].Accessible = model.No
	}
	if rng.IntN(4) == 0 {
		d.Trips[0].Accessible = model.Unknown
	}
	return d, q
}
func TestIndependentWholeWordCorpus(t *testing.T) {
	total, feasible := 0, 0
	for seed := uint64(0); seed < 320; seed++ {
		d, q := generated(seed)
		ms, n := fullWords(d, q, 7)
		total += n
		feasible += len(ms)
		check(t, d, q, objectives(ms, q.MaxBoardings))
	}
	// Three positive scheduled events cannot repeat; one directed non-self walk
	// can occur at most once per gap, hence seven events exhaust this corpus.
	t.Logf("320 complete universes; %d unpruned words; %d feasible words", total, feasible)
}
func TestLiteralTripMechanisms(t *testing.T) {
	t.Run("overtaking and fewer-transfer frontier", func(t *testing.T) {
		d, q := data(), query()
		trip(&d, "slow", []string{"A", "B", "D"}, 1, 20, 21, 40)
		trip(&d, "fast", []string{"A", "B", "D"}, 2, 10, 11, 30)
		trip(&d, "feed", []string{"A", "C"}, 3, 5)
		trip(&d, "express", []string{"C", "D"}, 6, 15)
		idx := check(t, d, q, []int64{inf, 30, 15, 15})
		if idx.Preparation().Lines != 4 {
			t.Fatal("overtaking not split")
		}
	})
	t.Run("repeated stop and staying aboard restrictions", func(t *testing.T) {
		d, q := data(), query()
		trip(&d, "loop", []string{"A", "B", "A", "D"}, 0, 2, 3, 4, 5, 7)
		q.Departure = 3
		check(t, d, q, []int64{inf, 7, 7, 7})
		q.Departure = 0
		d.Connections[0].Dropoff = false
		d.Connections[1].Pickup = false
		d.Stops[1].Accessible = model.No
		q.AccessibleOnly = true
		check(t, d, q, []int64{inf, 7, 7, 7})
	})
	t.Run("later readiness via different alighting stop", func(t *testing.T) {
		d, q := data(), query()
		trip(&d, "early", []string{"A", "B"}, 0, 2)
		trip(&d, "later", []string{"A", "C"}, 1, 4)
		trip(&d, "exit", []string{"B", "D"}, 7, 9)
		d.Footpaths = []model.Footpath{{ID: "w", From: "C", To: "B", Duration: 1, Accessible: model.Yes}}
		d.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "B", ToStop: "B", Type: 2, MinSeconds: 8}}
		check(t, d, q, []int64{inf, inf, 9, 9})
	})
	t.Run("same line backward transfer retained", func(t *testing.T) {
		d, q := data(), query()
		q.Origin = "C"
		q.Destination = "B"
		trip(&d, "first", []string{"A", "B", "C", "X"}, 0, 1, 2, 3, 4, 5)
		trip(&d, "second", []string{"A", "B", "C", "X"}, 6, 7, 8, 9, 10, 11)
		d.Footpaths = []model.Footpath{{ID: "return", From: "X", To: "A", Duration: 1, Accessible: model.Yes}}
		check(t, d, q, []int64{inf, inf, 7, 7})
	})
	t.Run("minimum time overlaps walk", func(t *testing.T) {
		d, q := data(), query()
		trip(&d, "in", []string{"A", "B"}, 0, 2)
		trip(&d, "out", []string{"C", "D"}, 7, 9)
		d.Footpaths = []model.Footpath{{ID: "w", From: "B", To: "C", Duration: 3, Accessible: model.Yes}}
		d.Transfers = []model.TransferRule{{ID: "rule", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: 5}}
		check(t, d, q, []int64{inf, inf, 9, 9})
	})
}
func TestPermutationTranslationAndConcurrentReuse(t *testing.T) {
	d, q := data(), query()
	trip(&d, "direct", []string{"A", "D"}, 1, 20)
	trip(&d, "in", []string{"A", "B"}, 2, 4)
	trip(&d, "out", []string{"B", "D"}, 5, 10)
	want := []int64{inf, 20, 10, 10}
	idx := check(t, d, q, want)
	var wg sync.WaitGroup
	results := make(chan tripbased.Result, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := idx.Search(context.Background(), q)
			if e != nil {
				r.Error = e.Error()
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		if r.Error != "" || !reflect.DeepEqual(objectives(reconstructed(t, d, q, r.Journeys), 3), want) {
			t.Fatal("concurrent reuse changed results")
		}
	}
	slices.Reverse(d.Connections)
	slices.Reverse(d.Trips)
	check(t, d, q, want)
	const shift = 25 * 3600
	for i := range d.Connections {
		d.Connections[i].Departure += shift
		d.Connections[i].Arrival += shift
	}
	q.Departure += shift
	q.LatestArrival += shift
	check(t, d, q, []int64{inf, 20 + shift, 10 + shift, 10 + shift})
}
func TestUnsupportedAndBudgetFailures(t *testing.T) {
	d, q := data(), query()
	trip(&d, "in", []string{"A", "B"}, 0, 2)
	trip(&d, "one", []string{"B", "C"}, 3, 4)
	trip(&d, "two", []string{"B", "D"}, 3, 5)
	cfg := config(q)
	cfg.MaxTransfers = 1
	if idx, e := tripbased.New(context.Background(), d, cfg); e == nil || idx != nil {
		t.Fatal("truncated preprocessing accepted")
	}
	cfg = config(q)
	cfg.MaxQueuedSegments = 1
	idx, e := tripbased.New(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	r, e := idx.Search(context.Background(), q)
	if e == nil || r.Complete {
		t.Fatal("query budget certified")
	}
	idx, e = tripbased.New(context.Background(), d, config(q))
	if e != nil {
		t.Fatal(e)
	}
	for name, edit := range map[string]func(*model.Query){"egress below transfer": func(q *model.Query) { q.Walking.Egress = 1 }, "changed buffer": func(q *model.Query) { q.TransferBuffer++ }, "changed accessibility": func(q *model.Query) { q.AccessibleOnly = true }, "binding resources": func(q *model.Query) { q.MaxLegs = 1 }} {
		t.Run(name, func(t *testing.T) {
			bad := q
			w := *q.Walking
			bad.Walking = &w
			edit(&bad)
			r, e := idx.Search(context.Background(), bad)
			if e == nil || r.Complete {
				t.Fatal("unsupported query certified")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = tripbased.New(ctx, d, config(q)); e == nil {
		t.Fatal("canceled preprocessing accepted")
	}
	r, e = idx.Search(ctx, q)
	if e == nil || r.Complete {
		t.Fatal("canceled query certified")
	}
	d.Transfers = []model.TransferRule{{ID: "specific", FromStop: "B", ToStop: "B", FromTrip: "in", Type: 3}}
	if _, e = tripbased.New(context.Background(), d, config(q)); e == nil {
		t.Fatal("unsupported trip selector accepted")
	}
}
func FuzzTripBasedIndependent(f *testing.F) {
	f.Add(uint64(1))
	f.Add(uint64(20261017))
	f.Fuzz(func(t *testing.T, seed uint64) {
		d, q := generated(seed)
		ms, _ := fullWords(d, q, 7)
		check(t, d, q, objectives(ms, q.MaxBoardings))
	})
}
