package tripbased_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"reflect"
	"sync/atomic"
	"testing"
)

func snapshotFixture(branch bool) model.Dataset {
	d := data()
	d.Stops = []model.Stop{d.Stops[0], d.Stops[1], d.Stops[3]}
	trip(&d, "in", []string{"A", "B"}, 0, 1)
	trip(&d, "out", []string{"B", "D"}, 2, 3)
	if branch {
		d.Stops = append(d.Stops, model.Stop{ID: "X", Accessible: model.Yes})
		trip(&d, "branch", []string{"B", "X"}, 2, 4)
	}
	return d
}

type snapshotObservation struct {
	complete bool
	backend  tripbased.FlashSnapshot
	err      error
}

func observePreparation(t testing.TB, method string, ctx context.Context, d model.Dataset, c tripbased.FlashConfig, p tripbased.PatternConfig) snapshotObservation {
	t.Helper()
	var topComplete bool
	var topError string
	var backend tripbased.FlashSnapshot
	var buildError error
	q := flashQuery(query())
	var searchErr error
	var queryComplete bool
	var journeys int
	if method == "FLASH" {
		x, e := tripbased.BuildFlash(ctx, d, c)
		buildError = e
		a, b := x.Snapshot(), x.Snapshot()
		if !reflect.DeepEqual(a, b) {
			t.Fatal("Snapshot mutated diagnostic state")
		}
		if _, e = json.Marshal(a); e != nil {
			t.Fatal(e)
		}
		if !json.Valid(x.CostSummary()) {
			t.Fatal("invalid failure cost JSON")
		}
		topComplete, topError, backend = a.Complete, a.Error, a
		if buildError != nil {
			r, e := x.Search(context.Background(), q)
			searchErr = e
			queryComplete = r.Complete
			journeys = len(r.Journeys)
		}
	} else {
		p.Profile = c
		x, e := tripbased.BuildPatterns(ctx, d, p)
		buildError = e
		a, b := x.Snapshot(), x.Snapshot()
		if !reflect.DeepEqual(a, b) {
			t.Fatal("TP Snapshot mutated diagnostic state")
		}
		if _, e = json.Marshal(a); e != nil {
			t.Fatal(e)
		}
		if !json.Valid(x.CostSummary()) {
			t.Fatal("invalid TP failure cost JSON")
		}
		topComplete, topError, backend = a.Complete, a.Error, a.Backend
		if buildError != nil {
			r, e := x.Search(context.Background(), q)
			searchErr = e
			queryComplete = r.Complete
			journeys = len(r.Journeys)
		}
	}
	if topComplete != (buildError == nil) {
		t.Fatal("incorrect top-level completeness", method, buildError, topComplete)
	}
	if buildError != nil {
		if topError != buildError.Error() {
			t.Fatal("original error lost", topError, buildError)
		}
		if searchErr == nil || queryComplete || journeys != 0 {
			t.Fatal("incomplete index answered", method, searchErr, queryComplete, journeys)
		}
	}
	if diagnostic := backend.TransferDiagnostics; diagnostic != nil {
		switch diagnostic.RecordsStatus {
		case "transfer_index_incomplete":
			if diagnostic.TransferIndexComplete || len(backend.Transfers) != 0 || backend.OmittedTransferRecords != 0 {
				t.Fatal("unsafe partial offset expansion", backend)
			}
		case "flag_matrix_incomplete":
			if !diagnostic.TransferIndexComplete || diagnostic.FlagMatrixComplete || len(backend.Transfers) != 0 {
				t.Fatal("unsafe partial flags", backend)
			}
		case "snapshot_record_limit":
			if !diagnostic.TransferIndexComplete || !diagnostic.FlagMatrixComplete || len(backend.Transfers) != 0 || backend.OmittedTransferRecords <= 0 {
				t.Fatal("bad bounded omission", backend)
			}
		case "available":
			if !diagnostic.TransferIndexComplete || !diagnostic.FlagMatrixComplete || len(backend.Transfers) != backend.Stats.CanonicalTransfers {
				t.Fatal("false transfer availability", backend)
			}
		default:
			t.Fatal("missing diagnostic phase", diagnostic)
		}
	}
	return snapshotObservation{topComplete, backend, buildError}
}
func TestPartialSnapshotEveryWorkBoundary(t *testing.T) {
	d := snapshotFixture(false)
	for _, method := range []string{"FLASH", "TP"} {
		t.Run(method, func(t *testing.T) {
			c := ruleConfig(1)
			p := rulePatternConfig()
			full := observePreparation(t, method, context.Background(), d, c, p)
			if full.err != nil {
				t.Fatal(full.err)
			}
			maxWork := full.backend.Stats.Work
			statuses := map[string]int{}
			failures := 0
			for budget := 1; budget <= maxWork+1; budget++ {
				c.Limits.MaxWork = budget
				v := observePreparation(t, method, context.Background(), d, c, p)
				if v.err != nil {
					failures++
					if v.backend.TransferDiagnostics != nil {
						statuses[v.backend.TransferDiagnostics.RecordsStatus]++
					}
				}
			}
			if statuses["transfer_index_incomplete"] == 0 || statuses["flag_matrix_incomplete"] == 0 {
				t.Fatal("phase boundaries not exercised", statuses)
			}
			if method == "TP" && statuses["available"] == 0 {
				t.Fatal("no post-flags profile interruption", statuses)
			}
			t.Logf("%s MaxWork1..%d: %d incomplete builds safely snapshotted; phases%v", method, maxWork+1, failures, statuses)
		})
	}
}
func TestPartialSnapshotResourceCaps(t *testing.T) {
	d := snapshotFixture(true)
	caps := []struct {
		name string
		edit func(*tripbased.FlashConfig, *tripbased.PatternConfig)
	}{
		{"network-stops", func(c *tripbased.FlashConfig, _ *tripbased.PatternConfig) { c.Limits.MaxStops = 1 }},
		{"network-trips", func(c *tripbased.FlashConfig, _ *tripbased.PatternConfig) { c.Limits.MaxTrips = 1 }},
		{"closure", func(c *tripbased.FlashConfig, _ *tripbased.PatternConfig) { c.Limits.MaxClosurePaths = 1 }},
		{"walk-settled", func(c *tripbased.FlashConfig, _ *tripbased.PatternConfig) { c.Limits.MaxWalkSettled = 1 }},
		{"transfers", func(c *tripbased.FlashConfig, _ *tripbased.PatternConfig) { c.Limits.MaxTransfers = 1 }},
		{"flags", func(c *tripbased.FlashConfig, _ *tripbased.PatternConfig) { c.Limits.MaxFlagBytes = 1 }},
		{"suffix-bytes", func(c *tripbased.FlashConfig, _ *tripbased.PatternConfig) { c.Limits.MaxSuffixBytes = 1 }},
	}
	for _, method := range []string{"FLASH", "TP"} {
		for _, cap := range caps {
			t.Run(method+"/"+cap.name, func(t *testing.T) {
				c, p := ruleConfig(1), rulePatternConfig()
				cap.edit(&c, &p)
				if v := observePreparation(t, method, context.Background(), d, c, p); v.err == nil {
					t.Fatal("cap did not reject")
				}
			})
		}
	}
	for _, name := range []string{"runs", "patterns", "nodes", "edges", "direct-entries", "build-work"} {
		t.Run("TP/"+name, func(t *testing.T) {
			c, p := ruleConfig(1), rulePatternConfig()
			switch name {
			case "runs":
				c.Limits.MaxRuns = 1
			case "patterns":
				p.MaxPatterns = 1
			case "nodes":
				p.MaxNodes = 1
			case "edges":
				p.MaxEdges = 1
			case "direct-entries":
				p.MaxDirectEntries = 1
			case "build-work":
				p.MaxBuildWork = 1
			}
			if v := observePreparation(t, "TP", context.Background(), d, c, p); v.err == nil {
				t.Fatal("TP cap did not reject")
			}
		})
	}
}

// Cancellation is tied to cooperative Err checks, not wall-clock sleeps. The
// wrapped context remains concurrency-safe and cancellation stays permanent.
type checkpointCancellation struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int64
	at     int64
}

func (c *checkpointCancellation) Err() error {
	if c.checks.Add(1) >= c.at {
		c.cancel()
	}
	return c.Context.Err()
}
func cancelAt(at int64) *checkpointCancellation {
	ctx, cancel := context.WithCancel(context.Background())
	return &checkpointCancellation{Context: ctx, cancel: cancel, at: at}
}
func TestPartialSnapshotCancellationCheckpoints(t *testing.T) {
	d := snapshotFixture(true)
	for _, method := range []string{"FLASH", "TP"} {
		t.Run(method, func(t *testing.T) {
			count := cancelAt(1 << 60)
			full := observePreparation(t, method, count, d, ruleConfig(1), rulePatternConfig())
			count.cancel()
			if full.err != nil {
				t.Fatal(full.err)
			}
			checks := count.checks.Load()
			statuses := map[string]int{}
			for at := int64(1); at <= checks; at++ {
				ctx := cancelAt(at)
				v := observePreparation(t, method, ctx, d, ruleConfig(1), rulePatternConfig())
				ctx.cancel()
				if !errors.Is(v.err, context.Canceled) {
					t.Fatal("checkpoint did not cancel", at, v.err)
				}
				if v.backend.TransferDiagnostics != nil {
					statuses[v.backend.TransferDiagnostics.RecordsStatus]++
				}
			}
			if statuses["transfer_index_incomplete"] == 0 || statuses["flag_matrix_incomplete"] == 0 {
				t.Fatal("cancellation phases missing", statuses)
			}
			if method == "TP" && statuses["available"] == 0 {
				t.Fatal("no cancellation after ready graph/flags", statuses)
			}
			t.Logf("%s %d deterministic cancellation checkpoints; phases%v", method, checks, statuses)
		})
	}
}
func TestPartialSnapshotLimitAndOwnership(t *testing.T) {
	d := snapshotFixture(true)
	c := ruleConfig(1)
	c.Limits.MaxSnapshotTransfers = 1
	for _, method := range []string{"FLASH", "TP"} {
		v := observePreparation(t, method, context.Background(), d, c, rulePatternConfig())
		if v.err != nil || !v.complete || v.backend.TransferDiagnostics.RecordsStatus != "snapshot_record_limit" {
			t.Fatal("diagnostic cap changed routing completeness", method, v)
		}
	}
	x, e := tripbased.BuildFlash(context.Background(), d, ruleConfig(1))
	if e != nil {
		t.Fatal(e)
	}
	a := x.Snapshot()
	a.TransferDiagnostics.TransferIndexComplete = false
	a.TransferDiagnostics.RecordsStatus = "mutated"
	if b := x.Snapshot(); !b.TransferDiagnostics.TransferIndexComplete || b.TransferDiagnostics.RecordsStatus != "available" {
		t.Fatal("snapshot aliases phase state")
	}
}
func FuzzPartialPreparationSnapshots(f *testing.F) {
	for _, budget := range []uint16{1, 6, 7, 16, 32, 64, 128, 255} {
		f.Add(budget)
	}
	f.Fuzz(func(t *testing.T, budget uint16) {
		c := ruleConfig(1)
		c.Limits.MaxWork = 1 + int(budget%256)
		for _, method := range []string{"FLASH", "TP"} {
			observePreparation(t, method, context.Background(), snapshotFixture(false), c, rulePatternConfig())
		}
	})
}
