package paper

import "github.com/ertyoii/transit-routing-go/model"

// NodeArena amortizes witness-node allocation within a single query. Every
// returned node has a stable address and remains owned by its parent chain;
// slabs are never reset or reused while any node may still be referenced.
// The arena must not be copied after first use or shared between concurrent
// queries.
type NodeArena struct {
	slab []Node
	used int
}

const nodeSlabSize = 32

func (a *NodeArena) node(parent *Node, leg model.Leg) *Node {
	if a.used == len(a.slab) {
		a.slab = make([]Node, nodeSlabSize)
		a.used = 0
	}
	n := &a.slab[a.used]
	a.used++
	*n = Node{Previous: parent, Leg: leg}
	return n
}

// Ride records exactly the same leg as Ride, without a per-leg allocation.
func (a *NodeArena) Ride(parent *Node, c model.Connection) *Node {
	return a.node(parent, model.Leg{Kind: "ride", Ref: c.ID, Departure: c.Departure, Arrival: c.Arrival})
}

// Append records the same ordered walking legs and timestamps as Path.Append.
func (a *NodeArena) Append(p Path, n *Node, start int64) *Node {
	for _, w := range p.Edges {
		n = a.node(n, model.Leg{Kind: "walk", Ref: w.ID, Departure: start, Arrival: start + w.Duration})
		start += w.Duration
	}
	return n
}
