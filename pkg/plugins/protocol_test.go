package plugins

import (
	"bytes"
	"strings"
	"testing"
)

// TestProgress_RoundTrip locks the wire format: a Progress survives
// EncodeLine → DecodeStream unchanged, including the nil-Total case
// that distinguishes "total unknown" from "total is zero".
func TestProgress_RoundTrip(t *testing.T) {
	t.Run("all fields", func(t *testing.T) {
		current, total := int64(21), int64(100)
		in := Progress{
			Current: &current, Total: &total, Unit: "pages",
			RowsIn: 5, RowsOut: 6300,
			BytesIn: 50200000, BytesOut: 31800000,
			Rate: 1.7, Message: "Fetched offset 6000",
		}

		var buf bytes.Buffer
		if err := EncodeLine(&buf, NewProgress(in)); err != nil {
			t.Fatalf("EncodeLine: %v", err)
		}

		var got *Progress
		if err := DecodeStream(&buf, func(m Message) error {
			if m.Type == MsgProgress {
				got = m.Progress
			}
			return nil
		}); err != nil {
			t.Fatalf("DecodeStream: %v", err)
		}
		if got == nil {
			t.Fatal("no MsgProgress decoded")
		}
		if got.Current == nil || *got.Current != 21 {
			t.Errorf("Current: got %v, want 21", got.Current)
		}
		if got.Total == nil || *got.Total != 100 {
			t.Errorf("Total: got %v, want 100", got.Total)
		}
		if got.Unit != "pages" || got.RowsOut != 6300 || got.BytesOut != 31800000 {
			t.Errorf("scalar fields not preserved: %+v", got)
		}
		if got.Rate != 1.7 || got.Message != "Fetched offset 6000" {
			t.Errorf("Rate/Message not preserved: %+v", got)
		}
		// NewProgress stamps Timestamp when the caller leaves it empty.
		if got.Timestamp == "" {
			t.Error("Timestamp should have been stamped by NewProgress")
		}
	})

	t.Run("indeterminate total", func(t *testing.T) {
		current := int64(7)
		var buf bytes.Buffer
		if err := EncodeLine(&buf, NewProgress(Progress{Current: &current, Unit: "pages"})); err != nil {
			t.Fatalf("EncodeLine: %v", err)
		}
		var got *Progress
		if err := DecodeStream(&buf, func(m Message) error {
			if m.Type == MsgProgress {
				got = m.Progress
			}
			return nil
		}); err != nil {
			t.Fatalf("DecodeStream: %v", err)
		}
		if got == nil {
			t.Fatal("no MsgProgress decoded")
		}
		if got.Total != nil {
			t.Errorf("Total should stay nil (unknown), got %v", *got.Total)
		}
		if got.Current == nil || *got.Current != 7 {
			t.Errorf("Current: got %v, want 7", got.Current)
		}
	})
}

// TestDecodeStream_UnknownTypeIgnored guards the compatibility rule
// MsgProgress relies on: a host must ignore message types it does not
// know rather than failing the stream. Without this, adding any future
// message type would break every older host.
func TestDecodeStream_UnknownTypeIgnored(t *testing.T) {
	in := `{"type":"future_thing","whatever":1}
{"type":"record","data":{"id":1}}
{"type":"progress","progress":{"current":1}}
`
	var records int
	if err := DecodeStream(strings.NewReader(in), func(m Message) error {
		if m.Type == MsgRecord {
			records++
		}
		return nil
	}); err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	if records != 1 {
		t.Errorf("unknown message type disrupted the stream: got %d records, want 1", records)
	}
}