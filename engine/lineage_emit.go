package engine

import (
	"log"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
)

// Emitting run lifecycle events to a lineage catalogue.
//
// The emitter existed, was constructed at startup, and was called from
// nowhere: nothing in either repository read the registry field. So a
// deployment that configured a catalogue endpoint received no events at
// all, and the events the emitter could have sent carried empty inputs
// and outputs -- the two fields OpenLineage exists to convey.
//
// Emission is best effort and logged. A catalogue being down must not
// fail a run: the run is the product, and lineage is a report about it.

// emitLineageStart reports that a run began, naming what it will read
// and write.
func (e *Engine) emitLineageStart(pipe *models.Pipeline, runID string) {
	if e.Lineage == nil || pipe == nil {
		return
	}
	inputs, outputs := lineageDatasets(pipe)
	if err := e.Lineage.EmitRunStart(pipe.ID, pipe.Name, runID, inputs, outputs); err != nil {
		log.Printf("lineage: could not report the start of run %s: %v", runID, err)
	}
}

// emitLineageComplete reports that a run finished successfully.
func (e *Engine) emitLineageComplete(pipe *models.Pipeline, runID string, durationMs int64) {
	if e.Lineage == nil || pipe == nil {
		return
	}
	inputs, outputs := lineageDatasets(pipe)
	if err := e.Lineage.EmitRunComplete(pipe.ID, pipe.Name, runID, durationMs, inputs, outputs); err != nil {
		log.Printf("lineage: could not report the completion of run %s: %v", runID, err)
	}
}

// emitLineageFail reports that a run failed.
//
// The datasets are sent on a failure too. A catalogue that only hears
// about successes shows a pipeline as healthy while it is broken, and
// the run that failed is the one somebody is looking for.
func (e *Engine) emitLineageFail(pipe *models.Pipeline, runID, errMsg string) {
	if e.Lineage == nil || pipe == nil {
		return
	}
	inputs, outputs := lineageDatasets(pipe)
	if err := e.Lineage.EmitRunFail(pipe.ID, pipe.Name, runID, errMsg, inputs, outputs); err != nil {
		log.Printf("lineage: could not report the failure of run %s: %v", runID, err)
	}
}

// lineageDatasets converts the pipeline's assets into the wire shape.
func lineageDatasets(pipe *models.Pipeline) (inputs, outputs []extensions.LineageDataset) {
	in, out := PipelineAssets(pipe)
	return toLineageDatasets(in), toLineageDatasets(out)
}

func toLineageDatasets(assets []PipelineAsset) []extensions.LineageDataset {
	if len(assets) == 0 {
		// Empty rather than nil: an emitter marshals these straight into
		// JSON arrays, and a null where a catalogue expects a list is a
		// parse error rather than "no datasets".
		return []extensions.LineageDataset{}
	}
	out := make([]extensions.LineageDataset, 0, len(assets))
	for _, a := range assets {
		out = append(out, extensions.LineageDataset{ID: a.ID, Type: a.Type, Name: a.Name})
	}
	return out
}
