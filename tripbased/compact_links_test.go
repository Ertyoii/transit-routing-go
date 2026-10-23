package tripbased

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"unsafe"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

// Frozen native-int transfer emitter from the pre-compaction source. Topology
// construction is unchanged; retained rows here deliberately do not call any
// packed representation, pool, interning or unpacking helper. Paths are compared
// structurally because each construction owns its own walking closures.
func nativeTransferReference(ctx context.Context, d model.Dataset, cfg Config) ([][][]link, Preparation, error) {
	x, err := newIndex(ctx, d, cfg, false)
	if err != nil {
		return nil, Preparation{}, err
	}
	p := x.p
	rows := make([][][]link, len(x.trips))
	// Query-independent closure: no destination is absorbing during preparation.
	// Legacy Search requires egress >= intermediate walk so a path passing its
	// destination is dominated by terminating there. SearchCappedWalking instead
	// repairs affected transfers against its absorbing query closure.
	prepQuery := model.Query{AccessibleOnly: cfg.AccessibleOnly, TransferBuffer: cfg.TransferBuffer, Walking: &model.WalkingLimits{Transfer: cfg.TransferWalkingLimit}}
	walker := p.Walker(prepQuery)
	for ti, t := range x.trips {
		rows[ti] = make([][]link, len(t.cs))
		if !p.TripAllowed(prepQuery, t.id) {
			continue
		}
		for i, c := range t.cs {
			if err := ctx.Err(); err != nil {
				return nil, Preparation{}, err
			}
			if !c.Dropoff || !p.Accessible(prepQuery, c.To) {
				continue
			}
			before := walker.Count
			paths, err := walker.Paths(ctx, c.To)
			if err != nil {
				return nil, Preparation{}, err
			}
			if walker.Count != before {
				x.prep.WalkingPaths += len(paths)
			}
			for wi := range paths {
				w := &paths[wi]
				ready, allowed, err := p.ReadyTime(prepQuery, c.To, w.To, c.Arrival, w.Duration)
				if err != nil {
					return nil, Preparation{}, err
				}
				if !allowed {
					continue
				}
				for _, o := range x.at[w.To] {
					l := x.lines[o.line]
					prototype := x.trips[l.trips[0]]
					if !prototype.cs[o.position].Pickup || !p.TripAllowed(prepQuery, prototype.id) {
						continue
					}
					rank := sort.Search(len(l.trips), func(r int) bool { return x.trips[l.trips[r]].cs[o.position].Departure >= ready })
					if rank == len(l.trips) {
						continue
					}
					u := l.trips[rank]
					// i+1 is the alighting stop index; o.position is the boarding stop index.
					// Equal-time earlier identities may retain extra transfers, never remove more.
					if u == ti || o.line == t.line && rank >= t.rank && o.position >= i+1 {
						x.prep.SameLineDiscarded++
						continue
					}
					if x.prep.Transfers == cfg.MaxTransfers {
						return nil, Preparation{}, fmt.Errorf("Trip-Based preprocessing incomplete: transfer budget %d exhausted", cfg.MaxTransfers)
					}
					rows[ti][i] = append(rows[ti][i], link{u, o.position, w})
					x.prep.Transfers++
				}
			}
		}
	}
	// Diagnostics count only closures needed by transfer sources. Do not run
	// extra Dijkstra searches at unused stops solely to populate statistics.
	x.prep.WalkingClosures = walker.Count
	return rows, x.prep, nil
}

func TestCompactLinksLayoutIdentityAndNativeEscapes(t *testing.T) {
	if got := unsafe.Sizeof(compactLink{}); got != 12 {
		t.Fatalf("retained link is %d bytes, want 12", got)
	}
	typ := reflect.TypeOf(compactLink{})
	for i := 0; i < typ.NumField(); i++ {
		if k := typ.Field(i).Type.Kind(); k != reflect.Int32 && k != reflect.Uint32 {
			t.Fatalf("retained field is not pointer-free: %v", typ.Field(i))
		}
	}
	a, equalValue := &paper.Path{To: "B", Duration: 3}, &paper.Path{To: "B", Duration: 3}
	var data transferLinkData
	builder := transferLinkBuilder{data: &data}
	values := []link{{0, 0, nil}, {17, 31, a}, {0, 9, a}, {17, 31, equalValue}, {-1, 5, a}, {9, -1, equalValue}}
	if strconv.IntSize == 64 {
		maxTrip, maxBoard := uint64(1<<31-1), uint64(1<<32-1)
		values = append(values, link{int(maxTrip), int(maxBoard), a}, link{int(maxTrip + 1), 2, a}, link{3, int(maxBoard + 1), a}, link{int(^uint(0) >> 1), int(^uint(0) >> 1), nil})
	}
	stored := make([]compactLink, len(values))
	for i, want := range values {
		stored[i] = builder.pack(want)
		if got := data.unpack(stored[i]); got != want {
			t.Fatalf("%d: got %+v want %+v", i, got, want)
		}
	}
	if len(data.paths) != 3 || data.paths[0] != nil || data.paths[1] != a || data.paths[2] != equalValue {
		t.Fatal("pool coalesced values instead of preserving exact pointer identities", data.paths)
	}
	if stored[1].walk != stored[2].walk || stored[1].walk == stored[3].walk {
		t.Fatal("path interning changed pointer identity")
	}
	if stored[4].trip != -1 || stored[5].trip != -1 {
		t.Fatal("native-domain escape not retained")
	}
	// Escapes and pool growth cannot invalidate previously emitted links.
	for i, want := range values {
		if got := data.unpack(stored[i]); got != want {
			t.Fatal(i, got, want)
		}
	}
	t.Logf("native=%d retained=%d bytes, pointer-free retained fields", unsafe.Sizeof(link{}), unsafe.Sizeof(compactLink{}))
}

func TestCompactLinksCopiedIndexPoolOwnership(t *testing.T) {
	a, sentinel, b := &paper.Path{To: "A"}, &paper.Path{To: "sentinel"}, &paper.Path{To: "B"}
	paths := []*paper.Path{a, sentinel, sentinel, sentinel}
	wide := []link{{-1, 1, a}, {-9, 2, sentinel}, {-9, 2, sentinel}, {-9, 2, sentinel}}
	base := transferLinkData{paths: paths[:1], wide: wide[:1]}
	copied := base
	builder := transferLinkBuilder{data: &copied}
	first := builder.pack(link{1, 2, a})
	second := builder.pack(link{3, 4, b})
	escaped := builder.pack(link{-2, 7, b})
	if copied.unpack(first).walk != a || copied.unpack(second).walk != b || copied.unpack(escaped) != (link{-2, 7, b}) {
		t.Fatal("copied pool decode changed")
	}
	if len(base.paths) != 1 || len(base.wide) != 1 || paths[1] != sentinel || wide[1] != (link{-9, 2, sentinel}) {
		t.Fatal("copy appended into another owner's pool capacity")
	}
}

func TestCompactLinksNativeEmitterDifferential(t *testing.T) {
	type fixture struct {
		Dataset model.Dataset `json:"dataset"`
		Query   model.Query   `json:"query"`
	}
	cases := []fixture{}
	for _, c := range capCases(t) {
		cases = append(cases, fixture{c.Dataset, c.Query})
	}
	raw, err := os.ReadFile("testdata/exact_storage_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var storage []fixture
	if err = json.Unmarshal(raw, &storage); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, storage...)
	rowsCompared, linksCompared, budgetsCompared := 0, 0, 0
	for _, c := range cases {
		for _, strict := range []bool{false, true} {
			cfg := Config{StrictLineOrder: strict, TransferWalkingLimit: c.Query.Walking.Transfer, TransferBuffer: c.Query.TransferBuffer, AccessibleOnly: c.Query.AccessibleOnly, MaxTransfers: 100000, MaxQueuedSegments: 100000}
			native, prep, err := nativeTransferReference(context.Background(), c.Dataset, cfg)
			if err != nil {
				t.Fatal(c.Dataset.ID, err)
			}
			x, err := New(context.Background(), c.Dataset, cfg)
			if err != nil {
				t.Fatal(c.Dataset.ID, err)
			}
			if x.Preparation() != prep || len(x.links) != len(native) {
				t.Fatal(c.Dataset.ID, "preparation changed", x.Preparation(), prep)
			}
			for ti, exits := range native {
				for exit, want := range exits {
					row := x.links[ti][exit]
					if len(row) != len(want) || cap(row) != len(row) {
						t.Fatal(c.Dataset.ID, ti, exit, "row length/capacity changed")
					}
					for i, tr := range want {
						if got := x.linkData.unpack(row[i]); !reflect.DeepEqual(got, tr) {
							t.Fatalf("%s strict=%v [%d][%d][%d]: got %+v want %+v", c.Dataset.ID, strict, ti, exit, i, got, tr)
						}
						linksCompared++
					}
					rowsCompared++
				}
			}
			// Compare the exact transfer-budget error and fail-closed nil-index boundary.
			for _, limit := range []int{max(1, prep.Transfers-1), max(1, prep.Transfers), prep.Transfers + 1} {
				cfg.MaxTransfers = limit
				_, _, oldErr := nativeTransferReference(context.Background(), c.Dataset, cfg)
				got, newErr := New(context.Background(), c.Dataset, cfg)
				if fmt.Sprint(newErr) != fmt.Sprint(oldErr) || newErr != nil && got != nil {
					t.Fatal(c.Dataset.ID, limit, "budget contract changed", oldErr, newErr)
				}
				budgetsCompared++
			}
		}
	}
	t.Logf("%d frozen datasets, %d rows, %d links, %d exact budget boundaries", len(cases), rowsCompared, linksCompared, budgetsCompared)
}

func TestCompactLinksChunkOrderAndAppendIsolation(t *testing.T) {
	const count = 3 * 4101
	storage := transferLinkStorage{limit: count}
	var data transferLinkData
	builder := transferLinkBuilder{data: &data}
	rows := [][]compactLink{}
	path := &paper.Path{To: "B"}
	for row := 0; row < 3; row++ {
		storage.begin()
		for i := 0; i < 4101; i++ {
			storage.append(builder.pack(link{row*4101 + i, i, path}))
		}
		rows = append(rows, storage.finish())
	}
	if storage.emitted != count {
		t.Fatal(storage.emitted)
	}
	for r, row := range rows {
		if len(row) != 4101 || cap(row) != len(row) {
			t.Fatal(r, len(row), cap(row))
		}
		for i, v := range row {
			if got := data.unpack(v); got != (link{r*4101 + i, i, path}) {
				t.Fatal(r, i, got)
			}
		}
	}
	// Appending to a published row must allocate, not corrupt its neighbour.
	original := rows[1][0]
	_ = append(rows[0], compactLink{trip: 99})
	if rows[1][0] != original {
		t.Fatal("append overwrote adjacent published row")
	}
	storage.begin()
	if storage.finish() != nil {
		t.Fatal("empty row not nil")
	}
}

// The native benchmark retains the identical pre-compaction chunk algorithm.
type nativeTransferLinkStorage struct {
	data                  []link
	start, emitted, limit int
}

func (s *nativeTransferLinkStorage) begin() { s.start = len(s.data) }
func (s *nativeTransferLinkStorage) append(v link) {
	if len(s.data) == cap(s.data) {
		n := len(s.data) - s.start
		// No row can exceed the remaining declared transfer count. Multiplication
		// is bounded by the remaining count to avoid int overflow on huge limits.
		remaining := s.limit - s.emitted + n
		capacity := min(transferLinkChunk, remaining)
		if n >= capacity {
			capacity = remaining
			if n <= remaining/2 {
				capacity = 2 * n
			}
		}
		next := make([]link, n, capacity)
		copy(next, s.data[s.start:])
		s.data = next
		s.start = 0
	}
	s.data = append(s.data, v)
	s.emitted++
}
func (s *nativeTransferLinkStorage) finish() []link {
	if s.start == len(s.data) {
		return nil
	}
	return s.data[s.start:len(s.data):len(s.data)]
}

var compactBenchmarkRows [][]compactLink
var compactBenchmarkData transferLinkData
var nativeBenchmarkRows [][]link

// A bounded synthetic construction benchmark, not a dataset/query-latency claim.
// Both branches retain the same 32K links and row boundaries; the compact branch
// includes its path pool, identity map and all construction-local allocations.
func BenchmarkTransferLinkStorage(b *testing.B) {
	const rows, rowLength = 32, 1024
	for _, pathCount := range []int{1, 64, 4096} {
		paths := make([]paper.Path, pathCount)
		for i := range paths {
			paths[i].Duration = int64(i)
		}
		b.Run(fmt.Sprintf("paths_%d/native", pathCount), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				storage := nativeTransferLinkStorage{limit: rows * rowLength}
				out := make([][]link, rows)
				for r := range out {
					storage.begin()
					for i := 0; i < rowLength; i++ {
						storage.append(link{r, i, &paths[(r*rowLength+i)/(rows*rowLength/pathCount)%pathCount]})
					}
					out[r] = storage.finish()
				}
				nativeBenchmarkRows = out
			}
			b.ReportMetric(float64(rows*rowLength)*float64(unsafe.Sizeof(link{})), "link_payload_B")
		})
		b.Run(fmt.Sprintf("paths_%d/compact", pathCount), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				storage := transferLinkStorage{limit: rows * rowLength}
				var data transferLinkData
				builder := transferLinkBuilder{data: &data}
				out := make([][]compactLink, rows)
				for r := range out {
					storage.begin()
					for i := 0; i < rowLength; i++ {
						storage.append(builder.pack(link{r, i, &paths[(r*rowLength+i)/(rows*rowLength/pathCount)%pathCount]}))
					}
					out[r] = storage.finish()
				}
				compactBenchmarkRows, compactBenchmarkData = out, data
			}
			b.ReportMetric(float64(rows*rowLength)*float64(unsafe.Sizeof(compactLink{})), "link_payload_B")
			b.ReportMetric(float64(cap(compactBenchmarkData.paths))*float64(unsafe.Sizeof((*paper.Path)(nil))), "path_pool_B")
		})
	}
}

type compactCancelAfterChecks struct {
	context.Context
	checks int
}

func (c *compactCancelAfterChecks) Err() error {
	c.checks--
	if c.checks <= 0 {
		return context.Canceled
	}
	return nil
}
func TestCompactLinksCancellationBoundary(t *testing.T) {
	// A counting context exercises cancellation during topology, closure and
	// retained-transfer construction, not only an already-cancelled entry call.
	c := capCases(t)[45]
	cfg := Config{TransferWalkingLimit: c.Query.Walking.Transfer, TransferBuffer: c.Query.TransferBuffer, AccessibleOnly: c.Query.AccessibleOnly, MaxTransfers: 100000, MaxQueuedSegments: 100000}
	for _, checks := range []int{1, 2, 3, 5, 10, 20, 40, 80, 160, 1000000} {
		oldCtx := &compactCancelAfterChecks{Context: context.Background(), checks: checks}
		newCtx := &compactCancelAfterChecks{Context: context.Background(), checks: checks}
		_, _, oldErr := nativeTransferReference(oldCtx, c.Dataset, cfg)
		got, newErr := New(newCtx, c.Dataset, cfg)
		if fmt.Sprint(oldErr) != fmt.Sprint(newErr) || newErr != nil && got != nil || oldCtx.checks != newCtx.checks {
			t.Fatal(checks, "cancellation checks/outcome changed", oldErr, newErr, oldCtx.checks, newCtx.checks)
		}
	}
}
