package csa

import "container/heap"

const unreachableBound int64 = 1 << 62

type boundEntry struct {
	stop int
	time int64
}
type boundQueue []boundEntry

func (q boundQueue) Len() int           { return len(q) }
func (q boundQueue) Less(i, j int) bool { return q[i].time < q[j].time }
func (q boundQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *boundQueue) Push(x any)        { *q = append(*q, x.(boundEntry)) }
func (q *boundQueue) Pop() any          { old := *q; x := old[len(old)-1]; *q = old[:len(old)-1]; return x }

// Optimistic bounds ignore accessibility and total accumulated walk. If even
// this relaxation cannot finish after the last boarding, no real suffix can.
func (l *lane) prepareLastBoardingBound() {
	l.walkToDestination = make([]int64, len(l.index.data.Stops))
	for i := range l.walkToDestination {
		l.walkToDestination[i] = unreachableBound
	}
	destination := l.index.stops[l.query.Destination]
	l.walkToDestination[destination] = 0
	queue := &boundQueue{{destination, 0}}
	for queue.Len() > 0 {
		u := heap.Pop(queue).(boundEntry)
		if u.time != l.walkToDestination[u.stop] {
			continue
		}
		for _, w := range l.index.incomingWalks[u.stop] {
			from := l.index.stops[w.From]
			n := u.time + w.Duration
			if n < l.walkToDestination[from] {
				l.walkToDestination[from] = n
				heap.Push(queue, boundEntry{from, n})
			}
		}
	}
	if l.query.MaxBoardings == 0 {
		return
	}
	l.lastVehicleArrival = make([]int64, len(l.index.data.Connections))
	cap := l.query.MaxWalkSeconds
	if l.query.Walking != nil {
		cap = l.query.Walking.Egress
	}
	for _, chain := range l.index.chains {
		best := unreachableBound
		for i := len(chain) - 1; i >= 0; i-- {
			ci := chain[i]
			c := l.index.data.Connections[ci]
			walk := l.walkToDestination[l.index.stops[c.To]]
			if c.Dropoff && walk <= cap {
				best = min(best, c.Arrival+walk)
			}
			l.lastVehicleArrival[ci] = best
		}
	}
}
func (l *lane) lastBoardingDeadEnd(p *label) bool {
	if p.boards < l.query.MaxBoardings {
		return false
	}
	if p.lastConnection >= 0 {
		return l.lastVehicleArrival[p.lastConnection] > l.query.LatestArrival
	}
	walk := l.walkToDestination[p.stop]
	if walk == unreachableBound || p.time+walk > l.query.LatestArrival || p.walk+walk > l.query.MaxWalkSeconds {
		return true
	}
	if l.query.Walking != nil {
		cap := l.query.Walking.Egress
		if p.boards == 0 {
			cap = l.query.Walking.Direct
		}
		return p.block+walk > cap
	}
	return false
}
