package tripbased

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

func TestWalkingPrefixCapacityLeafSharingAndAppendOwnership(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, branch := range []bool{false, true} {
			names := []string{"A", "B", "C", "D", "E", "X"}
			d := model.Dataset{}
			for i := 0; i < 4; i++ {
				from, to := names[i], names[i+1]
				if reverse {
					from, to = to, from
				}
				d.Footpaths = append(d.Footpaths, model.Footpath{ID: fmt.Sprintf("e%d", i), From: from, To: to, Duration: 1, Accessible: model.Yes})
			}
			if branch {
				d.Footpaths = append(d.Footpaths, model.Footpath{ID: "branch", From: "B", To: "X", Duration: 1, Accessible: model.Yes})
			}
			x := &FlashIndex{names: names, ids: map[string]int{}, cfg: FlashConfig{Limits: FlashLimits{MaxWork: 10000, MaxWalkSettled: 1000, MaxClosurePaths: 1000}}}
			for i, name := range names {
				x.ids[name] = i
			}
			if e := x.closeWalks(d, &flashWork{context.Background(), x}); e != nil {
				t.Fatal(e)
			}
			for source, row := range x.walks {
				unique := map[*model.Footpath]int{}
				logical := 0
				for i, p := range row {
					if cap(p.Edges) != len(p.Edges) {
						t.Fatal("shared prefix capacity escapes", source, p)
					}
					if x.walkMap[source][x.ids[p.To]] != &x.walks[source][i] {
						t.Fatal("lost distinct stable Path identity")
					}
					logical += len(p.Edges)
					if len(p.Edges) > 0 {
						key := &p.Edges[0]
						unique[key] = max(unique[key], len(p.Edges))
					}
					before, _ := json.Marshal(x.walks)
					copyOfHeader := p.Edges
					copyOfHeader = append(copyOfHeader, model.Footpath{ID: "caller-append"})
					copyOfHeader[len(copyOfHeader)-1].ID = "caller-mutated-new-element"
					after, _ := json.Marshal(x.walks)
					if string(before) != string(after) {
						t.Fatal("append wrote into a shared suffix")
					}
				}
				retained := 0
				for _, n := range unique {
					retained += n
				}
				children := map[string]int{}
				for _, p := range row {
					if len(p.Edges) > 0 {
						children[p.Edges[len(p.Edges)-1].From]++
					}
				}
				leaves, leafSlots := 0, 0
				for _, p := range row {
					if len(p.Edges) > 0 && children[p.To] == 0 {
						leaves++
						leafSlots += len(p.Edges)
					}
				}
				if retained > logical || len(unique) != leaves || retained != leafSlots {
					t.Fatal("retained arrays must be exactly the predecessor leaves", source, retained, logical, len(unique), leaves, leafSlots)
				}
			}
		}
	}
}

func TestWalkingPrefixPublishedWordsConcurrentReadOnly(t *testing.T) {
	d, q, _, cfg := fixedRankLiteral(t)
	// Extend the walking predecessor tree with immutable multi-hop prefixes.
	d.Stops = append(d.Stops, model.Stop{ID: "prefix-tail-1", Accessible: model.Yes}, model.Stop{ID: "prefix-tail-2", Accessible: model.Yes})
	d.Footpaths = append(d.Footpaths,
		model.Footpath{ID: "prefix-edge-1", From: q.Origin, To: "prefix-tail-1", Duration: 1, Accessible: model.Yes},
		model.Footpath{ID: "prefix-edge-2", From: "prefix-tail-1", To: "prefix-tail-2", Duration: 1, Accessible: model.Yes})
	cfg.Partition["prefix-tail-1"] = cfg.Partition[q.Origin]
	cfg.Partition["prefix-tail-2"] = cfg.Partition[q.Origin]
	x, e := BuildTREXStaticRulesCandidate(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	q.MaxWalkSeconds = model.MaxServiceSecond
	q.Walking = &model.WalkingLimits{Access: 1000, Transfer: 1000, Egress: 1000, Direct: 1000}
	before, _ := json.Marshal(x.Snapshot())
	wordsBefore, _ := json.Marshal(x.walks)
	want, e := x.Search(context.Background(), q)
	if e != nil || !want.Complete {
		t.Fatal(want, e)
	}
	// Input ownership: closure copies values, not the caller's mutable slice.
	for i := range d.Footpaths {
		d.Footpaths[i].ID = "caller-mutation"
		d.Footpaths[i].Duration = 123
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				got, e := x.Search(context.Background(), q)
				if e != nil || !reflect.DeepEqual(got, want) {
					t.Error("concurrent query changed", got, e)
				}
				for _, row := range x.walks {
					for _, p := range row {
						_ = p.Append((*paper.Node)(nil), 0)
					}
				}
				s := x.Snapshot()
				for k := range s.Transfers {
					for j := range s.Transfers[k].RawEdges {
						s.Transfers[k].RawEdges[j] = "caller-owned"
					}
				}
				for k := range s.Witnesses {
					for j := range s.Witnesses[k].Journey.Legs {
						s.Witnesses[k].Journey.Legs[j].Ref = "caller-owned"
					}
				}
			}
		}()
	}
	wg.Wait()
	after, _ := json.Marshal(x.Snapshot())
	wordsAfter, _ := json.Marshal(x.walks)
	if string(before) != string(after) || string(wordsBefore) != string(wordsAfter) {
		t.Fatal("input/snapshot/query aliased prepared walking words")
	}
}

func TestWalkingPrefixZeroNegativeAndPositiveSelfQualification(t *testing.T) {
	d, _, _, cfg := fixedRankLiteral(t)
	for _, duration := range []int64{0, -1} {
		bad := d.Clone()
		bad.Footpaths = append(bad.Footpaths, model.Footpath{ID: "invalid-zero-negative", From: d.Stops[0].ID, To: d.Stops[0].ID, Duration: duration, Accessible: model.Yes})
		if x, e := BuildTREXStaticRulesCandidate(context.Background(), bad, cfg); e == nil || x.Snapshot().Complete {
			t.Fatal("unqualified nonpositive edge admitted", duration, e)
		}
	}
	good := d.Clone()
	good.Footpaths = append(good.Footpaths, model.Footpath{ID: "positive-self", From: d.Stops[0].ID, To: d.Stops[0].ID, Duration: 1, Accessible: model.Yes})
	x, e := BuildTREXStaticRulesCandidate(context.Background(), good, cfg)
	if e != nil {
		t.Fatal(e)
	}
	for source := range x.walks {
		p := x.walkMap[source][source]
		if p == nil || p.Duration != 0 || len(p.Edges) != 0 || p.Edges == nil {
			t.Fatal("zero stay changed", source, p)
		}
	}
}
