package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// DefaultSpillThresholdBytes is the estimated encoded size at which a node's
// output stops being held in memory for the rest of the run and is written
// to the artifact store instead.
//
// Deliberately high. The problem being solved is a pipeline whose memory use
// scales with the largest result it ever touches; a few megabytes of rows is
// not that, and paying disk I/O for them would make ordinary pipelines
// slower to fix a problem they do not have. Below this, behaviour is exactly
// what it was.
const DefaultSpillThresholdBytes int64 = 64 << 20 // 64 MiB

// sizeEstimateSampleRows is how many rows are encoded to estimate the size
// of a whole dataset.
//
// Encoding every row to decide whether to encode every row would cost the
// serialization twice for datasets that never spill, which is all of them in
// a normal pipeline. Sampling makes the check proportional to the sample
// rather than the data. It is an estimate and named as one: rows with wildly
// uneven sizes will be misjudged, and the consequence of misjudging is
// holding a dataset in memory that could have spilled, or spilling one that
// need not have — never incorrect data.
const sizeEstimateSampleRows = 32

// nodeOutputs holds each node's result for the rest of the run and decides
// whether to keep it in memory.
//
// It exists so that spilling lives in one place. ADR-012 anticipated that
// every node handler would have to learn that its input might be a reference
// to resolve; it does not, because a handler never sees this type. Outputs
// enter and leave as *common.DataSet, and whether the bytes in between sat
// in memory or on disk is not something a handler can observe. One
// implementation to get right, and no change to the node contract.
//
// A spilled output is read back once per consumer. For a fan-out that is
// more reads than holding it in memory would be, which is the trade being
// made: the point is that memory stops scaling with the largest result, and
// re-reading is what that costs.
type nodeOutputs struct {
	mu      sync.Mutex
	inline  map[string]*common.DataSet
	spilled map[string]*artifact.DatasetRef

	// blobs is where spilled outputs go. Nil disables spilling entirely,
	// which is the case for any runner without an artifact store behind it.
	blobs artifact.Store
	// namespace scopes spilled outputs to this run, so they are reclaimed
	// by the same DeleteRunArtifacts call as everything else the run wrote.
	namespace string
	// threshold is the estimated encoded size at or above which an output
	// spills. Zero or negative disables spilling.
	threshold int64

	// onSpill, when set, is called after an output is spilled. Used for
	// logging; kept as a hook so this type does not need a logger.
	onSpill func(nodeID string, estimatedBytes int64, ref *artifact.DatasetRef)
}

func newNodeOutputs(blobs artifact.Store, namespace string, threshold int64) *nodeOutputs {
	return &nodeOutputs{
		inline:    make(map[string]*common.DataSet),
		spilled:   make(map[string]*artifact.DatasetRef),
		blobs:     blobs,
		namespace: namespace,
		threshold: threshold,
	}
}

// spillEnabled reports whether this store can spill at all.
func (o *nodeOutputs) spillEnabled() bool {
	return o.blobs != nil && o.namespace != "" && o.threshold > 0
}

// Put records a node's output, spilling it if it is large enough to be worth
// getting out of memory.
//
// A spill failure is not fatal: the output is kept in memory instead. The
// dataset is already in hand and correct, and refusing to continue because
// an optimisation could not be applied would turn a full disk into a failed
// pipeline. The error is returned so the caller can log it.
func (o *nodeOutputs) Put(nodeID string, ds *common.DataSet) error {
	if ds == nil {
		return nil
	}
	if !o.spillEnabled() {
		o.putInline(nodeID, ds)
		return nil
	}

	estimate := estimateEncodedSize(ds)
	if estimate < o.threshold {
		o.putInline(nodeID, ds)
		return nil
	}

	ref, err := o.spill(ds)
	if err != nil {
		o.putInline(nodeID, ds)
		return fmt.Errorf("spill node %s output (%d bytes estimated): %w", nodeID, estimate, err)
	}

	o.mu.Lock()
	delete(o.inline, nodeID)
	o.spilled[nodeID] = ref
	o.mu.Unlock()

	if o.onSpill != nil {
		o.onSpill(nodeID, estimate, ref)
	}
	return nil
}

func (o *nodeOutputs) putInline(nodeID string, ds *common.DataSet) {
	o.mu.Lock()
	o.inline[nodeID] = ds
	delete(o.spilled, nodeID)
	o.mu.Unlock()
}

// spill writes the dataset to the artifact store and returns a reference.
func (o *nodeOutputs) spill(ds *common.DataSet) (*artifact.DatasetRef, error) {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(EncodeArrowJSON(pw, ds))
	}()

	ref, err := o.blobs.Put(context.Background(), o.namespace, pr, artifact.PutOptions{
		MediaType: artifact.MediaTypeNDJSON,
	})
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, err
	}

	cols := []string{}
	if ds.Columns != nil {
		cols = ds.Columns
	}
	return &artifact.DatasetRef{
		ArtifactRef: *ref,
		Format:      artifact.FormatNDJSON,
		Columns:     cols,
		RowCount:    int64(len(ds.Rows)),
	}, nil
}

// Get returns a node's output, reading it back from the artifact store if it
// was spilled. The second return reports whether the node has an output at
// all, matching the map lookup this replaced.
func (o *nodeOutputs) Get(nodeID string) (*common.DataSet, bool, error) {
	o.mu.Lock()
	if ds, ok := o.inline[nodeID]; ok {
		o.mu.Unlock()
		return ds, true, nil
	}
	ref, ok := o.spilled[nodeID]
	o.mu.Unlock()
	if !ok {
		return nil, false, nil
	}

	rc, err := o.blobs.Open(context.Background(), &ref.ArtifactRef)
	if err != nil {
		// A spilled output that cannot be read back is a hard failure. The
		// data is not somewhere else — this was the only copy, and handing
		// a downstream node an empty dataset instead is the exact data-loss
		// shape Tnsor-Labs/brokoli#8 exists to prevent.
		return nil, true, fmt.Errorf("read spilled output for node %s: %w", nodeID, err)
	}
	defer rc.Close()

	ds, err := DecodeArrowJSON(rc, ref.Columns)
	if err != nil {
		return nil, true, fmt.Errorf("decode spilled output for node %s: %w", nodeID, err)
	}
	return ds, true, nil
}

// spilledCount reports how many outputs are currently held by reference.
// Used by tests and by the run summary.
func (o *nodeOutputs) spilledCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.spilled)
}

// estimateEncodedSize approximates how many bytes ds would occupy in the
// NDJSON encoding, without encoding all of it.
//
// Rows are sampled and the average is multiplied out. For a dataset whose
// rows are similar — which is what a tabular result usually is — this is
// close enough to decide a threshold, and it costs the sample rather than
// the whole dataset.
func estimateEncodedSize(ds *common.DataSet) int64 {
	if ds == nil || len(ds.Rows) == 0 {
		return 0
	}
	sample := len(ds.Rows)
	if sample > sizeEstimateSampleRows {
		sample = sizeEstimateSampleRows
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // match EncodeArrowJSON, so the estimate tracks the real encoding
	for i := 0; i < sample; i++ {
		if err := enc.Encode(ds.Rows[i]); err != nil {
			// A row that will not encode here will not encode when spilled
			// either. Report zero so the caller keeps it in memory and the
			// real failure surfaces where it can be reported properly.
			return 0
		}
	}
	if sample == len(ds.Rows) {
		return int64(buf.Len())
	}
	avg := float64(buf.Len()) / float64(sample)
	return int64(avg * float64(len(ds.Rows)))
}

// newOutputs builds this run's output store, spilling to the same artifact
// store and the same per-run namespace everything else in the run uses.
//
// Spilling is off when the runner has no artifact store, when there is no
// run to scope blobs to (a dry run), or when the store cannot hold arbitrary
// bytes. In each case outputs stay in memory, which is exactly the behaviour
// that existed before spilling.
func (r *Runner) newOutputs() *nodeOutputs {
	threshold := r.spillThreshold
	if threshold == 0 {
		threshold = DefaultSpillThresholdBytes
	}

	var blobs artifact.Store
	namespace := ""
	if provider, ok := r.artifactStore.(BlobStoreProvider); ok && r.run != nil && r.run.ID != "" {
		blobs = provider.Blobs()
		namespace = r.run.ID
	}

	out := newNodeOutputs(blobs, namespace, threshold)
	out.onSpill = func(nodeID string, estimatedBytes int64, ref *artifact.DatasetRef) {
		r.log(nodeID, models.LogLevelInfo,
			"Spilled %d row(s) (~%d bytes) to the artifact store; held by reference for the rest of the run",
			ref.RowCount, estimatedBytes)
	}
	return out
}
