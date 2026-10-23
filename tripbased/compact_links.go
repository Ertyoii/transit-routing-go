package tripbased

import "github.com/ertyoii/transit-routing-go/paper"

// compactLink is the immutable retained representation, not a query label.
// Ordinary links occupy 12 pointer-free bytes: a nonnegative trip index, a
// boarding position, and an ID in the owning index's immutable path pool.
// Decoding restores the original native-int link and the exact Path pointer.
// In particular, destination repairs remain query-local and never edit a pool.
type compactLink struct {
	trip        int32
	board, walk uint32
}

type transferLinkData struct {
	paths []*paper.Path
	wide  []link
}

func (d *transferLinkData) unpack(v compactLink) link {
	if v.trip >= 0 {
		return link{int(v.trip), int(v.board), d.paths[v.walk]}
	}
	// A negative trip is an escape, with the native-link index in the other
	// two words. The escape retains the full native domain without introducing
	// a dataset-size limit or silently narrowing an index/path count.
	return d.wide[uint64(v.board)<<32|uint64(v.walk)]
}

// transferLinkBuilder is construction-local. Its identity map is discarded
// after preparation; only one pointer per distinct retained Path stays alive.
// Paths are interned by pointer, not value, preserving ownership and identity.
type transferLinkBuilder struct {
	data     *transferLinkData
	paths    map[*paper.Path]uint32
	owned    bool
	lastPath *paper.Path
	lastID   uint32
	lastOK   bool
}

func (b *transferLinkBuilder) pack(v link) compactLink {
	// Transfer emission visits all receiving lines for one walking path at a
	// time. Repeated links for that path need neither a hash lookup nor any
	// additional allocation. Keep this small fast path inlineable.
	if uint64(v.trip) <= 1<<31-1 && uint64(v.board) <= 1<<32-1 && b.lastOK && v.walk == b.lastPath {
		return compactLink{int32(v.trip), uint32(v.board), b.lastID}
	}
	return b.packSlow(v)
}

func (b *transferLinkBuilder) packSlow(v link) compactLink {
	if !b.owned {
		// An Index may be shallow-copied for FLASH. Appending to its private pool
		// must not overwrite a neighbouring owner's unused backing capacity.
		b.data.paths = b.data.paths[:len(b.data.paths):len(b.data.paths)]
		b.data.wide = b.data.wide[:len(b.data.wide):len(b.data.wide)]
		b.owned = true
	}
	const maxTrip = uint64(1<<31 - 1)
	const maxWord = uint64(1<<32 - 1)
	if v.trip >= 0 && uint64(v.trip) <= maxTrip && v.board >= 0 && uint64(v.board) <= maxWord {
		if b.paths == nil {
			b.paths = make(map[*paper.Path]uint32, len(b.data.paths))
			for i, path := range b.data.paths {
				b.paths[path] = uint32(i)
			}
		}
		id, exists := b.paths[v.walk]
		if exists {
			b.lastPath, b.lastID, b.lastOK = v.walk, id, true
			return compactLink{int32(v.trip), uint32(v.board), id}
		}
		if uint64(len(b.data.paths)) <= maxWord {
			id = uint32(len(b.data.paths))
			b.data.paths = append(b.data.paths, v.walk)
			b.paths[v.walk] = id
			b.lastPath, b.lastID, b.lastOK = v.walk, id, true
			return compactLink{int32(v.trip), uint32(v.board), id}
		}
	}
	id := uint64(len(b.data.wide))
	b.data.wide = append(b.data.wide, v)
	return compactLink{-1, uint32(id >> 32), uint32(id)}
}
