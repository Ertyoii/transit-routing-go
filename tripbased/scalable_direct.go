package tripbased

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/paper"
)

type patternLinePair struct{ line, board, exit int }

// Store every usable pair of stop occurrences on each strict FIFO line once.
// Timetable departures remain in the line; no trip is sampled or omitted.
func (x *ScalableIndex) indexDirectPairs(ctx context.Context) error {
	p := x.local
	p.linePairs = map[[2]int][]patternLinePair{}
	for li, line := range p.base.cp.lines {
		cs := p.base.cp.trips[line.trips[0]].cs
		for i, a := range cs {
			for j := i; j < len(cs); j++ {
				if e := x.tick(ctx); e != nil {
					return e
				}
				if a.From == cs[j].To {
					continue
				}
				if x.snap.Stats.IndexedLinePairs >= x.cfg.MaxDirectPairs {
					return fmt.Errorf("Scalable TP incomplete: direct line-pair budget")
				}
				key := [2]int{p.base.ids[a.From], p.base.ids[cs[j].To]}
				p.linePairs[key] = append(p.linePairs[key], patternLinePair{li, i, j + 1})
				x.snap.Stats.IndexedLinePairs++
			}
		}
	}
	x.border.linePairs = p.linePairs
	return nil
}
func (x *PatternIndex) directIndexed(from, to int, ready int64, s *PatternQueryStats, tick func() error) (patternRide, bool, error) {
	return x.directPairList(x.linePairs[[2]int{from, to}], ready, s, tick)
}
func (x *PatternIndex) directPairList(pairs []patternLinePair, ready int64, s *PatternQueryStats, tick func() error) (patternRide, bool, error) {
	s.DirectLookups++
	best := paper.Infinity
	ride := patternRide{}
	if e := tick(); e != nil {
		return ride, false, e
	}
	for _, p := range pairs {
		if e := tick(); e != nil {
			return ride, false, e
		}
		s.LinePairs++
		line := x.base.cp.lines[p.line]
		lo, hi := 0, len(line.trips)
		for lo < hi {
			if e := tick(); e != nil {
				return ride, false, e
			}
			s.BinarySteps++
			m := lo + (hi-lo)/2
			if x.base.cp.trips[line.trips[m]].cs[p.board].Departure < ready {
				lo = m + 1
			} else {
				hi = m
			}
		}
		if lo == len(line.trips) {
			continue
		}
		ti := line.trips[lo]
		at := x.base.cp.trips[ti].cs[p.exit-1].Arrival
		if at < best {
			best = at
			ride = patternRide{ti, p.board, p.exit}
		}
	}
	return ride, best < paper.Infinity, nil
}
