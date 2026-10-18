// walkserve serves a prepared normalized or audited OSM walking graph locally.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/walking"
	"github.com/ertyoii/transit-routing-go/walking/graph"
	"github.com/ertyoii/transit-routing-go/walking/httpapi"
	"github.com/ertyoii/transit-routing-go/walking/osm"
	"github.com/ertyoii/transit-routing-go/walking/transit"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run() error {
	dataset := flag.String("dataset", "", "normalized dataset JSON")
	osmPath := flag.String("osm-graph", "", "audited graph JSON (alternative to dataset)")
	osmHash := flag.String("osm-graph-sha256", "", "required graph hash")
	pbfHash := flag.String("osm-source-sha256", "", "required source PBF hash")
	policy := flag.String("osm-permission-policy", "pedestrian_allowlist_v2", "audited permission profile")
	address := flag.String("listen", "127.0.0.1:8080", "listen address; no authentication, keep local")
	flag.Parse()
	if (*dataset == "") == (*osmPath == "") {
		return fmt.Errorf("choose exactly one of -dataset or -osm-graph")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var p walking.Provider
	var e error
	if *dataset != "" {
		var d model.Dataset
		var b []byte
		b, e = os.ReadFile(*dataset)
		if e == nil {
			e = json.Unmarshal(b, &d)
		}
		if e == nil {
			p, e = transit.FromDataset(d, graph.DefaultLimits())
		}
	} else {
		var f *os.File
		f, e = os.Open(*osmPath)
		if e == nil {
			p, e = osm.Load(ctx, f, osm.Options{MaxBytes: 256 << 20, ExpectedGraphSHA256: *osmHash, ExpectedSourceSHA256: *pbfHash, PermissionPolicy: *policy, Limits: graph.DefaultLimits()})
			_ = f.Close()
		}
	}
	if e != nil {
		return e
	}
	handler, e := httpapi.New(p, httpapi.DefaultLimits())
	if e != nil {
		return e
	}
	server := &http.Server{Addr: *address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Fprintf(os.Stderr, "walking snapshot %s at %s\n", p.Descriptor().SnapshotID, *address)
	if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		return e
	}
	return nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
