// Package transit adapts walking snapshots at timetable preparation boundaries.
package transit

import (
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/walking"
	"github.com/ertyoii/transit-routing-go/walking/graph"
)

// FromDataset preserves every directed footpath identity and its integer cost.
// It does not compute shortcuts or discard street vertices.
func FromDataset(d model.Dataset, limits graph.Limits) (*graph.Graph, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	version := model.DatasetFingerprint(d)
	data := graph.Data{Descriptor: walking.Descriptor{ProviderID: "normalized-transit-footpaths", GraphID: d.ID, Version: version, CostModel: "stored-positive-integer-seconds-v1", Sources: []string{"dataset-sha256:" + version}, RoleIndependent: true}}
	for _, s := range d.Stops {
		data.Nodes = append(data.Nodes, graph.Node{ID: s.ID, Accessible: walking.Accessibility(s.Accessible)})
	}
	for _, e := range d.Footpaths {
		source := e.Provenance
		if source == "" {
			source = fmt.Sprintf("dataset:%s", d.ID)
		}
		data.Edges = append(data.Edges, walking.Segment{ID: e.ID, From: e.From, To: e.To, Seconds: e.Duration, Accessible: walking.Accessibility(e.Accessible), Witness: walking.Witness{Source: source, Revision: version, EdgeIDs: []string{e.ID}}})
	}
	return graph.New(data, limits)
}
