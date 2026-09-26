package engine

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	actuallyfine "github.com/Tnsor-Labs/actually-fine/adapter/brokoli"
	"github.com/Tnsor-Labs/actually-fine/contract"
	"github.com/Tnsor-Labs/actually-fine/result"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/quality/contractgate"
)

/*
 * A contract gate that only ran in batch put the memory ceiling back
 * into any pipeline it sat in: source and sink either side stream by
 * reference, and a node in the middle that materialises undoes both.
 *
 * The contract engine checks record by record with a stream checker and
 * already drives a pull Source until io.EOF, so nothing about a gate
 * requires the dataset to be whole.
 */

func streamGateContract(action string) contract.Contract {
	return contract.Contract{
		IRVersion: "1.0",
		Metadata:  contract.Metadata{ID: "orders", Version: "1"},
		Input:     contract.Input{Kind: "record-stream"},
		Rules: []contract.Rule{{
			ID: "required-email", Kind: "record", Path: "$.email",
			Predicate: contract.Predicate{Op: "required"},
			OnBreach:  contract.Policy{Action: action},
		}},
	}
}

// gateInput builds a dataset where every third record fails the
// contract, big enough to cross more than one batch.
func gateInput(n int) *common.DataSet {
	in := &common.DataSet{Columns: []string{"id", "email"}}
	for i := 0; i < n; i++ {
		row := common.DataRow{"id": fmt.Sprintf("r%d", i)}
		if i%3 != 0 {
			row["email"] = fmt.Sprintf("u%d@example.com", i)
		}
		in.Rows = append(in.Rows, row)
	}
	return in
}

// The equivalence property: streaming changes where the rows are, not
// which ones clear the gate.
func TestStreamedContractGate_EquivalentToBatchGate(t *testing.T) {
	outputs := newStreamTestOutputs(t)
	in := gateInput(2500)

	batchOut, batchSummary, err := contractgate.Run(context.Background(),
		streamGateContract("quarantine"), in, func(result.Event) error { return nil })
	if err != nil {
		t.Fatalf("batch gate: %v", err)
	}

	if err := outputs.Put("up", in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	inRef, ok := outputs.GetRef("up")
	if !ok {
		t.Fatal("input did not spill to a ref")
	}

	var streamSummary actuallyfine.Summary
	outRef, err := streamOut(outputs, func(write func(*common.DataSet) error) ([]string, error) {
		batches, closer, oerr := outputs.OpenBatches(inRef)
		if oerr != nil {
			return nil, oerr
		}
		defer closer.Close()
		s, gerr := contractgate.RunStreaming(context.Background(), streamGateContract("quarantine"),
			func() (*common.DataSet, error) { return batches.Next() },
			write, func(result.Event) error { return nil })
		streamSummary = s
		return inRef.Columns, gerr
	})
	if err != nil {
		t.Fatalf("streamed gate: %v", err)
	}

	if streamSummary.Total != batchSummary.Total ||
		streamSummary.Cleared != batchSummary.Cleared ||
		streamSummary.Quarantined != batchSummary.Quarantined {
		t.Errorf("summaries differ:\n  streamed = %+v\n  batch    = %+v", streamSummary, batchSummary)
	}
	if outRef.RowCount != int64(len(batchOut.Rows)) {
		t.Fatalf("streamed cleared %d rows, batch cleared %d", outRef.RowCount, len(batchOut.Rows))
	}

	outputs.PutRef("down", outRef)
	streamedDS, ok, err := outputs.Get("down")
	if err != nil || !ok {
		t.Fatalf("materialize streamed output: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(streamedDS.Columns, batchOut.Columns) {
		t.Fatalf("columns differ: streamed=%v batch=%v", streamedDS.Columns, batchOut.Columns)
	}
	for i := range batchOut.Rows {
		if streamedDS.Rows[i]["id"] != batchOut.Rows[i]["id"] {
			t.Fatalf("row %d differs: streamed=%v batch=%v", i, streamedDS.Rows[i], batchOut.Rows[i])
		}
	}
}

// Enforcement must not soften because the rows arrived in batches.
func TestStreamedContractGate_StillFailsOnBreach(t *testing.T) {
	outputs := newStreamTestOutputs(t)
	if err := outputs.Put("up", gateInput(300)); err != nil {
		t.Fatal(err)
	}
	inRef, _ := outputs.GetRef("up")

	_, err := streamOut(outputs, func(write func(*common.DataSet) error) ([]string, error) {
		batches, closer, oerr := outputs.OpenBatches(inRef)
		if oerr != nil {
			return nil, oerr
		}
		defer closer.Close()
		_, gerr := contractgate.RunStreaming(context.Background(), streamGateContract("reject"),
			func() (*common.DataSet, error) { return batches.Next() },
			write, func(result.Event) error { return nil })
		return inRef.Columns, gerr
	})
	if err == nil {
		t.Fatal("a reject breach did not fail the streamed gate; enforcement must not depend on the path")
	}
}

// The gate has to be declared streamable, or none of the above is
// reachable from a real pipeline.
func TestContractGateIsStreamEligible(t *testing.T) {
	outputs := newStreamTestOutputs(t)
	// newStreamTestOutputs sets the spill threshold but not the stream
	// threshold, which streamEligible checks separately. Without this
	// every node type is ineligible and the assertion below would pass
	// for the wrong reason no matter what the switch says.
	outputs.streamThreshold = 1
	r := &Runner{}
	node := models.Node{ID: "g", Type: models.NodeTypeContractGate,
		Config: map[string]interface{}{"contract": map[string]interface{}{}}}
	if !r.streamEligible(node, outputs) {
		t.Error("contract_gate is not stream-eligible, so it materialises its input and " +
			"collapses streaming for the whole pipeline")
	}
	// The control: eligibility is a per-type decision, not a blanket
	// yes. A type the engine has no streamed path for must still be
	// ineligible, or the assertion above proves nothing.
	notStreamed := models.Node{ID: "q", Type: models.NodeTypeQualityCheck}
	if r.streamEligible(notStreamed, outputs) {
		t.Error("quality_check reported stream-eligible; streamEligible is answering yes to everything")
	}
}

// idAtMostFive clears ids 1..5 and quarantines the rest.
func idAtMostFive() contract.Contract {
	max := 5.0
	return contract.Contract{
		IRVersion: "1.0",
		Metadata:  contract.Metadata{ID: "ids", Version: "1"},
		Input:     contract.Input{Kind: "record-stream"},
		Rules: []contract.Rule{{
			ID: "id-at-most-5", Kind: "record", Path: "$.id",
			Predicate: contract.Predicate{Op: "range", Max: &max},
			OnBreach:  contract.Policy{Action: "quarantine"},
		}},
	}
}

// The gate's answer must not depend on how a number happens to be typed.
//
// Brokoli holds a JSON source's numbers as float64 in memory, but decodes
// whole numbers as int64 when a dataset comes back from a spilled blob (to
// keep 64-bit values exact), and a database source hands over int64 for
// integer columns. The contract engine used to accept only float64 as a
// number, so the same contract over the same ten rows cleared 5 when the
// rows were in memory and 0 when they had been spilled -- found on a live
// cluster. The differential test above could not see it: it gates on
// "required", which never reads a value.
func TestContractGateAnswerDoesNotDependOnNumericType(t *testing.T) {
	rows := func(num func(int) any) *common.DataSet {
		ds := &common.DataSet{Columns: []string{"id"}}
		for i := 1; i <= 10; i++ {
			ds.Rows = append(ds.Rows, common.DataRow{"id": num(i)})
		}
		return ds
	}
	noEvidence := func(result.Event) error { return nil }

	for name, ds := range map[string]*common.DataSet{
		"float64 (a JSON source in memory)":        rows(func(i int) any { return float64(i) }),
		"int64 (a spilled dataset, a DB column)":   rows(func(i int) any { return int64(i) }),
		"int (a Go caller building rows directly)": rows(func(i int) any { return i }),
	} {
		_, summary, err := contractgate.Run(context.Background(), idAtMostFive(), ds, noEvidence)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if summary.Cleared != 5 || summary.Quarantined != 5 {
			t.Errorf("%s: cleared %d, quarantined %d; want 5 and 5 whatever the type",
				name, summary.Cleared, summary.Quarantined)
		}
	}

	// And through the real streaming path, where the int64 comes from.
	outputs := newStreamTestOutputs(t)
	if err := outputs.Put("up", rows(func(i int) any { return float64(i) })); err != nil {
		t.Fatal(err)
	}
	inRef, ok := outputs.GetRef("up")
	if !ok {
		t.Fatal("input did not spill to a ref")
	}
	var streamed actuallyfine.Summary
	if _, err := streamOut(outputs, func(write func(*common.DataSet) error) ([]string, error) {
		batches, closer, oerr := outputs.OpenBatches(inRef)
		if oerr != nil {
			return nil, oerr
		}
		defer closer.Close()
		s, gerr := contractgate.RunStreaming(context.Background(), idAtMostFive(),
			func() (*common.DataSet, error) { return batches.Next() }, write, noEvidence)
		streamed = s
		return inRef.Columns, gerr
	}); err != nil {
		t.Fatalf("streamed gate: %v", err)
	}
	if streamed.Cleared != 5 || streamed.Quarantined != 5 {
		t.Errorf("streamed over a spilled ref: cleared %d, quarantined %d; want 5 and 5 -- "+
			"the same rows gave a different answer once they had been spilled",
			streamed.Cleared, streamed.Quarantined)
	}
}
