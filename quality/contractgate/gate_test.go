package contractgate

import (
	"context"
	"testing"

	"github.com/Tnsor-Labs/actually-fine/contract"
	"github.com/Tnsor-Labs/actually-fine/result"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func requiredEmailContract(action string) contract.Contract {
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

func TestRunQuarantinesAndPassesAcceptedRows(t *testing.T) {
	var events []result.Event
	output, summary, err := Run(context.Background(), requiredEmailContract("quarantine"), &common.DataSet{
		Columns: []string{"id", "email"},
		Rows:    []common.DataRow{{"id": "ok", "email": "a@example.com"}, {"id": "bad"}},
	}, func(event result.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Cleared != 1 || summary.Quarantined != 1 || len(output.Rows) != 1 || len(events) != 1 {
		t.Fatalf("unexpected result: summary=%+v output=%+v events=%d", summary, output.Rows, len(events))
	}
	if output.Rows[0]["id"] != "ok" || events[0].Action != "quarantine" {
		t.Fatalf("unexpected routing/evidence: output=%v event=%+v", output.Rows, events[0])
	}
}

func TestRunRejectFailsAfterEvidence(t *testing.T) {
	events := 0
	_, summary, err := Run(context.Background(), requiredEmailContract("reject"), &common.DataSet{
		Columns: []string{"email"},
		Rows:    []common.DataRow{{}},
	}, func(result.Event) error {
		events++
		return nil
	})
	if err == nil || summary.Breached != 1 || events != 1 {
		t.Fatalf("expected enforcement failure: summary=%+v events=%d err=%v", summary, events, err)
	}
}
