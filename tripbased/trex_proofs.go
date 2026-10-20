package tripbased

import (
	"encoding/binary"
	"fmt"
)

const trexProofChunkBytes = 64 * 1024
const trexProofMaxTransfers = 31 // Public constructors accept at most 32 boardings.

// Only proof descriptors persist. Records never cross chunks; their transfer
// count is their delimiter. Fixed chunks avoid replacement giant byte arrays.
// The input graph owns all referenced timetable and walking events.
type trexProofStore struct {
	chunks [][]byte
	count  int
}

type trexProof struct {
	level, board, exit, count int
	ids                       [trexProofMaxTransfers]int
}

func (s *trexProofStore) append(level, board, exit int, ids []int) {
	// Called after the unchanged unpack/tick/promotion sequence has succeeded.
	// Bounds follow from that sequence and the constructor's boarding limit.
	if level < 0 || board < 0 || exit < 0 || len(ids) < 1 || len(ids) > trexProofMaxTransfers {
		panic("T-REX internal: invalid proof descriptor")
	}
	var buf [(4 + trexProofMaxTransfers) * binary.MaxVarintLen64]byte
	used := 0
	put := func(v int) {
		if v < 0 {
			panic("T-REX internal: negative proof transfer")
		}
		used += binary.PutUvarint(buf[used:], uint64(v))
	}
	put(level)
	put(board)
	put(exit)
	put(len(ids))
	for _, id := range ids {
		put(id)
	}
	n := len(s.chunks)
	if n == 0 || cap(s.chunks[n-1])-len(s.chunks[n-1]) < used {
		s.chunks = append(s.chunks, make([]byte, 0, trexProofChunkBytes))
		n++
	}
	s.chunks[n-1] = append(s.chunks[n-1], buf[:used]...)
	s.count++
}

// The private encoding is never an input API. Check malformed/truncated values
// nevertheless, so an internal invariant failure cannot silently omit proofs.
func decodeTREXProof(b []byte) (p trexProof, used int, err error) {
	get := func() (int, error) {
		v, n := binary.Uvarint(b[used:])
		if n <= 0 || v > uint64(^uint(0)>>1) {
			return 0, fmt.Errorf("T-REX internal: truncated or overflowing proof")
		}
		used += n
		return int(v), nil
	}
	for _, field := range []*int{&p.level, &p.board, &p.exit, &p.count} {
		*field, err = get()
		if err != nil {
			return
		}
	}
	if p.count < 1 || p.count > trexProofMaxTransfers {
		return p, used, fmt.Errorf("T-REX internal: invalid proof transfer count")
	}
	for i := 0; i < p.count; i++ {
		p.ids[i], err = get()
		if err != nil {
			return
		}
	}
	return
}

func (x *TREXIndex) expandTREXProof(p trexProof) (TREXWitness, error) {
	bad := func() (TREXWitness, error) {
		return TREXWitness{}, fmt.Errorf("T-REX internal: invalid proof graph reference")
	}
	if p.level < 0 || p.level >= x.cfg.Levels || p.count < 1 || p.count > trexProofMaxTransfers {
		return bad()
	}
	for _, id := range p.ids[:p.count] {
		if id < 0 || id >= len(x.edges) {
			return bad()
		}
	}
	first := x.edges[p.ids[0]]
	source := first.from
	if source < 0 || source >= len(x.base.trips) || p.board < 0 || p.board >= len(x.base.trips[source].cs) {
		return bad()
	}
	node := &trexNode{trip: source, board: p.board, edge: -1}
	for _, id := range p.ids[:p.count] {
		e := x.edges[id]
		if e.from != node.trip || e.exit < node.board || e.exit >= len(x.base.trips[node.trip].cs) || e.to < 0 || e.to >= len(x.base.trips) || e.board < 0 || e.board >= len(x.base.trips[e.to].cs) {
			return bad()
		}
		node = &trexNode{trip: e.to, board: e.board, edge: id, parent: node}
	}
	if p.exit < node.board || p.exit >= len(x.base.trips[node.trip].cs) {
		return bad()
	}
	c := x.base.trips[source].cs[p.board]
	stop, ok := x.ids[c.To]
	if !ok || stop < 0 || stop >= len(x.cells) {
		return bad()
	}
	// Snapshot is an explicit expansion, not construction or query work. The
	// unchanged unpacker produces freshly owned legs and ordered transfer IDs.
	j, ids, err := x.unpackTREX(node, p.exit, nil, c.Departure, func() error { return nil })
	if err != nil {
		return TREXWitness{}, err
	}
	return TREXWitness{p.level, x.cells[stop] >> uint(p.level), x.base.trips[source].id, p.board, x.base.trips[node.trip].id, p.exit, ids, j}, nil
}

func (x *TREXIndex) expandTREXProofs() []TREXWitness {
	// Preserve the zero-value Snapshot's null shape; constructors initialize an
	// owned empty witness slice even when construction immediately fails.
	if x.snap.Witnesses == nil && x.proofs.count == 0 {
		return nil
	}
	out := make([]TREXWitness, 0, x.proofs.count)
	for _, chunk := range x.proofs.chunks {
		for at := 0; at < len(chunk); {
			p, used, err := decodeTREXProof(chunk[at:])
			if err != nil {
				panic(err)
			}
			w, err := x.expandTREXProof(p)
			if err != nil {
				panic(err)
			}
			out = append(out, w)
			at += used
		}
	}
	if len(out) != x.proofs.count {
		panic("T-REX internal: proof count mismatch")
	}
	return out
}
