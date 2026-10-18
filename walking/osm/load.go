// Package osm loads the existing audited pedestrian graph, not an arbitrary OSM PBF.
package osm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ertyoii/transit-routing-go/walking"
	"github.com/ertyoii/transit-routing-go/walking/graph"
	"io"
	"math"
	"strconv"
)

const CostModel = "max(1,ceil(segment-metres/1.4))-v1"

type Options struct {
	MaxBytes             int64
	ExpectedGraphSHA256  string
	ExpectedSourceSHA256 string
	PermissionPolicy     string
	Limits               graph.Limits
}
type source struct {
	Attribution      string `json:"attribution"`
	PermissionPolicy string `json:"permission_policy"`
	SourceSHA256     string `json:"source_sha256"`
	Nodes            []struct {
		ID  int64   `json:"id"`
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"nodes"`
	Edges []struct {
		ID      string  `json:"id"`
		From    int64   `json:"from"`
		To      int64   `json:"to"`
		Metres  float64 `json:"metres"`
		Seconds int64   `json:"seconds"`
	} `json:"edges"`
}
type reader struct {
	ctx context.Context
	r   io.Reader
}

func (r reader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}

// Load checks exact input and PBF hashes, permission-profile identity and stored
// cost consistency. Permission/geometry truth belongs to the separate PBF audit.
// Node IDs are decimal OSM IDs, directed edges are preserved, accessibility unknown.
func Load(ctx context.Context, r io.Reader, o Options) (*graph.Graph, error) {
	if o.MaxBytes <= 0 || len(o.ExpectedGraphSHA256) != 64 || len(o.ExpectedSourceSHA256) != 64 || o.PermissionPolicy == "" {
		return nil, fmt.Errorf("%w: pinned OSM load options", walking.ErrInvalid)
	}
	b, e := io.ReadAll(io.LimitReader(reader{ctx, r}, o.MaxBytes+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > o.MaxBytes {
		return nil, walking.ErrBudget
	}
	h := sha256.Sum256(b)
	hash := hex.EncodeToString(h[:])
	if hash != o.ExpectedGraphSHA256 {
		return nil, walking.ErrStale
	}
	var raw source
	if e = json.Unmarshal(b, &raw); e != nil {
		return nil, e
	}
	if raw.SourceSHA256 != o.ExpectedSourceSHA256 || raw.PermissionPolicy != o.PermissionPolicy {
		return nil, walking.ErrStale
	}
	if raw.Attribution == "" {
		return nil, fmt.Errorf("%w: attribution missing", walking.ErrInvalid)
	}
	data := graph.Data{Descriptor: walking.Descriptor{ProviderID: "osm-audited-pedestrian-graph", GraphID: hash, Version: raw.PermissionPolicy, CostModel: CostModel, RoleIndependent: true, Sources: []string{raw.Attribution, "PBF-sha256:" + raw.SourceSHA256, "graph-sha256:" + hash}}}
	for _, n := range raw.Nodes {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		if n.ID <= 0 {
			return nil, fmt.Errorf("%w: OSM node", walking.ErrInvalid)
		}
		data.Nodes = append(data.Nodes, graph.Node{ID: strconv.FormatInt(n.ID, 10), Coordinate: &walking.Coordinate{Latitude: n.Lat, Longitude: n.Lon}, Accessible: walking.Unknown})
	}
	for _, s := range raw.Edges {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		if s.Metres < 0 || math.IsNaN(s.Metres) || math.IsInf(s.Metres, 0) || s.Seconds != int64(math.Max(1, math.Ceil(s.Metres/1.4))) {
			return nil, fmt.Errorf("%w: OSM cost model %q", walking.ErrInvalid, s.ID)
		}
		data.Edges = append(data.Edges, walking.Segment{ID: s.ID, From: strconv.FormatInt(s.From, 10), To: strconv.FormatInt(s.To, 10), Seconds: s.Seconds, Accessible: walking.Unknown, Witness: walking.Witness{Source: "OSM PBF", Revision: raw.SourceSHA256, EdgeIDs: []string{s.ID}}})
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	g, e := graph.New(data, o.Limits)
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	return g, nil
}
