package osm_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/ertyoii/transit-routing-go/walking"
	"github.com/ertyoii/transit-routing-go/walking/graph"
	"github.com/ertyoii/transit-routing-go/walking/osm"
	"strings"
	"testing"
)

func TestPinnedOSMLoadPreservesDirectionCoordinatesAndUnknownAccess(t *testing.T) {
	source := strings.Repeat("a", 64)
	raw := `{"attribution":"OSM contributors ODbL","permission_policy":"audited-v1","source_sha256":"` + source + `","nodes":[{"id":1,"lat":1,"lon":103},{"id":2,"lat":1.1,"lon":103.1}],"edges":[{"id":"w1:0:f","from":1,"to":2,"metres":2.8,"seconds":2}],"ways":[]}`
	h := sha256.Sum256([]byte(raw))
	o := osm.Options{MaxBytes: 10000, ExpectedGraphSHA256: hex.EncodeToString(h[:]), ExpectedSourceSHA256: source, PermissionPolicy: "audited-v1", Limits: graph.DefaultLimits()}
	g, e := osm.Load(context.Background(), strings.NewReader(raw), o)
	if e != nil {
		t.Fatal(e)
	}
	r := walking.Request{SnapshotID: g.Descriptor().SnapshotID, From: walking.Endpoint{NodeID: "1", Coordinate: &walking.Coordinate{Latitude: 1, Longitude: 103}}, To: walking.Endpoint{NodeID: "2"}, Options: walking.Options{Role: walking.Direct, MaxSeconds: 2}}
	v, e := g.Route(context.Background(), r)
	if e != nil || v.Path == nil || v.Path.Seconds != 2 || v.Path.Segments[0].Witness.Revision != source {
		t.Fatal(v, e)
	}
	r.Options.AccessibleOnly = true
	v, e = g.Route(context.Background(), r)
	if e != nil || v.Status != walking.Unreachable {
		t.Fatal(v, e)
	}
	r.From = walking.Endpoint{NodeID: "2"}
	r.To = walking.Endpoint{NodeID: "1"}
	r.Options.AccessibleOnly = false
	v, e = g.Route(context.Background(), r)
	if e != nil || v.Status != walking.Unreachable {
		t.Fatal("invented reverse edge", v, e)
	}
	o.MaxBytes = 5
	if _, e = osm.Load(context.Background(), strings.NewReader(raw), o); !errors.Is(e, walking.ErrBudget) {
		t.Fatal(e)
	}
	o.MaxBytes = 10000
	o.ExpectedGraphSHA256 = strings.Repeat("b", 64)
	if _, e = osm.Load(context.Background(), strings.NewReader(raw), o); !errors.Is(e, walking.ErrStale) {
		t.Fatal(e)
	}
}
