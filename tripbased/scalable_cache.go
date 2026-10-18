package tripbased

// Demand lookup caches topology, never a ready-time-dependent journey. The cache
// admits only while building and freezes before any query can observe it.
type DirectCacheStats struct {
	Keys     int `json:"keys"`
	Records  int `json:"records"` // keys plus occurrence records, including empty keys
	Hits     int `json:"hits"`
	Misses   int `json:"misses"`
	Rejected int `json:"rejected"`
}
type patternDirectCache struct {
	open  bool
	limit int
	pairs map[[2]int][]patternLinePair
	stats DirectCacheStats
}

func (x *PatternIndex) directDemand(from, to int, ready int64, s *PatternQueryStats, tick func() error) (patternRide, bool, error) {
	c := x.pairCache
	key := [2]int{from, to}
	if e := tick(); e != nil {
		return patternRide{}, false, e
	}
	if pairs, ok := c.pairs[key]; ok {
		s.DirectCacheHits++
		if c.open {
			c.stats.Hits++
		}
		return x.directPairList(pairs, ready, s, tick)
	}
	s.DirectCacheMisses++
	if c.open {
		c.stats.Misses++
	}
	if !c.open || c.stats.Records >= c.limit {
		return x.directIncidence(from, to, ready, s, tick)
	}
	a, b := x.incident[from], x.incident[to]
	pairs := []patternLinePair{}
	remaining := c.limit - c.stats.Records - 1
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if e := tick(); e != nil {
			return patternRide{}, false, e
		}
		if a[i].line < b[j].line {
			i++
			continue
		}
		if b[j].line < a[i].line {
			j++
			continue
		}
		ai, bj := i, j
		for i < len(a) && a[i].line == a[ai].line {
			i++
		}
		for j < len(b) && b[j].line == b[bj].line {
			j++
		}
		for _, u := range a[ai:i] {
			for _, v := range b[bj:j] {
				if e := tick(); e != nil {
					return patternRide{}, false, e
				}
				if u.pos >= v.pos {
					continue
				}
				if len(pairs) >= remaining {
					c.stats.Rejected++
					return x.directIncidence(from, to, ready, s, tick)
				}
				pairs = append(pairs, patternLinePair{u.line, u.pos, v.pos})
			}
		}
	}
	c.pairs[key] = pairs
	c.stats.Keys++
	c.stats.Records += 1 + len(pairs)
	return x.directPairList(pairs, ready, s, tick)
}
