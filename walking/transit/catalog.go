package transit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/walking"
	"reflect"
	"sort"
)

// Catalog is an acquired set of witnessed paths, not a claim of complete map coverage.
// Save it alongside the dataset: full source witnesses and acquisition options are retained.
type Catalog struct {
	Descriptor walking.Descriptor `json:"descriptor"`
	Requests   []walking.Request  `json:"requests"`
	Results    []walking.Result   `json:"results"`
	SHA256     string             `json:"sha256"`
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func catalogHash(c Catalog) string { c.SHA256 = ""; return digest(c) }

// Acquire performs provider work explicitly during preparation. A batch-capable
// adapter receives one bounded batch. Otherwise calls are sequential and cancellable.
// Role-dependent, accessible-only and absorbing-node acquisitions are rejected:
// the current timeless Footpath schema cannot encode those restricted subgraphs.
func Acquire(ctx context.Context, p walking.Provider, requests []walking.Request, maxRequests int) (Catalog, error) {
	if e := ctx.Err(); e != nil {
		return Catalog{}, e
	}
	if maxRequests <= 0 || len(requests) == 0 || len(requests) > maxRequests {
		return Catalog{}, walking.ErrBudget
	}
	descriptor := p.Descriptor()
	if !descriptor.RoleIndependent {
		return Catalog{}, fmt.Errorf("%w: materialization requires role-independent costs", walking.ErrUnsupported)
	}
	b, e := json.Marshal(requests)
	if e != nil {
		return Catalog{}, e
	}
	var owned []walking.Request
	if e = json.Unmarshal(b, &owned); e != nil {
		return Catalog{}, e
	}
	for _, r := range owned {
		if e := r.Options.Validate(); e != nil {
			return Catalog{}, e
		}
		if r.SnapshotID != descriptor.SnapshotID {
			return Catalog{}, walking.ErrStale
		}
		if r.Options.AccessibleOnly || r.Options.StopAt != "" {
			return Catalog{}, fmt.Errorf("%w: policy-restricted catalog", walking.ErrUnsupported)
		}
	}
	var values []walking.Result
	if batch, ok := p.(walking.BatchProvider); ok {
		values, e = batch.Batch(ctx, owned)
	} else {
		for _, r := range owned {
			if e = ctx.Err(); e != nil {
				break
			}
			var v walking.Result
			v, e = p.Route(ctx, r)
			if e != nil {
				break
			}
			values = append(values, v)
		}
	}
	if e != nil {
		return Catalog{}, e
	}
	if e = ctx.Err(); e != nil {
		return Catalog{}, e
	}
	if !reflect.DeepEqual(descriptor, p.Descriptor()) {
		return Catalog{}, walking.ErrStale
	}
	if len(values) != len(owned) {
		return Catalog{}, fmt.Errorf("%w: missing catalog results", walking.ErrInvalid)
	}
	for i, v := range values {
		if e = walking.CheckResult(owned[i], v); e != nil {
			return Catalog{}, e
		}
		values[i] = walking.CloneResult(v)
	}
	c := Catalog{Descriptor: descriptor, Requests: owned, Results: values}
	c.SHA256 = catalogHash(c)
	return c, nil
}

// catalogData validates the unchanged provider-namespace witnesses before any
// materialization. Neither endpoint equality nor an interior spelling binds stops.
func (c Catalog) catalogData() (map[string]walking.Segment, map[string]bool, error) {
	if c.SHA256 == "" || c.SHA256 != catalogHash(c) {
		return nil, nil, fmt.Errorf("%w: catalog digest", walking.ErrInvalid)
	}
	if !c.Descriptor.RoleIndependent || len(c.Requests) == 0 || len(c.Requests) != len(c.Results) {
		return nil, nil, walking.ErrUnsupported
	}
	if c.Descriptor.ProviderID == "" || c.Descriptor.GraphID == "" || c.Descriptor.Version == "" || c.Descriptor.SnapshotID == "" {
		return nil, nil, fmt.Errorf("%w: catalog provider identity", walking.ErrInvalid)
	}
	segments, observed := map[string]walking.Segment{}, map[string]bool{}
	for i, r := range c.Requests {
		if r.SnapshotID != c.Descriptor.SnapshotID {
			return nil, nil, walking.ErrStale
		}
		if r.Options.AccessibleOnly || r.Options.StopAt != "" {
			return nil, nil, fmt.Errorf("%w: catalog policy", walking.ErrInvalid)
		}
		v := c.Results[i]
		if e := walking.CheckResult(r, v); e != nil {
			return nil, nil, e
		}
		observed[r.From.NodeID], observed[r.To.NodeID] = true, true
		if v.Path == nil {
			continue
		}
		for _, s := range v.Path.Segments {
			if prior, ok := segments[s.ID]; ok && !reflect.DeepEqual(prior, s) {
				return nil, nil, fmt.Errorf("%w: conflicting segment identity", walking.ErrInvalid)
			}
			segments[s.ID] = s
			observed[s.From], observed[s.To] = true, true
		}
	}
	return segments, observed, nil
}

// Materialize replaces walking edges with the acquired segment union, using
// explicit anchors. Unbound nodes are namespaced by the content-bound binding
// declaration (including provider/catalog and base dataset), never reused by
// spelling. The returned mapping preserves full provider witness traceability.
// This finite graph may omit alternatives and does not certify physical anchors.
func (c Catalog) Materialize(d model.Dataset, bindings Bindings) (Materialization, error) {
	if e := d.Validate(); e != nil {
		return Materialization{}, e
	}
	segments, observed, e := c.catalogData()
	if e != nil {
		return Materialization{}, e
	}
	anchored, e := c.bindingMap(d, bindings, observed)
	if e != nil {
		return Materialization{}, e
	}
	out := d.Clone()
	out.ID = d.ID + ":walk:" + bindings.SHA256
	out.Footpaths = nil
	mapping := Mapping{Schema: "walking-transit-mapping.v1", CatalogSHA256: c.SHA256,
		BindingSHA256: bindings.SHA256, Nodes: []MaterializedNode{}, Segments: []MaterializedSegment{}}
	stops, mapped := map[string]bool{}, map[string]string{}
	for _, s := range out.Stops {
		stops[s.ID] = true
	}
	nodes := make([]string, 0, len(observed))
	for id := range observed {
		nodes = append(nodes, id)
	}
	sort.Strings(nodes)
	for _, id := range nodes {
		target, explicit := anchored[id]
		if !explicit {
			target = "walk-node:" + digest([]string{BindingVersion, bindings.SHA256, id})
			if stops[target] {
				return Materialization{}, fmt.Errorf("%w: generated node collision", walking.ErrInvalid)
			}
			out.Stops = append(out.Stops, model.Stop{ID: target, Accessible: model.Unknown})
			stops[target] = true
		}
		mapped[id] = target
		mapping.Nodes = append(mapping.Nodes, MaterializedNode{id, target, explicit})
	}
	keys := make([]string, 0, len(segments))
	for id := range segments {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		s := segments[id]
		footpathID := "walk:" + digest([]string{BindingVersion, bindings.SHA256, s.ID})
		out.Footpaths = append(out.Footpaths, model.Footpath{ID: footpathID, From: mapped[s.From], To: mapped[s.To], Duration: s.Seconds,
			Accessible: model.Access(s.Accessible), Provenance: "walking-catalog-sha256:" + c.SHA256 + ";bindings-sha256:" + bindings.SHA256 + ";segment:" + s.ID})
		mapping.Segments = append(mapping.Segments, MaterializedSegment{s.ID, footpathID})
	}
	if e := out.Validate(); e != nil {
		return Materialization{}, e
	}
	return Materialization{out, mapping}, nil
}

// Apply is Materialize's dataset-only convenience wrapper. Explicit bindings
// are mandatory even when provider and transit endpoint spellings are identical.
func (c Catalog) Apply(d model.Dataset, bindings Bindings) (model.Dataset, error) {
	out, e := c.Materialize(d, bindings)
	return out.Dataset, e
}

// MaterializeLayer replaces only named footpaths and returns the added layer's
// source mapping. All retained walking edges and transit/transfer rules survive.
// An empty removal list adds a layer; duplicate/unknown removal IDs fail closed.
func (c Catalog) MaterializeLayer(d model.Dataset, bindings Bindings, removeIDs []string) (Materialization, error) {
	known, removed := map[string]bool{}, map[string]bool{}
	for _, edge := range d.Footpaths {
		known[edge.ID] = true
	}
	for _, id := range removeIDs {
		if !known[id] || removed[id] {
			return Materialization{}, fmt.Errorf("%w: unknown/duplicate layer removal %q", walking.ErrInvalid, id)
		}
		removed[id] = true
	}
	out, e := c.Materialize(d, bindings)
	if e != nil {
		return Materialization{}, e
	}
	for _, edge := range d.Footpaths {
		if !removed[edge.ID] {
			out.Dataset.Footpaths = append(out.Dataset.Footpaths, edge)
		}
	}
	ids := append([]string{}, removeIDs...)
	sort.Strings(ids)
	out.Dataset.ID = d.ID + ":walk-layer:" + digest(struct {
		Bindings string
		Removed  []string
	}{bindings.SHA256, ids})
	if e = out.Dataset.Validate(); e != nil {
		return Materialization{}, e
	}
	return out, nil
}

func (c Catalog) ApplyLayer(d model.Dataset, bindings Bindings, removeIDs []string) (model.Dataset, error) {
	out, e := c.MaterializeLayer(d, bindings, removeIDs)
	return out.Dataset, e
}
