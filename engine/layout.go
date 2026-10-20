package engine

import "github.com/Tnsor-Labs/brokoli/models"

// ArrangeStacked lays out a pipeline whose nodes all sit at one position,
// and reports whether it did.
//
// A pipeline compiled from code carries no layout: the canonical IR a
// repository deploys leaves positions out on purpose (moving a node on a
// canvas is not a change to the pipeline), so every node arrives at the
// origin and every view drew them on top of each other. A pipeline that
// has a layout, even one with two nodes that happen to coincide, is left
// as it is.
func ArrangeStacked(p *models.Pipeline) bool {
	if p == nil || len(p.Nodes) < 2 {
		return false
	}
	first := p.Nodes[0].Position
	for _, n := range p.Nodes[1:] {
		if n.Position != first {
			return false
		}
	}
	for i := range p.Nodes {
		p.Nodes[i].Position = models.Position{}
	}
	autoLayoutNodes(p)
	return true
}
