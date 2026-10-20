package tripbased

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"unsafe"

	"github.com/ertyoii/transit-routing-go/model"
)

func TestTREXProofEmptySnapshotPreservesNilShape(t *testing.T) {
	var zero TREXIndex
	if zero.Snapshot().Witnesses != nil {
		t.Fatal("zero-value snapshot changed nil witnesses to an empty array")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	x, err := BuildTREX(ctx, model.Dataset{}, TREXConfig{})
	if err != context.Canceled || x.Snapshot().Witnesses == nil {
		t.Fatal("constructed incomplete snapshot lost its owned empty witness array")
	}
}

func proofFixture(t *testing.T) *TREXIndex {
	t.Helper()
	d := model.Dataset{SchemaVersion: model.SchemaVersion, ID: "proof-storage", TimeZone: "Asia/Singapore", ServiceDate: "2026-10-16"}
	for _, id := range []string{"A", "B", "C", "D", "E", "F", "X"} {
		d.Stops = append(d.Stops, model.Stop{ID: id, Accessible: model.Unknown})
	}
	for _, id := range []string{"in", "out"} {
		d.Trips = append(d.Trips, model.Trip{ID: id, RouteID: id, Accessible: model.Unknown})
	}
	for i, c := range []model.Connection{
		{TripID: "in", From: "A", To: "B", Departure: 0, Arrival: 1},
		{TripID: "in", From: "B", To: "C", Departure: 2, Arrival: 3, Sequence: 1},
		{TripID: "out", From: "D", To: "E", Departure: 6, Arrival: 7},
		{TripID: "out", From: "E", To: "F", Departure: 8, Arrival: 9, Sequence: 1},
	} {
		c.ID = fmt.Sprint("c", i)
		c.Pickup = true
		c.Dropoff = true
		d.Connections = append(d.Connections, c)
	}
	d.Footpaths = []model.Footpath{{ID: "cx", From: "C", To: "X", Duration: 1, Accessible: model.Unknown}, {ID: "xd", From: "X", To: "D", Duration: 1, Accessible: model.Unknown}}
	cfg := TREXConfig{Levels: 1, MaxBoardings: 3, Partition: map[string]int{"A": 0, "B": 0, "C": 1, "X": 1, "D": 1, "E": 1, "F": 0}, Limits: TREXLimits{MaxStops: 100, MaxTrips: 100, MaxInputEdges: 1000, MaxClosurePaths: 1000, MaxWalkSettled: 1000, MaxTransfers: 1000, MaxRuns: 1000, MaxWork: 100000, MaxWitnesses: 30000, MaxOverlayEntries: 10000, MaxQueuedSegments: 10000, MaxQueryWork: 100000}}
	x, err := BuildTREX(context.Background(), d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// On-demand proofs must use owned timetable, walking and partition state.
	d.Connections[1].ID = "caller-mutated-connection"
	d.Connections[1].Arrival = 900
	d.Footpaths[0].ID = "caller-mutated-footpath"
	d.Footpaths[0].Duration = 99
	cfg.Partition["C"] = 0
	return x
}

// Literal nonzero boarding/exit metadata and two walking clocks are not
// derivable from a transfer ID alone. Snapshot copies must remain owned.
func TestTREXProofSnapshotLiteralAndOwnership(t *testing.T) {
	x := proofFixture(t)
	a := x.Snapshot()
	if len(a.Witnesses) != 1 {
		t.Fatalf("witness count %d", len(a.Witnesses))
	}
	want := TREXWitness{Level: 0, Cell: 1, SourceTrip: "in", SourceBoard: 1, ExitTrip: "out", ExitEvent: 1, Transfers: []int{0}, Journey: model.Journey{Legs: []model.Leg{
		{Kind: "ride", Ref: "c1", Departure: 2, Arrival: 3}, {Kind: "walk", Ref: "cx", Departure: 3, Arrival: 4}, {Kind: "walk", Ref: "xd", Departure: 4, Arrival: 5}, {Kind: "ride", Ref: "c2", Departure: 6, Arrival: 7}, {Kind: "ride", Ref: "c3", Departure: 8, Arrival: 9},
	}}}
	if !reflect.DeepEqual(a.Witnesses[0], want) {
		t.Fatalf("literal proof mismatch: got %+v want %+v", a.Witnesses[0], want)
	}
	if x.Diagnostic().RetainedWitnesses != 1 {
		t.Fatal("diagnostic lost proof count")
	}
	a.Witnesses[0].Transfers[0] = 999
	a.Witnesses[0].Journey.Legs[0].Ref = "mutated"
	if !reflect.DeepEqual(x.Snapshot().Witnesses[0], want) {
		t.Fatal("snapshot aliases proof")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				if !reflect.DeepEqual(x.Snapshot().Witnesses[0], want) || x.Diagnostic().RetainedWitnesses != 1 {
					t.Error("concurrent snapshot changed")
				}
			}
		}()
	}
	wg.Wait()
}

func TestTREXProofChunksPreserveOrderAndWideIntegers(t *testing.T) {
	var s trexProofStore
	max := int(^uint(0) >> 1)
	for i := 0; i < 1000; i++ {
		ids := make([]int, 31)
		for j := range ids {
			ids[j] = max - i - j
		}
		s.append(i%12, 129+i, max-i, ids)
		for j := range ids {
			ids[j] = 0
		}
	}
	if len(s.chunks) < 2 {
		t.Fatal("chunk rollover not exercised")
	}
	ordinal := 0
	for _, chunk := range s.chunks {
		if cap(chunk) != trexProofChunkBytes {
			t.Fatal("unbounded chunk capacity")
		}
		for at := 0; at < len(chunk); {
			p, n, e := decodeTREXProof(chunk[at:])
			if e != nil {
				t.Fatal(e)
			}
			if p.level != ordinal%12 || p.board != 129+ordinal || p.exit != max-ordinal || p.count != 31 {
				t.Fatal("record order/header changed", ordinal, p)
			}
			for j := 0; j < 31; j++ {
				if p.ids[j] != max-ordinal-j {
					t.Fatal("edge chain changed", ordinal, j)
				}
			}
			ordinal++
			at += n
		}
	}
	if ordinal != 1000 || s.count != ordinal {
		t.Fatal("record count mismatch", ordinal, s.count)
	}
}

func TestTREXProofRejectsMalformedEncodingAndGraphReferences(t *testing.T) {
	literal := []byte{0, 1, 1, 1, 0}
	for cut := 0; cut < len(literal); cut++ {
		if _, _, err := decodeTREXProof(literal[:cut]); err == nil {
			t.Fatal("accepted truncation", cut)
		}
	}
	for _, bad := range [][]byte{bytes.Repeat([]byte{255}, 11), {0, 0, 0, 0}, {0, 0, 0, 32}, {0, 0, 0, 1, 255}} {
		if _, _, err := decodeTREXProof(bad); err == nil {
			t.Fatal("accepted malformed encoding", bad)
		}
	}
	x := proofFixture(t)
	p, _, err := decodeTREXProof(literal)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*trexProof){func(p *trexProof) { p.level = 1 }, func(p *trexProof) { p.board = 2 }, func(p *trexProof) { p.exit = 2 }, func(p *trexProof) { p.ids[0] = len(x.edges) }, func(p *trexProof) { p.count = 0 }} {
		bad := p
		change(&bad)
		if _, err := x.expandTREXProof(bad); err == nil {
			t.Fatal("accepted malformed graph reference", bad)
		}
	}
}

// Synthetic repetition exercises real chunk rollover and public expansion;
// it is not a Singapore memory or construction benchmark.
func TestTREXProofMultichunkSnapshotAndCapacity(t *testing.T) {
	x := proofFixture(t)
	want := x.Snapshot().Witnesses[0]
	var s trexProofStore
	for i := 0; i < 20000; i++ {
		s.append(want.Level, want.SourceBoard, want.ExitEvent, want.Transfers)
	}
	x.proofs = s
	snap := x.Snapshot()
	if len(snap.Witnesses) != 20000 || x.Diagnostic().RetainedWitnesses != 20000 {
		t.Fatal("multichunk count lost")
	}
	for i, w := range snap.Witnesses {
		if !reflect.DeepEqual(w, want) {
			t.Fatal("multichunk snapshot changed", i)
		}
	}
	packed, capacity := 0, 0
	for _, chunk := range s.chunks {
		packed += len(chunk)
		capacity += cap(chunk)
	}
	if packed != 100000 || capacity != 2*trexProofChunkBytes {
		t.Fatal("unexpected compact footprint", packed, capacity)
	}
	expandedPayload := 20000 * (int(unsafe.Sizeof(TREXWitness{})) + len(want.Journey.Legs)*int(unsafe.Sizeof(model.Leg{})) + len(want.Transfers)*int(unsafe.Sizeof(int(0))))
	t.Logf("synthetic proofs=20000 packed_bytes=%d chunk_capacity_bytes=%d expanded_logical_payload_bytes=%d; excludes chunk slice metadata, allocator rounding and immutable graph", packed, capacity, expandedPayload)
}

func FuzzTREXProofDecode(f *testing.F) {
	for _, b := range [][]byte{{0, 1, 1, 1, 0}, {11, 128, 1, 127, 2, 0, 1}, {}, {255, 255, 255}} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		p, n, err := decodeTREXProof(b)
		if err == nil {
			if n < 5 || n > len(b) || p.count < 1 || p.count > 31 || p.level < 0 || p.board < 0 || p.exit < 0 {
				t.Fatal("decoder returned invalid descriptor")
			}
			for _, id := range p.ids[:p.count] {
				if id < 0 {
					t.Fatal("negative id")
				}
			}
		}
	})
}
