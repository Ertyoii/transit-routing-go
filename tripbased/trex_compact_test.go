package tripbased

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"unsafe"

	"github.com/ertyoii/transit-routing-go/paper"
)

func TestTREXCompactNativeBoundariesAndIdentity(t *testing.T) {
	if unsafe.Sizeof(trexPackedEdge{}) != 12 {
		t.Fatal("packed edge layout changed")
	}
	x := &TREXIndex{offsets: []int{0, 2, 4}, eventTrips: []int{0, 0, 1, 1}}
	b := trexEdgeBuilder{store: &x.edges}
	p, sameValue := &paper.Path{}, &paper.Path{}
	for _, v := range []trexEdge{{0, 1, 1, 0, p}, {0, 1, 1, 1, p}, {1, 0, 0, 1, sameValue}} {
		x.edges.append(b.pack(v, x.offsets[v.from]+v.exit, x.offsets[v.to]+v.board))
		got := x.edgeAt(x.edgeCount() - 1)
		if got != v {
			t.Fatal("native identity/indices changed", got, v)
		}
		if target := x.edgeTarget(x.edgeCount() - 1); target != (link{v.to, v.board, v.walk}) {
			t.Fatal("target decode changed", target, v)
		}
	}
	if len(x.edges.paths) != 2 {
		t.Fatal("interning merged equal-valued distinct paths")
	}
	for _, id := range []uint64{0, 1, 1<<32 - 1, 1 << 32, 1 << 40, 1<<63 - 1} {
		v := trexEscapeEdge(id)
		if v.from != trexWideEvent || v.wideID() != id {
			t.Fatal("native escape ID narrowed", v, id)
		}
	}
	pathBoundary := trexEdgeBuilder{store: &trexEdgeStore{}, paths: map[*paper.Path]uint32{p: ^uint32(0)}}
	if got := pathBoundary.pack(trexEdge{walk: p}, 0, 0); got.walk != ^uint32(0) || got.from == trexWideEvent {
		t.Fatal("path ID narrowed", got)
	}
	if strconv.IntSize == 64 {
		hi := int64(1 << 32)
		// Source's all-ones word is an escape; destination's all-ones word is legal.
		normal := b.pack(trexEdge{walk: p}, int(hi-2), int(hi-1))
		if normal.from == trexWideEvent || normal.to != ^uint32(0) {
			t.Fatal(normal)
		}
		for _, event := range []int{int(hi - 1), int(hi), int(1 << 40)} {
			v := trexEdge{int(1 << 40), int(1 << 36), int(1 << 39), int(1 << 35), p}
			x.edges.append(b.pack(v, event, 0))
			if got := x.edgeAt(x.edgeCount() - 1); got != v {
				t.Fatal("native escape narrowed", got, v)
			}
			if target := x.edgeTarget(x.edgeCount() - 1); target != (link{v.to, v.board, v.walk}) {
				t.Fatal("native target escape narrowed", target, v)
			}
		}
		o := trexOverlay{wide: []int{0, int(1 << 32), int(1 << 40)}}
		for i, want := range o.wide {
			if got := o.edgeAt(i); got != want {
				t.Fatal("overlay narrowed", got, want)
			}
		}
	}
}

func TestTREXCompactChunkOrderAndExactCapacity(t *testing.T) {
	for _, exact := range []bool{false, true} {
		n := trexEdgeChunkSize + 17
		s := trexEdgeStore{}
		if exact {
			s.expected = n
		}
		for i := 0; i < n; i++ {
			s.append(trexPackedEdge{uint32(i), uint32(i + 1), uint32(i + 2)})
		}
		for i := 0; i < n; i++ {
			if got := s.at(i); got != (trexPackedEdge{uint32(i), uint32(i + 1), uint32(i + 2)}) {
				t.Fatal(i, got)
			}
		}
		if exact && s.capacity() != n {
			t.Fatal("exact chunk capacity", s.capacity(), n)
		}
		if !exact && s.capacity() >= n+trexEdgeChunkSize {
			t.Fatal("unbounded last-chunk slack")
		}
	}
}

func TestTREXCompactRangesAndImplicitOverlay(t *testing.T) {
	d, _, _, cfg := fixedRankLiteral(t)
	x, e := BuildTREXFixedTransferCapRankedCandidate(context.Background(), d, cfg, 900)
	if e != nil {
		t.Fatal(e)
	}
	ids := []int{}
	for _, rows := range x.byEvent {
		for _, row := range rows {
			for id := row.begin; id < row.end; id++ {
				ids = append(ids, id)
			}
		}
	}
	want := make([]int, x.edgeCount())
	for i := range want {
		want[i] = i
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatal("event-major IDs changed", ids, want)
	}
	o := x.overlays[0]
	if !o.implicit || len(o.edges) != 0 || len(o.wide) != 0 || o.length() != x.edgeCount() {
		t.Fatal("level-zero IDs retained")
	}
	for i := 0; i < o.length(); i++ {
		if o.edgeAt(i) != i {
			t.Fatal(i)
		}
	}
	st := x.Diagnostic().Stats.Storage
	if st.EdgeBytes != uint64(x.edgeCount())*12 || st.AdjacencyCapacity != 0 || st.ImplicitOverlayEntries != x.edgeCount() || st.NativeEscapeBytes != 0 {
		t.Fatal(st)
	}
	t.Logf("layout edge=%d rank=%d adjacency=%d overlay=%d path_pool=%d event_trip=%d event_range=%d chunk_headers=%d transfers=%d", st.EdgeBytes, st.RankBytes, st.AdjacencyCapacity, st.OverlayBytes, st.PathPoolBytes, st.EventTripBytes, st.EventRangeBytes, st.ChunkHeaderBytes, x.edgeCount())
}
