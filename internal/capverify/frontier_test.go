package capverify

import (
	"context"
	"github.com/ertyoii/transit-routing-go/model"
	"reflect"
	"testing"
)

func TestZeroTravelProjectionUsesOriginalQuery(t *testing.T) {
	q := model.Query{Origin: "A", Destination: "A", Departure: 90007}
	d := model.Dataset{}
	got, _, e := Frontier(context.Background(), d, q, []model.Journey{{Legs: []model.Leg{}}}, 100)
	if e != nil || !reflect.DeepEqual(got, []Point{{90007, 0}}) {
		t.Fatal("zero-travel objective", got, e)
	}
	empty, _, e := Frontier(context.Background(), d, q, []model.Journey{}, 100)
	if e != nil || len(empty) != 0 {
		t.Fatal("empty frontier confused with zero travel", empty, e)
	}
	q.Destination = "B"
	if _, _, e := Frontier(context.Background(), d, q, []model.Journey{{}}, 100); e == nil {
		t.Fatal("empty witness accepted for unequal endpoints")
	}
}
