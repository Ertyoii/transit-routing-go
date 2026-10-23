package graph

import (
	"context"
	"fmt"

	"github.com/ertyoii/transit-routing-go/walking"
)

type indexedEdge struct {
	to   int
	edge int
}

// ReachSession reuses search storage across closure requests on one immutable
// graph. It retains no paths, changes no graph state, and returns independently
// owned witnesses. Concurrent calls are serialized; different sessions never
// share mutable search storage. Copies of a session share its gate and scratch.
// Construct sessions with NewReachSession; an uninitialized session returns an error.
type ReachSession struct {
	graph   *Graph
	gate    chan struct{}
	scratch *reachScratch
}

// NewReachSession creates an initially empty closure session. Dense search
// storage is allocated lazily on its first valid request, not at graph creation.
func (g *Graph) NewReachSession() *ReachSession {
	return &ReachSession{graph: g, gate: make(chan struct{}, 1), scratch: &reachScratch{}}
}

// ReachWithTargets has the same reconstruction, work-budget, accessibility,
// absorbing-node, and cancellation contract as Graph.ReachWithTargets.
func (s *ReachSession) ReachWithTargets(ctx context.Context, r walking.ReachRequest, selected *TargetSet) (walking.ReachResult, error) {
	if e := ctx.Err(); e != nil {
		return walking.ReachResult{}, e
	}
	if s == nil || s.graph == nil || s.gate == nil || s.scratch == nil {
		return walking.ReachResult{}, fmt.Errorf("walking: uninitialized reach session; use NewReachSession")
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return walking.ReachResult{}, ctx.Err()
	}
	return s.graph.reachWithTargets(ctx, r, selected, s.scratch)
}

type denseEntry struct {
	node    int
	seconds int64
}

type reachScratch struct {
	dist       []int64
	prev       []int
	seen       []uint32
	generation uint32
	touched    []int
	queue      []denseEntry
}

func (s *reachScratch) reset(nodes int) {
	if len(s.dist) != nodes {
		s.dist = make([]int64, nodes)
		s.prev = make([]int, nodes)
		s.seen = make([]uint32, nodes)
	}
	s.generation++
	if s.generation == 0 {
		// Generation wrap must never make an ancient distance look reachable.
		clear(s.seen)
		s.generation = 1
	}
	s.touched = s.touched[:0]
	s.queue = s.queue[:0]
}

func denseLess(a, b denseEntry) bool {
	if a.seconds == b.seconds {
		// Node ordinals are canonical lexical IDs, exactly matching queue.Less.
		return a.node < b.node
	}
	return a.seconds < b.seconds
}
func (s *reachScratch) push(v denseEntry) {
	s.queue = append(s.queue, v)
	for i := len(s.queue) - 1; i > 0; {
		parent := (i - 1) / 2
		if !denseLess(s.queue[i], s.queue[parent]) {
			break
		}
		s.queue[i], s.queue[parent] = s.queue[parent], s.queue[i]
		i = parent
	}
}
func (s *reachScratch) pop() denseEntry {
	out := s.queue[0]
	last := len(s.queue) - 1
	s.queue[0] = s.queue[last]
	s.queue = s.queue[:last]
	for i := 0; ; {
		left := 2*i + 1
		if left >= len(s.queue) {
			break
		}
		child := left
		if right := left + 1; right < len(s.queue) && denseLess(s.queue[right], s.queue[left]) {
			child = right
		}
		if !denseLess(s.queue[child], s.queue[i]) {
			break
		}
		s.queue[i], s.queue[child] = s.queue[child], s.queue[i]
		i = child
	}
	return out
}

func (g *Graph) searchDense(ctx context.Context, from string, o walking.Options, s *reachScratch) (search, error) {
	s.reset(len(g.nodeIDs))
	origin := g.nodeIndex[from]
	s.dist[origin] = 0
	s.seen[origin] = s.generation
	s.touched = append(s.touched, origin)
	s.push(denseEntry{node: origin})
	stopAt := -1
	if o.StopAt != "" {
		stopAt = g.nodeIndex[o.StopAt]
	}
	settled, visits := 0, 0
	for len(s.queue) > 0 {
		if e := ctx.Err(); e != nil {
			return search{}, e
		}
		u := s.pop()
		if u.seconds != s.dist[u.node] {
			continue
		}
		settled++
		if settled > g.limits.MaxSettled {
			return search{}, walking.ErrBudget
		}
		if u.node == stopAt || o.AccessibleOnly && !g.accessible[u.node] {
			continue
		}
		for _, next := range g.indexedOut[u.node] {
			if e := ctx.Err(); e != nil {
				return search{}, e
			}
			visits++
			if visits > g.limits.MaxEdgeVisits {
				return search{}, walking.ErrBudget
			}
			edge := g.indexedEdges[next.edge]
			if o.AccessibleOnly && (edge.Accessible != walking.Yes || !g.accessible[next.to]) {
				continue
			}
			n := u.seconds + edge.Seconds
			if n > o.MaxSeconds {
				continue
			}
			if s.seen[next.to] != s.generation || n < s.dist[next.to] {
				if s.seen[next.to] != s.generation {
					s.touched = append(s.touched, next.to)
				}
				s.dist[next.to] = n
				s.prev[next.to] = next.edge
				s.seen[next.to] = s.generation
				s.push(denseEntry{node: next.to, seconds: n})
			}
		}
	}
	if e := ctx.Err(); e != nil {
		return search{}, e
	}
	return search{dense: s, graph: g}, nil
}
