package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// tables holds ADR-023 TableRefs: outputs that were never read out of
	// the database at all. They are run-scoped and never persisted — a
	// barrier materialises to a blob, which is where durability lives.
	tables map[string]*TableRef

	// blobs is where spilled outputs go. Nil disables spilling entirely,
	// which is the case for any runner without an artifact store behind it.
	blobs artifact.Store
	// namespace scopes spilled outputs to this run, so they are reclaimed
	// by the same DeleteRunArtifacts call as everything else the run wrote.
	namespace string
	// threshold is the estimated encoded size at or above which an output
	// spills. Zero or negative disables spilling.
	threshold int64
	// streamThreshold is the encoded size at or above which ADR-019's
	// reference-passing paths engage (see Engine.StreamThresholdBytes) —
	// resolved here alongside threshold so the two knobs travel together.
	// Zero or negative disables reference-passing.
	streamThreshold int64

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

// PutStream writes a node's output to the blob store as it is produced,
// without the whole dataset ever existing.
//
// spill takes a *common.DataSet, which means the dataset had to fit in
// memory before it could be got out of memory — fine for a node whose
// result is already in hand, useless for a source reading a table larger
// than the worker. produce is handed an emit function instead and calls it
// per batch; the rows are encoded and streamed into the blob as they
// arrive, so the resident set is one batch no matter how many rows pass
// through.
//
// The returned ref carries the row count produce actually emitted, counted
// here rather than trusted from the caller, since it is what every
// downstream consumer reports as the node's row count. columns is called
// after production finishes, because a source does not learn its column
// list until the query has begun returning rows.
func (o *nodeOutputs) PutStream(produce func(emit func(*common.DataSet) error) error, columns func() []string) (*artifact.DatasetRef, error) {
	if !o.spillEnabled() {
		return nil, fmt.Errorf("stream output: spilling is not enabled")
	}

	pr, pw := io.Pipe()
	type produced struct {
		rows int64
		err  error
	}
	// The producer reports through a channel rather than shared variables:
	// on the error path Put can return before the goroutine has finished,
	// and reading its results then would be a data race.
	done := make(chan produced, 1)
	go func() {
		// Buffered for the reason given on EncodeArrowJSON: pw is a pipe,
		// and an unbuffered write per row costs a scheduler round-trip per
		// row.
		buf := bufio.NewWriterSize(pw, encodeBufferSize)
		enc := json.NewEncoder(buf)
		enc.SetEscapeHTML(false) // match EncodeArrowJSON byte-for-byte
		rows := int64(0)
		err := produce(func(batch *common.DataSet) error {
			for _, row := range batch.Rows {
				if encErr := enc.Encode(row); encErr != nil {
					return encErr
				}
				rows++
			}
			return nil
		})
		if err == nil && rows == 0 {
			// Preserve EncodeArrowJSON's empty-dataset sentinel so the blob
			// decodes identically to a batch-written empty output.
			_, err = buf.Write([]byte("[]"))
		}
		if err == nil {
			err = buf.Flush()
		}
		_ = pw.CloseWithError(err)
		done <- produced{rows: rows, err: err}
	}()

	ref, putErr := o.blobs.Put(context.Background(), o.namespace, pr, artifact.PutOptions{
		MediaType: artifact.MediaTypeNDJSON,
	})
	if putErr != nil {
		// Unblock the producer so the wait below cannot hang.
		_ = pr.CloseWithError(putErr)
	}
	res := <-done

	// A genuine producer error (a failing query, a bad row) is the root
	// cause even though it also surfaces as a Put error through the closed
	// pipe. But a producer error that IS a pipe error means the consumer
	// died first, and Put's own error is the root cause there.
	if res.err != nil && !errors.Is(res.err, io.ErrClosedPipe) {
		return nil, res.err
	}
	if putErr != nil {
		return nil, putErr
	}

	cols := []string{}
	if columns != nil {
		if c := columns(); c != nil {
			cols = c
		}
	}
	return &artifact.DatasetRef{
		ArtifactRef: *ref,
		Format:      artifact.FormatNDJSON,
		Columns:     cols,
		RowCount:    res.rows,
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

	// Materializing is the one place a reference can still exhaust the
	// worker: streaming consumers read a batch at a time, but a consumer
	// that cannot stream decodes the whole blob into Go maps here. Left
	// unchecked that is an OOM kill, which takes down every other run on
	// the worker and leaves this one to be failed later by recovery with
	// "interrupted mid-execution" — telling the author nothing. Refusing
	// up front costs one run and says exactly what happened.
	if budget := datasetMemoryBudget(); budget > 0 && ref.SizeBytes > budget {
		return nil, true, fmt.Errorf(
			"output of node %s is too large to materialize: %s encoded (budget %s) across %d rows. "+
				"The node consuming it cannot stream, so the whole dataset has to be decoded into memory. "+
				"Narrow the data upstream, send it to a sink that streams (postgres append/overwrite, csv, json), "+
				"or give the worker more memory (BROKOLI_DATASET_MEMORY_BUDGET overrides the budget)",
			nodeID, humanBytes(ref.SizeBytes), humanBytes(budget), ref.RowCount)
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
	streamThreshold := r.streamThreshold
	if streamThreshold == 0 {
		streamThreshold = DefaultStreamThresholdBytes
	}

	var blobs artifact.Store
	namespace := ""
	// Dry runs never spill, unconditionally. Today they cannot anyway —
	// Engine.DryRun builds its Runner without an artifact store — but that
	// is an accident of wiring, not a guarantee. A dry run is not persisted
	// as a run, so nothing would ever call DeleteRunArtifacts for its
	// namespace, and every spilled preview would be an orphaned blob on
	// disk until someone deleted it by hand.
	if provider, ok := r.artifactStore.(BlobStoreProvider); ok && !r.dryRun && r.run != nil && r.run.ID != "" {
		blobs = provider.Blobs()
		namespace = r.run.ID
	}

	out := newNodeOutputs(blobs, namespace, threshold)
	out.streamThreshold = streamThreshold
	out.onSpill = func(nodeID string, estimatedBytes int64, ref *artifact.DatasetRef) {
		r.log(nodeID, models.LogLevelInfo,
			"Spilled %d row(s) (~%d bytes) to the artifact store; held by reference for the rest of the run",
			ref.RowCount, estimatedBytes)
	}
	return out
}

// PutRef records a node's output that already lives in the blob store as a
// reference, without any in-memory dataset ever having existed
// (docs/adr/019-execution-segments-and-streaming.md, Milestone 1:
// ref-producing handlers hand their output straight here). The consumer
// side is unchanged — Get materializes it exactly like any spilled
// output, and GetRef serves stream-capable consumers.
// PutTable records a node's output as still being in its database.
func (o *nodeOutputs) PutTable(nodeID string, ref *TableRef) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.tables == nil {
		o.tables = make(map[string]*TableRef)
	}
	o.tables[nodeID] = ref
}

// GetTable returns a node's TableRef, if its output stayed in the database.
func (o *nodeOutputs) GetTable(nodeID string) (*TableRef, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ref, ok := o.tables[nodeID]
	return ref, ok
}

func (o *nodeOutputs) PutRef(nodeID string, ref *artifact.DatasetRef) {
	o.mu.Lock()
	delete(o.inline, nodeID)
	o.spilled[nodeID] = ref
	o.mu.Unlock()
}

// GetRef returns the blob reference behind a node's output, if it is held
// by reference (spilled by Put, or produced as a ref via PutRef). An
// inline output returns (nil, false): it is already materialized, and a
// stream-capable consumer gains nothing from re-reading it off disk —
// per ADR-019's engagement rule, small data takes the batch path.
func (o *nodeOutputs) GetRef(nodeID string) (*artifact.DatasetRef, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ref, ok := o.spilled[nodeID]
	return ref, ok
}

// OpenBatches opens a batch reader over a reference previously returned by
// GetRef, using the ref's own recorded column order. Caller closes the
// returned closer when done (also on error paths — the reader holds an
// open blob).
func (o *nodeOutputs) OpenBatches(ref *artifact.DatasetRef) (*NDJSONBatchReader, io.Closer, error) {
	rc, err := o.blobs.Open(context.Background(), &ref.ArtifactRef)
	if err != nil {
		return nil, nil, err
	}
	return NewNDJSONBatchReader(rc, ref.Columns, 0), rc, nil
}
