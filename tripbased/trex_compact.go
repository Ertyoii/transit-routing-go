package tripbased

import "github.com/ertyoii/transit-routing-go/paper"

const trexEdgeChunkBits = 16
const trexEdgeChunkSize = 1 << trexEdgeChunkBits
const trexWideEvent = ^uint32(0)

// Ordinary retained transfers use 12 pointer-free bytes. Event IDs recover
// trip/position through immutable timetable tables; the path ID preserves the
// exact original Path pointer. The all-ones source event is a native escape,
// with its full native-edge index encoded in the remaining two words.
// No event, path, trip or boarding-index representability limit is added.
type trexPackedEdge struct{ from, to, walk uint32 }

type trexEdgeStore struct {
	chunks          [][]trexPackedEdge
	count, expected int
	paths           []*paper.Path
	wide            []trexEdge
}

type trexEdgeBuilder struct {
	store  *trexEdgeStore
	paths  map[*paper.Path]uint32
	last   *paper.Path
	lastID uint32
	lastOK bool
}

func (b *trexEdgeBuilder) pack(v trexEdge, from, to int) trexPackedEdge {
	const max = uint64(1<<32 - 1)
	if from >= 0 && uint64(from) < max && to >= 0 && uint64(to) <= max {
		if b.lastOK && b.last == v.walk {
			return trexPackedEdge{uint32(from), uint32(to), b.lastID}
		}
		if b.paths == nil {
			b.paths = make(map[*paper.Path]uint32)
		}
		if id, ok := b.paths[v.walk]; ok {
			b.last, b.lastID, b.lastOK = v.walk, id, true
			return trexPackedEdge{uint32(from), uint32(to), id}
		}
		if uint64(len(b.store.paths)) <= max {
			id := uint32(len(b.store.paths))
			b.store.paths = append(b.store.paths, v.walk)
			b.paths[v.walk] = id
			b.last, b.lastID, b.lastOK = v.walk, id, true
			return trexPackedEdge{uint32(from), uint32(to), id}
		}
	}
	id := uint64(len(b.store.wide))
	b.store.wide = append(b.store.wide, v)
	return trexEscapeEdge(id)
}

func trexEscapeEdge(id uint64) trexPackedEdge {
	return trexPackedEdge{trexWideEvent, uint32(id >> 32), uint32(id)}
}
func (v trexPackedEdge) wideID() uint64 { return uint64(v.to)<<32 | uint64(v.walk) }

func (s *trexEdgeStore) append(v trexPackedEdge) {
	chunk, at := s.count>>trexEdgeChunkBits, s.count&(trexEdgeChunkSize-1)
	if at == 0 {
		capacity := trexEdgeChunkSize
		if s.expected == 0 {
			// Append-mode tiny indexes must not pay for an entire large chunk.
			// Growth never replaces more than one bounded chunk.
			capacity = 16
		} else if s.expected-s.count < capacity {
			capacity = s.expected - s.count
		}
		s.chunks = append(s.chunks, make([]trexPackedEdge, 0, capacity))
	}
	if len(s.chunks[chunk]) == cap(s.chunks[chunk]) {
		capacity := 2 * cap(s.chunks[chunk])
		if capacity > trexEdgeChunkSize {
			capacity = trexEdgeChunkSize
		}
		grown := make([]trexPackedEdge, len(s.chunks[chunk]), capacity)
		copy(grown, s.chunks[chunk])
		s.chunks[chunk] = grown
	}
	s.chunks[chunk] = append(s.chunks[chunk], v)
	s.count++
}
func (s *trexEdgeStore) at(id int) trexPackedEdge {
	if id < 0 || id >= s.count {
		panic("T-REX internal: invalid transfer ID")
	}
	return s.chunks[id>>trexEdgeChunkBits][id&(trexEdgeChunkSize-1)]
}
func (s *trexEdgeStore) capacity() int {
	n := 0
	for _, c := range s.chunks {
		n += cap(c)
	}
	return n
}
func (x *TREXIndex) edgeCount() int { return x.edges.count }
func (x *TREXIndex) edgeAt(id int) trexEdge {
	v := x.edges.at(id)
	if v.from == trexWideEvent {
		return x.edges.wide[v.wideID()]
	}
	from, to := int(v.from), int(v.to)
	ft, tt := x.eventTrips[from], x.eventTrips[to]
	return trexEdge{ft, from - x.offsets[ft], tt, to - x.offsets[tt], x.edges.paths[v.walk]}
}

// Event-TB customization and ordinary overlay queries need the receiving
// event/path only. Avoid decoding unused source-trip metadata on that hot path.
func (x *TREXIndex) edgeTarget(id int) link {
	v := x.edges.at(id)
	if v.from == trexWideEvent {
		e := x.edges.wide[v.wideID()]
		return link{e.to, e.board, e.walk}
	}
	event := int(v.to)
	trip := x.eventTrips[event]
	return link{trip, event - x.offsets[trip], x.edges.paths[v.walk]}
}
func (o *trexOverlay) edgeAt(at int) int {
	if o.implicit {
		return at
	}
	if o.wide != nil {
		return o.wide[at]
	}
	return int(o.edges[at])
}
func (o *trexOverlay) length() int {
	if o.implicit {
		return o.implicitCount
	}
	if o.wide != nil {
		return len(o.wide)
	}
	return len(o.edges)
}
func (o *trexOverlay) capacity() int {
	if o.wide != nil {
		return cap(o.wide)
	}
	return cap(o.edges)
}
