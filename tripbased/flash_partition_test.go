package tripbased

import (
	"context"
	"errors"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"math/rand"
	"reflect"
	"testing"
)

func partitionCase(n, cells int, connections []model.Connection, paths [][]paper.Path, budget int) *FlashIndex {
	x := &FlashIndex{cfg: FlashConfig{Cells: cells, Limits: FlashLimits{MaxWork: budget}}, ids: map[string]int{}, walks: paths,
		base: &Index{p: &paper.Profile{Data: model.Dataset{Connections: connections}}}}
	for v := 0; v < n; v++ {
		name := fmt.Sprintf("s%03d", v)
		x.names = append(x.names, name)
		x.ids[name] = v
	}
	return x
}

// A separate dense-matrix specification directly evaluates every unassigned
// candidate's affinity to cell members; it has no heap, epochs or incremental
// affinity state and is intentionally slow and small-network only.
func partitionSpecification(x *FlashIndex) FlashPartition {
	n := len(x.names)
	weights := make([][]int, n)
	degree := make([]int, n)
	for i := range weights {
		weights[i] = make([]int, n)
	}
	add := func(a, b int) {
		if a != b {
			weights[a][b]++
			weights[b][a]++
		}
	}
	for _, c := range x.base.p.Data.Connections {
		add(x.ids[c.From], x.ids[c.To])
	}
	for u, ps := range x.walks {
		for _, p := range ps {
			add(u, x.ids[p.To])
		}
	}
	for a := range weights {
		for _, weight := range weights[a] {
			degree[a] += weight
		}
	}
	p := FlashPartition{Method: "deterministic balanced weighted graph growth; max accumulated affinity, degree, stop ID", Cells: map[string]int{}, Sizes: make([]int, x.cfg.Cells)}
	for a := range weights {
		for b, w := range weights[a] {
			if a < b && w > 0 {
				p.LayoutEdges++
				p.TotalWeight += w
			}
		}
	}
	remaining := n
	for cell := 0; cell < x.cfg.Cells; cell++ {
		capacity := (remaining + x.cfg.Cells - cell - 1) / (x.cfg.Cells - cell)
		members := []int{}
		for slot := 0; slot < capacity; slot++ {
			best, bestAffinity := -1, -1
			for v := 0; v < n; v++ {
				if _, assigned := p.Cells[x.names[v]]; assigned {
					continue
				}
				affinity := 0
				for _, u := range members {
					affinity += weights[u][v]
				}
				if best < 0 || affinity > bestAffinity || affinity == bestAffinity && degree[v] > degree[best] {
					best, bestAffinity = v, affinity
				}
			}
			p.Cells[x.names[best]] = cell
			p.Sizes[cell]++
			remaining--
			members = append(members, best)
		}
	}
	for a := range weights {
		for b, w := range weights[a] {
			if a < b && p.Cells[x.names[a]] != p.Cells[x.names[b]] {
				p.CutWeight += w
			}
		}
	}
	return p
}
func checkPartition(t testing.TB, x *FlashIndex) {
	t.Helper()
	want := partitionSpecification(x)
	if e := x.partition(&flashWork{context.Background(), x}); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(x.snap.Partition, want) {
		t.Fatalf("partition differs: got%+v want%+v", x.snap.Partition, want)
	}
}
func TestFlashPartitionExhaustiveSmallGraphs(t *testing.T) {
	// Every simple undirected four-vertex graph, all possible cell counts.
	for mask := 0; mask < 64; mask++ {
		for cells := 1; cells <= 4; cells++ {
			cs := []model.Connection{}
			bit := 0
			for a := 0; a < 4; a++ {
				for b := a + 1; b < 4; b++ {
					if mask&(1<<bit) != 0 {
						cs = append(cs, model.Connection{From: fmt.Sprintf("s%03d", a), To: fmt.Sprintf("s%03d", b)})
					}
					bit++
				}
			}
			checkPartition(t, partitionCase(4, cells, cs, make([][]paper.Path, 4), 100000))
		}
	}
}
func TestFlashPartitionWeightedDirectedLayouts(t *testing.T) {
	for seed := int64(0); seed < 128; seed++ {
		rng := rand.New(rand.NewSource(seed))
		n := 1 + int(seed%40)
		cs := []model.Connection{}
		ps := make([][]paper.Path, n)
		for a := 0; a < n; a++ {
			for b := 0; b < n; b++ {
				if rng.Intn(13) == 0 {
					for count := 0; count < 1+rng.Intn(4); count++ {
						cs = append(cs, model.Connection{From: fmt.Sprintf("s%03d", a), To: fmt.Sprintf("s%03d", b)})
					}
				}
				if rng.Intn(17) == 0 {
					ps[a] = append(ps[a], paper.Path{To: fmt.Sprintf("s%03d", b), Duration: 1})
				}
			}
		}
		for _, cells := range []int{1, min(2, n), max(1, n/2), n} {
			checkPartition(t, partitionCase(n, cells, cs, ps, 1000000))
		}
		// Order of input edges must not change deterministic cell membership.
		rng.Shuffle(len(cs), func(i, j int) { cs[i], cs[j] = cs[j], cs[i] })
		checkPartition(t, partitionCase(n, min(3, n), cs, ps, 1000000))
	}
}
func TestFlashPartitionBoundedWorkAndFailure(t *testing.T) {
	cs := []model.Connection{}
	const n = 64
	for v := 1; v < n; v++ {
		cs = append(cs, model.Connection{From: fmt.Sprintf("s%03d", v-1), To: fmt.Sprintf("s%03d", v)})
	}
	x := partitionCase(n, 8, cs, make([][]paper.Path, n), 4*n)
	checkPartition(t, x)
	t.Logf("64-vertex path, 8 cells: %d charged selection/neighbor/seed steps; legacy full scans charged4096", x.snap.Stats.Work)
	for _, kind := range []string{"budget", "cancelled"} {
		x := partitionCase(n, 8, cs, make([][]paper.Path, n), 100000)
		ctx, cancel := context.WithCancel(context.Background())
		if kind == "budget" {
			x.cfg.Limits.MaxWork = 1
		} else {
			cancel()
		}
		e := x.partition(&flashWork{ctx, x})
		cancel()
		if e == nil || len(x.snap.Partition.Cells) != 0 {
			t.Fatal("partial partition accepted", kind, e)
		}
		if kind == "cancelled" && !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	}
}
func FuzzFlashPartitionSpecification(f *testing.F) {
	for _, seed := range []int64{0, 1, 71, 127} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed int64) {
		rng := rand.New(rand.NewSource(seed))
		n := 1 + rng.Intn(16)
		cs := []model.Connection{}
		for i := 0; i < 2*n; i++ {
			cs = append(cs, model.Connection{From: fmt.Sprintf("s%03d", rng.Intn(n)), To: fmt.Sprintf("s%03d", rng.Intn(n))})
		}
		checkPartition(t, partitionCase(n, 1+rng.Intn(n), cs, make([][]paper.Path, n), 100000))
	})
}
