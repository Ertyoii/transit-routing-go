package tripbased

import (
	"context"
	"encoding/json"
	"github.com/ertyoii/transit-routing-go/model"
	"os"
	"reflect"
	"testing"
)

func TestTREXExactStorageAndAccounting(t *testing.T) {
	var rows []struct {
		Dataset model.Dataset `json:"dataset"`
		Query   model.Query   `json:"query"`
		Level   int           `json:"level"`
	}
	b, e := os.ReadFile("testdata/exact_storage_cases.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &rows); e != nil {
		t.Fatal(e)
	}
	for n, r := range rows {
		cfg := TREXConfig{Levels: r.Level, MaxBoardings: 3, Limits: TREXLimits{MaxStops: 100, MaxTrips: 100, MaxInputEdges: 1000, MaxClosurePaths: 10000, MaxWalkSettled: 10000, MaxTransfers: 20000, MaxRuns: 10000, MaxWork: 2000000, MaxWitnesses: 10000, MaxOverlayEntries: 100000, MaxQueuedSegments: 100000, MaxQueryWork: 1000000}}
		a, e := BuildTREXStaticRulesCandidate(context.Background(), r.Dataset, cfg)
		if e != nil {
			t.Fatal(n, e)
		}
		cfg.ExactStorage = true
		x, e := BuildTREXStaticRulesCandidate(context.Background(), r.Dataset, cfg)
		if e != nil {
			t.Fatal(n, e)
		}
		sa, sx := a.Snapshot(), x.Snapshot()
		if !reflect.DeepEqual(sa.Transfers, sx.Transfers) || !reflect.DeepEqual(sa.Witnesses, sx.Witnesses) || !reflect.DeepEqual(a.byEvent, x.byEvent) || !reflect.DeepEqual(a.overlays, x.overlays) {
			t.Fatal(n, "graph/proof order changed")
		}
		st := sx.Stats.Storage
		m := len(sx.Transfers)
		if !st.TransferCountComplete || st.CountedTransfers != m || st.EdgeCapacity != m || st.RankCapacity != m || st.AdjacencyCapacity != m || st.OverlayCapacity != sx.Stats.OverlayEntries {
			t.Fatal(n, st)
		}
		if sx.Stats.Work != sa.Stats.Work+st.TransferCountWork+st.OverlayCountWork {
			t.Fatal(n, "count work not charged")
		}
		completed := 0
		for _, l := range sx.Stats.Levels {
			if !l.Complete || l.Started != l.Completed {
				t.Fatal(n, l)
			}
			completed += l.Completed
		}
		if completed != sx.Stats.EventRuns {
			t.Fatal(n, "completed-run accounting")
		}
		d := x.Diagnostic()
		if len(d.Stats.Levels) > 0 {
			d.Stats.Levels[0].Completed = -1
			if x.Diagnostic().Stats.Levels[0].Completed < 0 {
				t.Fatal("diagnostic aliases work rows")
			}
		}
		cert, e := PrepareTREXStaticCapCertificate(context.Background(), x, TREXStaticCapVerifyLimits{MaxStates: 200000, MaxJourneys: 20000, MaxScans: 4000000})
		if e != nil {
			t.Fatal(n, e)
		}
		ac, e := PrepareTREXStaticCapCertificate(context.Background(), a, TREXStaticCapVerifyLimits{MaxStates: 200000, MaxJourneys: 20000, MaxScans: 4000000})
		if e != nil {
			t.Fatal(n, e)
		}
		want, we := ac.Search(context.Background(), r.Query)
		got, ge := cert.Search(context.Background(), r.Query)
		if (we == nil) != (ge == nil) || !reflect.DeepEqual(want, got) {
			t.Fatal(n, "mandatory certificate outcome changed", we, ge)
		}
		cfg.Limits.MaxWork = sx.Stats.Work - 1
		y, e := BuildTREXStaticRulesCandidate(context.Background(), r.Dataset, cfg)
		if e == nil || y.Complete() {
			t.Fatal(n, "work guard accepted truncation")
		}
		if _, e = PrepareTREXStaticCapCertificate(context.Background(), y, TREXStaticCapVerifyLimits{MaxStates: 200000, MaxJourneys: 20000, MaxScans: 4000000}); e == nil {
			t.Fatal(n, "incomplete index certified")
		}
	}
}
