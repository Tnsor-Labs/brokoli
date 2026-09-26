// Package contractgate adapts actually-fine's contract engine to Brokoli's
// native dataset execution model. It deliberately does not translate the
// contract into Brokoli's legacy quality.Check model.
package contractgate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	actuallyfine "github.com/Tnsor-Labs/actually-fine/adapter/brokoli"
	"github.com/Tnsor-Labs/actually-fine/contract"
	"github.com/Tnsor-Labs/actually-fine/result"
	"github.com/Tnsor-Labs/actually-fine/transport"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

type source struct {
	batch transport.RecordBatch
	used  bool
}

func (s *source) Next(context.Context) (transport.RecordBatch, error) {
	if s.used {
		return transport.RecordBatch{}, io.EOF
	}
	s.used = true
	return s.batch, nil
}

// DecodeContract decodes an inline canonical actually-fine contract from a
// node's JSON config. Unknown fields and unsupported versions are rejected by
// the canonical decoder before any input row is processed.
func DecodeContract(raw any) (contract.Contract, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return contract.Contract{}, fmt.Errorf("marshal contract config: %w", err)
	}
	decoded, err := contract.Decode(data)
	if err != nil {
		return contract.Contract{}, err
	}
	return decoded, nil
}

// Run evaluates a native Brokoli dataset and returns only clear records.
// Quarantined records are excluded from the output. Reject and halt actions return an error after evidence
// has been emitted, so the node cannot silently pass an enforcement breach.
func Run(ctx context.Context, c contract.Contract, input *common.DataSet, evidence actuallyfine.EvidenceSink) (*common.DataSet, actuallyfine.Summary, error) {
	if input == nil {
		return nil, actuallyfine.Summary{}, fmt.Errorf("contract_gate requires input data")
	}
	accepted := make([]map[string]any, 0, len(input.Rows))
	summary, err := actuallyfine.RunBatches(actuallyfine.BatchRequest{
		Contract: c,
		Source:   &source{batch: transport.RecordBatch{Columns: input.Columns, Records: rows(input)}},
		Accepted: func(_ context.Context, batch transport.RecordBatch) error {
			accepted = append(accepted, batch.Records...)
			return nil
		},
		Evidence: evidence,
		Context:  ctx,
	})
	if err != nil {
		return nil, summary, err
	}
	output := &common.DataSet{Columns: append([]string(nil), input.Columns...), Rows: make([]common.DataRow, len(accepted))}
	for i, row := range accepted {
		output.Rows[i] = common.DataRow(row)
	}
	return output, summary, enforcementError(summary)
}

func rows(input *common.DataSet) []map[string]any {
	result := make([]map[string]any, len(input.Rows))
	for i, row := range input.Rows {
		result[i] = map[string]any(row)
	}
	return result
}

func enforcementError(summary actuallyfine.Summary) error {
	if summary.Breached > 0 || summary.Halted {
		return fmt.Errorf("contract gate failed: %d breached, %d quarantined, halted=%t", summary.Breached, summary.Quarantined, summary.Halted)
	}
	return nil
}

var _ actuallyfine.EvidenceSink = func(result.Event) error { return nil }

// batchSource feeds the contract engine from a pull iterator instead of
// one materialised dataset, so a gate can run over a dataset larger than
// memory. actually-fine's own loop already drives Source.Next until
// io.EOF and calls Accepted per batch (adapter/brokoli/batch.go), and it
// checks with engine.NewStreamChecker, so nothing here has to hold the
// input to evaluate it.
type batchSource struct {
	next func() (transport.RecordBatch, error)
}

func (s *batchSource) Next(context.Context) (transport.RecordBatch, error) { return s.next() }

// BatchOf converts one Brokoli dataset into the contract engine's batch
// shape. Exported so the engine's streaming path can build batches from
// its own reader without duplicating the conversion.
func BatchOf(ds *common.DataSet) transport.RecordBatch {
	return transport.RecordBatch{Columns: ds.Columns, Records: rows(ds)}
}

// RowsOf converts an accepted batch back into Brokoli rows.
func RowsOf(batch transport.RecordBatch) []common.DataRow {
	out := make([]common.DataRow, len(batch.Records))
	for i, rec := range batch.Records {
		out[i] = common.DataRow(rec)
	}
	return out
}

// RunStreaming evaluates a contract over a stream of batches, handing
// each batch of cleared records to write as it is produced.
//
// Same enforcement as Run: quarantined records are excluded, and a
// breach or halt returns an error after the evidence for it has been
// emitted. The difference is only where the rows live -- nothing
// accumulates the output, so the gate does not reintroduce the memory
// ceiling on a pipeline whose source and sink both stream.
func RunStreaming(
	ctx context.Context,
	c contract.Contract,
	nextBatch func() (*common.DataSet, error),
	write func(*common.DataSet) error,
	evidence actuallyfine.EvidenceSink,
) (actuallyfine.Summary, error) {
	source := &batchSource{next: func() (transport.RecordBatch, error) {
		ds, err := nextBatch()
		if err != nil {
			return transport.RecordBatch{}, err
		}
		return BatchOf(ds), nil
	}}
	summary, err := actuallyfine.RunBatches(actuallyfine.BatchRequest{
		Contract: c,
		Source:   source,
		Accepted: func(_ context.Context, batch transport.RecordBatch) error {
			return write(&common.DataSet{Columns: batch.Columns, Rows: RowsOf(batch)})
		},
		Evidence: evidence,
		Context:  ctx,
	})
	if err != nil {
		return summary, err
	}
	return summary, enforcementError(summary)
}
