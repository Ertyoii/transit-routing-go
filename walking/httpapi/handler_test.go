package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/ertyoii/transit-routing-go/walking"
	"github.com/ertyoii/transit-routing-go/walking/graph"
	"github.com/ertyoii/transit-routing-go/walking/httpapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPStatusesAndOrderedResults(t *testing.T) {
	g, e := graph.New(graph.Data{Descriptor: walking.Descriptor{ProviderID: "fixture", GraphID: "singleton", Version: "1", CostModel: "seconds", RoleIndependent: true}, Nodes: []graph.Node{{ID: "a", Accessible: walking.Unknown}}}, graph.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	h, e := httpapi.New(g, httpapi.Limits{MaxBodyBytes: 1024, MaxMatrixCells: 1, Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	r := walking.Request{SnapshotID: g.Descriptor().SnapshotID, From: walking.Endpoint{NodeID: "a"}, To: walking.Endpoint{NodeID: "a"}, Options: walking.Options{Role: walking.Direct, MaxSeconds: 0}}
	body, _ := json.Marshal(r)
	for _, tt := range []struct {
		path, body string
		status     int
	}{{"/v1/route", string(body), 200}, {"/v1/duration", string(body), 200}, {"/v1/route", `{"snapshot_id":"x","SNAPSHOT_ID":"y"}`, 400}, {"/v1/route", string(body) + "{}", 400}, {"/v1/route", strings.Repeat("x", 1025), 400}, {"/v1/matrix", `{"sources":[{},{}],"targets":[{}]}`, 429}, {"/v1/batch", "[]", 400}, {"/missing", "", 404}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body)))
		if w.Code != tt.status {
			t.Fatalf("%s got %d: %s", tt.path, w.Code, w.Body.String())
		}
	}
	r.SnapshotID = "changed"
	body, _ = json.Marshal(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/route", strings.NewReader(string(body))))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/route", strings.NewReader(string(body))).WithContext(ctx)
	h.ServeHTTP(w, req)
	if w.Code != 499 {
		t.Fatal(w.Code, w.Body.String())
	}
}
