package tripbased_test

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"reflect"
	"testing"
)

// Multiple large exit rows force both chunk rollover and copying an unfinished
// row. A lost prefix, overlap with the next row, or changed emission order must
// preserve neither these known itineraries nor the complete transfer count.
func TestTransferStorageLargeRows(t *testing.T) {
	const receivers = 4101
	d := model.Dataset{SchemaVersion: "0.2", ID: "transfer-storage-large-rows", TimeZone: "Asia/Singapore", ServiceDate: "2026-10-17"}
	for _, s := range []string{"A", "B", "C"} {
		d.Stops = append(d.Stops, model.Stop{ID: s, Accessible: model.Yes})
	}
	trip(&d, "s0", []string{"A", "B"}, 1, 2)
	trip(&d, "s1", []string{"A", "B"}, 3, 4)
	trip(&d, "s2", []string{"C", "B"}, 5, 6)
	for i := 0; i < receivers; i++ {
		dst := fmt.Sprintf("D%04d", i)
		d.Stops = append(d.Stops, model.Stop{ID: dst, Accessible: model.Yes})
		trip(&d, fmt.Sprintf("r%04d", i), []string{"B", dst}, 10, 20)
	}
	cfg := tripbased.Config{StrictLineOrder: true, MaxTransfers: 3 * receivers, MaxQueuedSegments: 20000}
	x, e := tripbased.New(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if x.Preparation().Transfers != 3*receivers {
		t.Fatal(x.Preparation())
	}
	for _, origin := range []string{"A", "C"} {
		for _, dep := range []int64{0, 2} {
			for _, n := range []int{0, 1, 4094, 4095, 4096, 4100} {
				q := model.Query{ID: "storage", Origin: origin, Destination: fmt.Sprintf("D%04d", n), Departure: dep, LatestArrival: 20, MaxBoardings: 2, MaxLegs: 2, Walking: &model.WalkingLimits{}}
				r, e := x.SearchCappedWalking(context.Background(), q)
				if e != nil || !r.Complete {
					t.Fatalf("%+v: %+v %v", q, r, e)
				}
				got := objectives(reconstructed(t, d, q, r.Journeys), 2)
				if !reflect.DeepEqual(got, []int64{inf, inf, 20}) {
					t.Fatalf("%+v: %v", q, got)
				}
			}
		}
	}
	cfg.MaxTransfers--
	if partial, e := tripbased.New(context.Background(), d, cfg); e == nil || partial != nil {
		t.Fatal("transfer guard did not fail closed")
	}
}
