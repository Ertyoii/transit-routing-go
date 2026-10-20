package tripbased

import (
	"context"
	"reflect"
	"testing"
)

func TestCappedProfileWorkAccounting(t *testing.T) {
	failed := 0
	for _, c := range capCases(t) {
		cfg := capConfig(c)
		cfg.MaxProfileRecords = cfg.MaxProfileQueries
		x, e := BuildDestinationCappedPatterns(context.Background(), c.Dataset, cfg)
		if e != nil {
			t.Fatal(c.ID, e)
		}
		s := x.Snapshot()
		if s.Stats.NativeCompleted != s.Stats.NativeProfileQueries || s.Stats.NativeFailed != 0 || len(s.ProfileWork) != s.Stats.NativeProfileQueries || s.Stats.ProfileRecordsOmitted != 0 {
			t.Fatal(c.ID, s.Stats)
		}
		sum := Stats{}
		elapsed := int64(0)
		for _, w := range s.ProfileWork {
			if !w.Complete || w.Error != "" {
				t.Fatal(c.ID, w)
			}
			a, b := reflect.ValueOf(&sum).Elem(), reflect.ValueOf(w.Stats)
			for i := 0; i < a.NumField(); i++ {
				a.Field(i).SetInt(a.Field(i).Int() + b.Field(i).Int())
			}
			elapsed += w.ElapsedNS
		}
		if sum != s.Stats.NativeWork || sum.QueuedSegments != s.Stats.NativeQueuedSegments || elapsed != s.Stats.NativeElapsedNS {
			t.Fatal(c.ID, "incomplete aggregate", sum, s.Stats)
		}
		if len(s.ProfileWork) > 0 {
			s.ProfileWork[0].Source = "mutated"
			if x.Snapshot().ProfileWork[0].Source == "mutated" {
				t.Fatal("snapshot aliases index")
			}
		}
		cfg.MaxProfileRecords = 1
		y, e := BuildDestinationCappedPatterns(context.Background(), c.Dataset, cfg)
		if e != nil || !y.Complete() {
			t.Fatal(c.ID, e)
		}
		z := y.Snapshot()
		if len(z.ProfileWork) != 1 || z.Stats.ProfileRecordsOmitted != z.Stats.NativeProfileQueries-1 || z.Stats.NativeWork != sum {
			t.Fatal(c.ID, "telemetry cap changed work", z.Stats)
		}
		q, e := y.Search(context.Background(), c.Query)
		if e != nil || !reflect.DeepEqual(capVector(t, c.Dataset, c.Query, q.Journeys), c.Expected) {
			t.Fatal(c.ID, "bounded logging changed answer", e)
		}
		cfg.Patterns.Profile.Limits.MaxQuerySegments = 1
		cfg.MaxProfileRecords = cfg.MaxProfileQueries
		b, e := BuildDestinationCappedPatterns(context.Background(), c.Dataset, cfg)
		if e != nil && b.stats.NativeFailed > 0 {
			failed++
			z := b.Snapshot()
			last := z.ProfileWork[len(z.ProfileWork)-1]
			if z.Complete || last.Complete || last.Error == "" || z.Stats.NativeFailed != 1 || z.Stats.NativeCompleted+1 != z.Stats.NativeProfileQueries || z.Stats.NativeWork.QueuedSegments != z.Stats.NativeQueuedSegments {
				t.Fatal(c.ID, "partial call omitted", z.Stats)
			}
		}
	}
	if failed == 0 {
		t.Fatal("no partial-call boundary exercised")
	}
}
