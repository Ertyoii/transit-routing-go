package tripbased

import (
	"context"
	"testing"
)

// This generic storage test checks finite typed words, including repeated stops;
// routing feasibility and objective completeness are independently tested through
// BuildScalable in the external test package.
func TestClusterDAGFiniteWords(t *testing.T) {
	ctx := context.Background()
	x := newClusterPatterns(20, 100, 100, func(context.Context) error { return nil })
	words := [][]PatternEdge{
		{{"A", "B", "ride"}, {"B", "C", "walk"}, {"C", "T", "ride"}},
		{{"D", "C", "ride"}, {"C", "B", "ride"}, {"B", "T", "ride"}},
		{{"E", "B", "ride"}, {"B", "C", "walk"}, {"C", "T", "ride"}},
		{{"A", "B", "ride"}, {"B", "A", "ride"}, {"A", "T", "walk"}},
	}
	for _, w := range words {
		if e := x.add(ctx, 7, w); e != nil {
			t.Fatal(e)
		}
	}
	ds, s := x.snapshot()
	if s.RejectedMerges == 0 || s.OccurrenceDAGs != 1 {
		t.Fatal("cycle handling not exercised", s)
	}
	for _, d := range ds {
		// Independent transitive-matrix closure rejects any actual graph cycle.
		n := len(d.Stops)
		reach := make([][]bool, n)
		for i := range reach {
			reach[i] = make([]bool, n)
		}
		for _, a := range d.Arcs {
			reach[a.From][a.To] = true
		}
		for k := 0; k < n; k++ {
			for i := 0; i < n; i++ {
				for j := 0; j < n; j++ {
					reach[i][j] = reach[i][j] || reach[i][k] && reach[k][j]
				}
			}
		}
		for i := 0; i < n; i++ {
			if reach[i][i] {
				t.Fatal("cyclic storage")
			}
		}
	}
	for _, word := range words {
		found := false
		for _, d := range ds {
			at := map[int]bool{}
			for i, s := range d.Stops {
				if s == word[0].From {
					at[i] = true
				}
			}
			for _, e := range word {
				next := map[int]bool{}
				for _, a := range d.Arcs {
					if at[a.From] && d.Stops[a.From] == e.From && d.Stops[a.To] == e.To && a.Kind == e.Kind {
						next[a.To] = true
					}
				}
				at = next
			}
			found = found || len(at) > 0
		}
		if !found {
			t.Fatal("word lost", word)
		}
	}
}
