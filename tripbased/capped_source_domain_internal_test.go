package tripbased

import (
	"context"
	"strings"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
)

func TestSourceDomainLegacyGuardsAndForeignPermit(t *testing.T) {
	c := capCases(t)[0]
	c.Dataset.Stops = append(c.Dataset.Stops, model.Stop{ID: "B", Accessible: model.Yes})
	cfg := DestinationSourceDomainConfig{Retained: DestinationRetainedCappedConfig{Scope: capConfig(c), MaxPendingSegments: 10000, MaxSourceSeeds: 10000}, Sources: []string{"A", "B"}}
	x, e := BuildDestinationSourceDomainPatterns(context.Background(), c.Dataset, cfg)
	if e != nil || !x.DomainComplete() || x.legacy.Complete() || x.legacy.p.Complete() {
		t.Fatal(e, x.Snapshot())
	}
	for _, q := range []model.Query{c.Query, func() model.Query { q := c.Query; q.Origin = q.Destination; return q }()} {
		if r, e := x.legacy.Search(context.Background(), q); e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal("legacy destination Search bypassed", r, e)
		}
		if r, e := x.legacy.p.Search(context.Background(), q); e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal("legacy graph Search bypassed", r, e)
		}
		if r, e := x.legacy.p.searchGraphWithSourceDomain(context.Background(), q, nil, x.legacy.checkQuery, nil); e == nil || r.Complete {
			t.Fatal("missing permit bypassed", r, e)
		}
	}
	cfg.Sources = []string{"A"}
	y, e := BuildDestinationSourceDomainPatterns(context.Background(), c.Dataset, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if r, e := x.legacy.p.searchGraphWithSourceDomain(context.Background(), c.Query, nil, x.legacy.checkQuery, y.permit); e == nil || r.Complete || !strings.Contains(e.Error(), "foreign graph certificate") {
		t.Fatal("foreign permit accepted", r, e)
	}
	q := c.Query
	q.Origin = "B"
	if r, e := y.Search(context.Background(), q); e == nil || r.Complete {
		t.Fatal("undeclared source accepted", r, e)
	}
	q.Origin, q.Destination = "B", "B"
	if r, e := y.Search(context.Background(), q); e == nil || r.Complete || len(r.Journeys) != 0 {
		t.Fatal("origin equals goal bypassed domain", r, e)
	}
	x.proof.completed--
	if x.DomainComplete() {
		t.Fatal("missing origin still complete")
	}
	if r, e := x.Search(context.Background(), c.Query); e == nil || r.Complete {
		t.Fatal("missing origin issued query certificate", r, e)
	}
}
