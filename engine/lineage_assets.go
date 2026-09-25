package engine

import (
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// The assets a pipeline reads and writes, named the same way the lineage
// graph names them.
//
// This exists so a lineage consumer outside the engine -- an
// OpenLineage-compatible catalogue, for instance -- can be told which
// datasets a run touched. The graph endpoint already computes exactly
// this and then keeps it to itself.
//
// Deliberately the same extractors buildLineageGraph uses. Two functions
// that each decide what "the asset behind this node" means would drift,
// and the first symptom would be a catalogue and a lineage page naming
// the same table differently, which is worse than either being wrong
// alone.

// PipelineAsset is one external dataset a pipeline reads or writes.
type PipelineAsset struct {
	// ID is the stable asset identifier the lineage graph uses, of the
	// form "file:/path" or "table:db.table".
	ID string
	// Type is "file", "table" or "api".
	Type string
	// Name is the display name: a filename, a table name, a URL.
	Name string
}

// PipelineAssets lists what a pipeline reads and what it writes.
//
// Derived from the pipeline definition, so it describes what the
// pipeline is declared to touch. A run that fails partway still declares
// the same assets; whether they were actually written is a separate
// question this does not answer.
func PipelineAssets(p *models.Pipeline) (inputs, outputs []PipelineAsset) {
	if p == nil {
		return nil, nil
	}
	// Variables are resolved first, for the same reason the graph
	// resolves them: an unresolved "${env.DATA_DIR}/x.csv" is not an
	// asset identity, it is a template, and two pipelines pointing at the
	// same file through different variables must land on the same asset.
	varCtx := NewVariableContext(p.Params, "lineage", time.Now())

	nodes := make(map[string]models.Node, len(p.Nodes))
	for _, n := range p.Nodes {
		resolved := n
		resolved.Config = varCtx.ResolveConfig(n.Config)
		nodes[n.ID] = resolved
	}

	// assetNodes is the extractors' output parameter; the display
	// metadata they record there is what gives an asset its name.
	assetNodes := make(map[string]LineageNode)

	for _, n := range p.Nodes {
		resolved := nodes[n.ID]
		var id string
		var isInput bool

		switch n.Type {
		case models.NodeTypeSourceFile:
			id, isInput = extractFileAsset(resolved.Config, assetNodes), true
		case models.NodeTypeSourceAPI:
			id, isInput = extractAPIAsset(resolved.Config, assetNodes), true
		case models.NodeTypeSourceDB:
			id, isInput = extractSourceDBAsset(resolved.Config, assetNodes), true
		case models.NodeTypeSinkFile:
			id, isInput = extractFileAsset(resolved.Config, assetNodes), false
		case models.NodeTypeSinkDB:
			table := findUpstreamTableName(n.ID, nodes, p.Edges)
			id, isInput = extractSinkDBAsset(resolved.Config, table, assetNodes), false
		default:
			// Processing nodes are not external assets. A code node reads
			// and writes datasets inside the run, which is the graph's
			// business and not a catalogue's.
			continue
		}
		if id == "" {
			// The extractor could not name an asset: a source_file with no
			// path, a sink_db with no table. Skipped rather than emitted
			// as an empty identity, which would merge every unnamed asset
			// in the catalogue into one.
			continue
		}

		asset := PipelineAsset{ID: id, Type: "file", Name: id}
		if node, ok := assetNodes[id]; ok {
			asset.Type, asset.Name = node.Type, node.Name
		}
		if isInput {
			inputs = append(inputs, asset)
		} else {
			outputs = append(outputs, asset)
		}
	}
	return dedupeAssets(inputs), dedupeAssets(outputs)
}

// dedupeAssets removes repeats, keeping order.
//
// A pipeline may read the same file from two nodes, and a catalogue that
// receives it twice records two reads of one dataset.
func dedupeAssets(assets []PipelineAsset) []PipelineAsset {
	if len(assets) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(assets))
	out := assets[:0]
	for _, a := range assets {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		out = append(out, a)
	}
	return out
}
