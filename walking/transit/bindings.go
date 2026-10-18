package transit

import (
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/walking"
	"sort"
)

const BindingVersion = "walking-transit-bindings.v1"

// NodeBinding is an explicit physical anchor, never inferred from ID equality.
// Distinct provider nodes cannot alias one transit stop. Model separate entrances
// with witnessed positive-cost connectors and bind their shared boarding anchor.
type NodeBinding struct {
	ProviderNodeID string `json:"provider_node_id"`
	TransitStopID  string `json:"transit_stop_id"`
}

// Bindings is a versioned, content-bound declaration of cross-namespace anchors.
// The digest detects accidental modification; it is not a physical-truth attestation.
type Bindings struct {
	Schema        string        `json:"schema"`
	CatalogSHA256 string        `json:"catalog_sha256"`
	DatasetSHA256 string        `json:"dataset_sha256"`
	Nodes         []NodeBinding `json:"nodes"`
	SHA256        string        `json:"sha256"`
}

func bindingHash(b Bindings) string { b.SHA256 = ""; return digest(b) }

// Bind owns and validates the supplied anchors. Every requested endpoint must
// be bound; any observed interior node may also be explicitly bound. Unbound
// interiors are private materialized nodes, even if their spelling matches a stop.
func (c Catalog) Bind(d model.Dataset, nodes []NodeBinding) (Bindings, error) {
	if err := d.Validate(); err != nil {
		return Bindings{}, err
	}
	_, observed, err := c.catalogData()
	if err != nil {
		return Bindings{}, err
	}
	b := Bindings{Schema: BindingVersion, CatalogSHA256: c.SHA256,
		DatasetSHA256: model.DatasetFingerprint(d), Nodes: append([]NodeBinding{}, nodes...)}
	sort.Slice(b.Nodes, func(i, j int) bool { return b.Nodes[i].ProviderNodeID < b.Nodes[j].ProviderNodeID })
	b.SHA256 = bindingHash(b)
	if _, err = c.bindingMap(d, b, observed); err != nil {
		return Bindings{}, err
	}
	return b, nil
}

func (c Catalog) bindingMap(d model.Dataset, b Bindings, observed map[string]bool) (map[string]string, error) {
	if b.Schema != BindingVersion || b.SHA256 == "" || b.SHA256 != bindingHash(b) {
		return nil, fmt.Errorf("%w: explicit versioned node bindings required", walking.ErrInvalid)
	}
	if b.CatalogSHA256 != c.SHA256 || b.DatasetSHA256 != model.DatasetFingerprint(d) {
		return nil, fmt.Errorf("%w: catalog/dataset node bindings", walking.ErrStale)
	}
	stops, targets, mapped := map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, s := range d.Stops {
		stops[s.ID] = true
	}
	for _, anchor := range b.Nodes {
		if !observed[anchor.ProviderNodeID] || !stops[anchor.TransitStopID] ||
			mapped[anchor.ProviderNodeID] != "" || targets[anchor.TransitStopID] {
			return nil, fmt.Errorf("%w: unknown or non-injective node binding", walking.ErrInvalid)
		}
		mapped[anchor.ProviderNodeID] = anchor.TransitStopID
		targets[anchor.TransitStopID] = true
	}
	for _, r := range c.Requests {
		if mapped[r.From.NodeID] == "" || mapped[r.To.NodeID] == "" {
			return nil, fmt.Errorf("%w: unbound provider request endpoint", walking.ErrInvalid)
		}
	}
	return mapped, nil
}

type MaterializedNode struct {
	ProviderNodeID string `json:"provider_node_id"`
	DatasetStopID  string `json:"dataset_stop_id"`
	Explicit       bool   `json:"explicit_binding"`
}

type MaterializedSegment struct {
	ProviderSegmentID string `json:"provider_segment_id"`
	FootpathID        string `json:"footpath_id"`
}

// Mapping links normalized paths back to the unchanged provider witnesses in
// Catalog. Persist both with the dataset. It covers the acquired layer only.
type Mapping struct {
	Schema        string                `json:"schema"`
	CatalogSHA256 string                `json:"catalog_sha256"`
	BindingSHA256 string                `json:"binding_sha256"`
	Nodes         []MaterializedNode    `json:"nodes"`
	Segments      []MaterializedSegment `json:"segments"`
}

type Materialization struct {
	Dataset model.Dataset `json:"dataset"`
	Mapping Mapping       `json:"mapping"`
}
