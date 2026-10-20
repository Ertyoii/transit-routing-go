package tripbased

import (
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

// ruleTransfers retains the complete earliest-feasible line transfer set. This
// is an explicit conservative alternative to the unrestricted Trans-ULTRA
// reduction, not a claim that the latter's canonical proof now covers rules.
func (x *FlashIndex) transferCount() int {
	if x.cfg.Semantics == "static_stop_pair_v1" {
		return x.ruleTransferCount
	}
	return len(x.snap.Transfers)
}
func (x *FlashIndex) transferID(t, i, e int) int {
	if x.cfg.Semantics == "static_stop_pair_v1" {
		return x.ruleOffsets[t][i] + e
	}
	return x.transferIDs[t][i][e]
}
func (x *FlashIndex) ruleTransfers(w *flashWork) error {
	// The base owns immutable, already rule-checked links. Reuse them instead of
	// duplicating M links, M IDs and M string-heavy exported transfer records.
	x.cp.links = x.base.links
	x.ruleOffsets = make([][]int, len(x.base.trips))
	for ti, t := range x.base.trips {
		x.ruleOffsets[ti] = make([]int, len(t.cs))
		for i, links := range x.cp.links[ti] {
			x.ruleOffsets[ti][i] = x.ruleTransferCount
			for _, tr := range links {
				if e := w.tick(); e != nil {
					return e
				}
				from, to := t.cs[i], x.base.trips[tr.trip].cs[tr.board]
				if !from.Dropoff || !to.Pickup {
					return fmt.Errorf("FLASH-TB internal: invalid rule transfer endpoints")
				}
				ready, allowed, e := x.base.p.ReadyTime(model.Query{}, from.To, to.From, from.Arrival, tr.walk.Duration)
				if e != nil {
					return e
				}
				if !allowed || to.Departure < ready {
					return fmt.Errorf("FLASH-TB internal: invalid rule transfer readiness")
				}
				if x.ruleTransferCount >= x.cfg.Limits.MaxTransfers {
					return fmt.Errorf("FLASH-TB incomplete: rule transfer budget exhausted")
				}
				x.ruleTransferCount++
			}
		}
	}
	x.snap.Stats.CanonicalTransfers = x.ruleTransferCount // legacy alias, qualified by construction metadata
	x.ruleTransfersReady = true
	return nil
}

func (x *FlashIndex) ruleTransferDiagnostics() FlashTransferDiagnostics {
	d := FlashTransferDiagnostics{TransferIndexComplete: x.ruleTransfersReady, FlagMatrixComplete: x.ruleFlagsReady}
	switch {
	case !x.ruleTransfersReady:
		d.RecordsStatus = "transfer_index_incomplete"
	case !x.ruleFlagsReady:
		d.RecordsStatus = "flag_matrix_incomplete"
	case x.ruleTransferCount > x.cfg.Limits.MaxSnapshotTransfers:
		d.RecordsStatus = "snapshot_record_limit"
	default:
		d.RecordsStatus = "available"
	}
	return d
}

// Snapshot expansion is separately bounded; omission is explicit, never silent
// truncation of the routing graph. CostSummary does not expand these records.
func (x *FlashIndex) ruleTransferSnapshot() []FlashTransfer {
	// A partial count does not certify that every base-link offset or flag row
	// exists. Do not traverse or preallocate from an interrupted construction.
	if x.ruleTransferDiagnostics().RecordsStatus != "available" {
		return []FlashTransfer{}
	}
	out := make([]FlashTransfer, 0, x.ruleTransferCount)
	for ti, t := range x.cp.trips {
		for i, links := range x.cp.links[ti] {
			for j, tr := range links {
				raw := []string{}
				for _, e := range tr.walk.Edges {
					raw = append(raw, e.ID)
				}
				cells := []int{}
				id := x.transferID(ti, i, j)
				for cell := range x.flags {
					if x.flagged(id, cell) {
						cells = append(cells, cell)
					}
				}
				out = append(out, FlashTransfer{FromTrip: t.id, Exit: i + 1, ToTrip: x.cp.trips[tr.trip].id, Board: tr.board, WalkSeconds: tr.walk.Duration, RawEdges: raw, Cells: cells})
			}
		}
	}
	return out
}

// suffixFlags marks every cell reachable after boarding a transfer's receiving
// event, not just cells supported by one arbitrarily tied optimal witness.
// Positive rides make stay/transfer arcs a strict departure-time DAG. This
// conservative closure avoids unsafe stop-label/canonical dominance with rules.
// It remains a real destination-cell transfer predicate at query time.
func (x *FlashIndex) suffixFlags(w *flashWork) error {
	type event struct{ trip, pos int }
	starts := make([]int, len(x.cp.trips))
	events := []event{}
	for ti, t := range x.cp.trips {
		starts[ti] = len(events)
		for i := range t.cs {
			events = append(events, event{ti, i})
		}
	}
	words := (x.cfg.Cells + 63) / 64
	if len(events) > x.cfg.Limits.MaxSuffixBytes/8/words {
		return fmt.Errorf("FLASH-TB incomplete: suffix-state byte budget exhausted")
	}
	data := make([]uint64, len(events)*words)
	row := func(t, i int) []uint64 { return data[(starts[t]+i)*words : (starts[t]+i+1)*words] }
	sort.Slice(events, func(i, j int) bool {
		a, b := events[i], events[j]
		at, bt := x.cp.trips[a.trip].cs[a.pos].Departure, x.cp.trips[b.trip].cs[b.pos].Departure
		if at != bt {
			return at > bt
		}
		return x.eventOffset[a.trip]+a.pos < x.eventOffset[b.trip]+b.pos
	})
	for _, ev := range events {
		if e := w.tick(); e != nil {
			return e
		}
		t := x.cp.trips[ev.trip]
		c := t.cs[ev.pos]
		dst := row(ev.trip, ev.pos)
		if ev.pos+1 < len(t.cs) {
			copy(dst, row(ev.trip, ev.pos+1))
		}
		if c.Dropoff {
			for _, p := range x.walks[x.ids[c.To]] {
				if e := w.tick(); e != nil {
					return e
				}
				cell := x.snap.Partition.Cells[p.To]
				dst[cell/64] |= uint64(1) << uint(cell%64)
			}
		}
		for _, tr := range x.cp.links[ev.trip][ev.pos] {
			if e := w.tick(); e != nil {
				return e
			}
			if x.cp.trips[tr.trip].cs[tr.board].Departure <= c.Departure {
				return fmt.Errorf("FLASH-TB internal: suffix graph is not a positive-time DAG")
			}
			src := row(tr.trip, tr.board)
			for i, v := range src {
				if e := w.tick(); e != nil {
					return e
				}
				dst[i] |= v
			}
		}
	}
	for ti, exits := range x.cp.links {
		for i, links := range exits {
			for j, tr := range links {
				if e := w.tick(); e != nil {
					return e
				}
				id := x.transferID(ti, i, j)
				reachable := row(tr.trip, tr.board)
				for cell := 0; cell < x.cfg.Cells; cell++ {
					if e := w.tick(); e != nil {
						return e
					}
					if reachable[cell/64]&(uint64(1)<<uint(cell%64)) != 0 {
						x.flags[cell][id/64] |= uint64(1) << uint(id%64)
						x.snap.Stats.SetFlags++
					}
				}
			}
		}
	}
	x.ruleFlagsReady = true
	return nil
}

type ruleProfileEntry struct {
	board  int
	prefix *flashPath
	walk   *paper.Path
}
type ruleProfileArrival struct {
	at   int64
	path *flashPath
	walk *paper.Path
}

// Fresh event-context profile search. Boarding a trip resets transfer history;
// an earlier position on that exact trip dominates a later position at the same
// boarding count. No scalar stop label is used to prune transfer continuations.
func (x *FlashIndex) ruleProfile(source int, departure int64, w *flashWork) error {
	k := x.cfg.MaxBoardings
	_, windowEnd := x.ruleWindow()
	states := make([][]ruleProfileEntry, k+1)
	active := make([][]int, k+1)
	arrivals := make([][]ruleProfileArrival, k+1)
	for n := 1; n <= k; n++ {
		states[n] = make([]ruleProfileEntry, len(x.cp.trips))
		for ti, t := range x.cp.trips {
			if e := w.tick(); e != nil {
				return e
			}
			states[n][ti].board = len(t.cs)
		}
		arrivals[n] = make([]ruleProfileArrival, len(x.names))
		for i := range arrivals[n] {
			arrivals[n][i].at = paper.Infinity
		}
	}
	enqueue := func(n, ti, board int, prefix *flashPath, walk *paper.Path) error {
		if e := w.tick(); e != nil {
			return e
		}
		old := states[n][ti]
		if board >= old.board {
			return nil
		}
		if old.board == len(x.cp.trips[ti].cs) {
			active[n] = append(active[n], ti)
		}
		states[n][ti] = ruleProfileEntry{board, prefix, walk}
		return nil
	}
	for wi := range x.walks[source] {
		fp := &x.walks[source][wi]
		for _, o := range x.cp.at[fp.To] {
			if e := w.tick(); e != nil {
				return e
			}
			line := x.cp.lines[o.line]
			if !x.cp.trips[line.trips[0]].cs[o.position].Pickup {
				continue
			}
			rank := sort.Search(len(line.trips), func(i int) bool { return x.cp.trips[line.trips[i]].cs[o.position].Departure >= departure+fp.Duration })
			if rank < len(line.trips) {
				if e := enqueue(1, line.trips[rank], o.position, nil, fp); e != nil {
					return e
				}
			}
		}
	}
	for n := 1; n <= k; n++ {
		sort.Slice(active[n], func(i, j int) bool { return x.eventOffset[active[n][i]] < x.eventOffset[active[n][j]] })
		for _, ti := range active[n] {
			state := states[n][ti]
			t := x.cp.trips[ti]
			for i := state.board; i < len(t.cs); i++ {
				if e := w.tick(); e != nil {
					return e
				}
				c := t.cs[i]
				if c.Arrival > windowEnd {
					break
				}
				if !c.Dropoff {
					continue
				}
				path := &flashPath{state.prefix, state.walk, ti, state.board, i + 1}
				for wi := range x.walks[x.ids[c.To]] {
					if e := w.tick(); e != nil {
						return e
					}
					fp := &x.walks[x.ids[c.To]][wi]
					v := x.ids[fp.To]
					at := c.Arrival + fp.Duration
					if at <= windowEnd && at < arrivals[n][v].at {
						arrivals[n][v] = ruleProfileArrival{at, path, fp}
					}
				}
				if n == k {
					continue
				}
				for _, tr := range x.cp.links[ti][i] {
					if e := enqueue(n+1, tr.trip, tr.board, path, tr.walk); e != nil {
						return e
					}
				}
			}
		}
	}
	for target := range x.names {
		best := paper.Infinity
		if fp := x.walkMap[source][target]; fp != nil {
			best = departure + fp.Duration
		}
		for n := 1; n <= k; n++ {
			if e := w.tick(); e != nil {
				return e
			}
			a := arrivals[n][target]
			if a.at >= best {
				continue
			}
			best = a.at
			j, e := x.unpackPath(a.path, a.walk, departure, w)
			if e != nil {
				return e
			}
			if e = x.profileSink(FlashWitness{Source: x.names[source], Target: x.names[target], Departure: departure, Boardings: n, Journey: j}); e != nil {
				return e
			}
		}
	}
	return nil
}
