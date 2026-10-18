package csa

import (
	"cmp"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
)

// Incoming vehicle state is fixed; all remaining constrained resources are
// compared componentwise before a prefix can serve as a dominance certificate.
type stateKey struct {
	stop, lastRide, lastConnection int
}

func labelJourney(p *label) model.Journey {
	legs := make([]model.Leg, p.depth)
	for c := p; c.parent != nil; c = c.parent {
		legs[c.depth-1] = c.leg
	}
	return model.Journey{Legs: legs}
}
func labelIdentity(p *label) string {
	if p.identity == "" {
		p.identity = model.Identity(labelJourney(p))
	}
	return p.identity
}
func compareLabels(a, b *label) int {
	for _, c := range []int{cmp.Compare(a.time, b.time), cmp.Compare(max(0, a.boards-1), max(0, b.boards-1)), cmp.Compare(a.walk, b.walk), cmp.Compare(a.depth, b.depth)} {
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(labelIdentity(a), labelIdentity(b))
}
func (l *lane) beyondArrival(t int64) bool {
	return l.config.ExactTopK > 0 && len(l.terminal) == l.config.ExactTopK && t > l.terminal[len(l.terminal)-1].time
}
func (l *lane) keepTerminal(p *label) {
	for _, old := range l.terminal {
		if labelIdentity(old) == labelIdentity(p) {
			return
		}
	}
	where := sort.Search(len(l.terminal), func(i int) bool { return compareLabels(l.terminal[i], p) >= 0 })
	if where >= l.config.ExactTopK {
		return
	}
	l.terminal = append(l.terminal, nil)
	copy(l.terminal[where+1:], l.terminal[where:])
	l.terminal[where] = p
	if len(l.terminal) > l.config.ExactTopK {
		l.terminal = l.terminal[:l.config.ExactTopK]
	}
}
func (l *lane) keepEquivalent(p *label) bool {
	key := stateKey{p.stop, p.lastRide, p.lastConnection}
	pool := l.states[key]
	if len(pool) == 0 {
		l.states[key] = []*label{p}
		return true
	}
	dominators := 0
	for _, other := range pool {
		if prefixDominates(other, p) {
			dominators++
			if dominators >= l.config.ExactTopK {
				return false
			}
		}
	}
	pool = append(pool, p)
	for _, old := range pool {
		if old == p || !prefixDominates(p, old) {
			continue
		}
		n := 0
		for _, other := range pool {
			if other != old && !other.discarded && prefixDominates(other, old) {
				n++
				if n >= l.config.ExactTopK {
					old.discarded = true
					l.result.Stats.EquivalentPruned++
					break
				}
			}
		}
	}
	kept := pool[:0]
	for _, other := range pool {
		if !other.discarded {
			kept = append(kept, other)
		}
	}
	for i := len(kept); i < len(pool); i++ {
		pool[i] = nil
	}
	l.states[key] = kept
	return true
}

// Any typed suffix feasible from b remains feasible from a. Earlier prefix time
// alone is insufficient: waiting can erase that advantage, so we also require
// non-worse transfer/walking/leg/identity order if both catch the same events.
func prefixDominates(a, b *label) bool {
	if a.time > b.time || a.walk > b.walk || a.block > b.block || a.boards > b.boards || a.depth > b.depth {
		return false
	}
	for _, c := range []int{cmp.Compare(max(0, a.boards-1), max(0, b.boards-1)), cmp.Compare(a.walk, b.walk), cmp.Compare(a.depth, b.depth)} {
		if c != 0 {
			return c < 0
		}
	}
	return labelIdentity(a) < labelIdentity(b)
}
