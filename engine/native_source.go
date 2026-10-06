package engine

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// A source_db node reads through a native ADBC driver when, and only when,
// its saved connection is pinned to one. That is decided from the stored
// connection by conn_id, never from anything in the node's own config: a
// pipeline author can write any key into a node, and an earlier design that
// read the driver's library path from node config let one choose which
// native library the worker loaded.

// errNativeEnoughRows ends a materialized native read that has the rows it
// needs (a dry run's sample).
var errNativeEnoughRows = errors.New("native ADBC read has enough rows")

// nativeSourceRequest is the native query for node, or nil when node does not
// read through a native driver.
func (r *Runner) nativeSourceRequest(ctx context.Context, node models.Node) (*NativeADBCRequest, error) {
	if r.connResolver == nil || node.Type != models.NodeTypeSourceDB {
		return nil, nil
	}
	request, err := r.connResolver.NativeSource(ctx, node.Config, r.credScope(node.ID))
	if err != nil || request == nil {
		return nil, err
	}
	query, _ := node.Config["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("source_db node requires 'query' config")
	}
	// Run parameters bind as driver placeholders, and each ADBC driver has
	// its own placeholder syntax and binding through Arrow records. Until
	// that exists, refuse rather than send "{{...}}" to the database as
	// literal SQL text.
	if hasSQLParamReference(query) {
		return nil, fmt.Errorf("source_db: run parameters in the query are not supported yet for a connection that reads through a native ADBC driver")
	}
	request.Query = query
	return request, nil
}

// runNativeSourceStreamed writes the worker's Arrow IPC stream straight into
// the artifact store: no temporary file, no copy, no second pass.
func (r *Runner) runNativeSourceStreamed(ctx context.Context, node models.Node, outputs *nodeOutputs, request *NativeADBCRequest, attempt int) (nodeExecutionResult, error) {
	if r.nativeADBCWorker == nil {
		return nodeExecutionResult{}, errNativeWorkerNotConfigured
	}
	r.recordExecutedSQL(node.ID, attempt, request.Query)
	pr, pw := io.Pipe()
	type outcome struct {
		result NativeADBCResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := r.nativeADBCWorker.RunNativeADBC(ctx, *request, pw)
		_ = pw.CloseWithError(err) // nil closes cleanly; an error reaches Put's read
		done <- outcome{result, err}
	}()
	ref, putErr := outputs.blobs.Put(ctx, outputs.namespace, pr, artifact.PutOptions{MediaType: artifact.MediaTypeArrowIPC})
	_ = pr.CloseWithError(putErr) // unblock the worker if Put stopped reading early
	got := <-done
	if got.err != nil {
		return nodeExecutionResult{}, got.err
	}
	if putErr != nil {
		return nodeExecutionResult{}, fmt.Errorf("store native ADBC result: %w", putErr)
	}
	datasetRef := &artifact.DatasetRef{ArtifactRef: *ref, Format: artifact.FormatArrowIPC, Columns: got.result.Columns, RowCount: got.result.Rows}
	r.log(node.ID, models.LogLevelInfo, "Streamed %d rows, %d columns through native driver %s %s as Arrow IPC (never materialized)",
		datasetRef.RowCount, len(datasetRef.Columns), request.Driver.Name, request.Driver.Version)
	return nodeExecutionResult{outputRef: datasetRef}, nil
}

// runNativeSource reads the worker's result into memory, for the paths that
// need a materialized dataset: a dry run, or a run with no artifact store.
// maxRows > 0 stops the query once that many rows have arrived.
func (r *Runner) runNativeSource(ctx context.Context, node models.Node, request *NativeADBCRequest, attempt, maxRows int) (nodeExecutionResult, error) {
	if r.nativeADBCWorker == nil {
		return nodeExecutionResult{}, errNativeWorkerNotConfigured
	}
	r.recordExecutedSQL(node.ID, attempt, request.Query)
	pr, pw := io.Pipe()
	type outcome struct{ err error }
	done := make(chan outcome, 1)
	go func() {
		_, err := r.nativeADBCWorker.RunNativeADBC(ctx, *request, pw)
		_ = pw.CloseWithError(err)
		done <- outcome{err}
	}()
	ds, readErr := readNativeRows(pr, maxRows)
	_ = pr.CloseWithError(readErr) // stops the worker when we stopped early
	got := <-done
	if got.err != nil && !errors.Is(got.err, errNativeEnoughRows) {
		return nodeExecutionResult{}, got.err
	}
	if readErr != nil && !errors.Is(readErr, errNativeEnoughRows) {
		return nodeExecutionResult{}, fmt.Errorf("read native ADBC result: %w", readErr)
	}
	r.log(node.ID, models.LogLevelInfo, "Queried %d rows, %d columns through native driver %s %s",
		len(ds.Rows), len(ds.Columns), request.Driver.Name, request.Driver.Version)
	return nodeExecutionResult{output: ds}, nil
}

// readNativeRows decodes an Arrow IPC stream into a dataset, returning
// errNativeEnoughRows once maxRows (when positive) have been read.
func readNativeRows(r io.Reader, maxRows int) (*common.DataSet, error) {
	reader, err := NewArrowBatchReader(r, nil)
	if err != nil {
		return nil, err
	}
	defer reader.Release()
	ds := &common.DataSet{Columns: reader.Columns()}
	for {
		batch, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return ds, nil
		}
		if err != nil {
			return ds, err
		}
		ds.Rows = append(ds.Rows, batch.Rows...)
		if maxRows > 0 && len(ds.Rows) >= maxRows {
			ds.Rows = ds.Rows[:maxRows]
			return ds, errNativeEnoughRows
		}
	}
}

var errNativeWorkerNotConfigured = errors.New("this server has no isolated native worker configured, so it cannot read through a native ADBC driver")
