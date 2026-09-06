package taskruntime

import "encoding/json"

// ResolvedExecutionRecord is the deterministic, control-plane-owned
// payload selection ADR-033 section 4 requires before a 'task' node
// attempt is dispatched -- pinned before ClaimAttempt so a retry reuses
// the exact same bytes. Matches
// docs/schema/resolved-execution-record-v1.json exactly; field names
// mirror the instance work-order envelope's 'task' object (ADR-033
// section 6) so a future implementation copies rather than re-derives
// them.
type ResolvedExecutionRecord struct {
	RuntimeProtocol            string `json:"runtime_protocol"`
	BundleDigest               string `json:"bundle_digest"`
	PayloadID                  string `json:"payload_id"`
	PayloadDigest              string `json:"payload_digest"`
	ExecutionEnvironmentDigest string `json:"execution_environment_digest"`
	InterfaceDigest            string `json:"interface_digest"`
	// ExecutionProfile is "name@revision", e.g. "standard@7" -- the
	// revision pins policy identity the same way a digest pins bytes.
	ExecutionProfile string `json:"execution_profile"`
}

// ParseResolvedExecutionRecord decodes a resolved-execution-record-v1.json
// document. Structural decoding only; see ParseWorkerCapabilities's doc
// comment for why schema validation stays a separate, explicit step.
func ParseResolvedExecutionRecord(data []byte) (*ResolvedExecutionRecord, error) {
	var r ResolvedExecutionRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
