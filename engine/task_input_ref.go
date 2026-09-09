package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/datacap"
)

// Reference-based input for remotely dispatched task nodes (ADR-033
// sections 6 and 8).
//
// A task's input used to travel inside the work order, which is fine for
// a modest dataset and wrong for a large one -- so anything over
// maxInlineTaskInputRows was refused outright, naming the cap. That
// refusal was honest but it left the pipeline with no way forward.
//
// Now a dataset over the cap is written to the blob store once and the
// order carries a content digest plus a read capability for it. The
// claimant can do nothing with that pair except present it back to the
// control plane, which is what ADR-033 section 6 means by "references
// are opaque control-plane-issued capabilities, never locations".

// inputCapabilityTTL bounds how long a worker may fetch its input for.
//
// Longer than a typical dispatch-to-start delay and far shorter than
// datacap.MaxTTL: nothing revokes a bearer token mid-flight, so a grant
// that outlives the attempt it belongs to is a grant that outlives its
// reason to exist.
const inputCapabilityTTL = 30 * time.Minute

// controlPlaneURL is where a claimant presents its capabilities.
//
// BROKOLI_SERVER_URL is the existing convention for "this server, as
// something else on the network reaches it" (see pkg/fetchers and
// pkg/netguard, which both already read it). Reusing it means an
// operator who has configured it once does not configure it again.
func controlPlaneURL() string {
	if base := os.Getenv("BROKOLI_SERVER_URL"); base != "" {
		return base
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return "http://127.0.0.1:" + port
}

// spillTaskInput writes rows to the blob store as NDJSON and returns
// their content digest.
//
// NDJSON, matching what the harness already reads for a staged input
// (ADR-033 section 8's baseline codec), so the claimant stages the
// fetched bytes directly rather than transcoding a second format.
func spillTaskInput(ctx context.Context, blobs artifact.Store, runID string, input *common.DataSet) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i, row := range input.Rows {
		if err := enc.Encode(map[string]interface{}(row)); err != nil {
			return "", fmt.Errorf("encode input row %d: %w", i, err)
		}
	}
	ref, err := blobs.Put(ctx, runID, bytes.NewReader(buf.Bytes()), artifact.PutOptions{
		MediaType: "application/x-ndjson",
	})
	if err != nil {
		return "", fmt.Errorf("store task input: %w", err)
	}
	return ref.Checksum, nil
}

// issueInputCapability mints the read grant for one staged input.
//
// Bound to this attempt's fencing generation like every other
// capability, so a superseded attempt's grant stops working the moment a
// retry claims the attempt -- the same property that stops it writing.
func issueInputCapability(issuer *datacap.Issuer, orgID, runID, nodeID string, attempt int, fencingGeneration int64, digest string) (string, error) {
	return issuer.Issue(datacap.Capability{
		OrgID:             orgID,
		RunID:             runID,
		NodeID:            nodeID,
		Attempt:           attempt,
		FencingGeneration: fencingGeneration,
		Direction:         datacap.DirectionRead,
		Namespace:         runID,
		ObjectID:          digest,
		Checksum:          digest,
		NotAfter:          time.Now().Add(inputCapabilityTTL),
	})
}

// errTaskInputTooLargeToInline says the dataset cannot ride inside a
// work order. A distinct error rather than a message, so the dispatcher
// can act on it instead of only reporting it.
var errTaskInputTooLargeToInline = errors.New("task input is too large to inline")

// stageTaskInputByReference spills a task's input and mints the read
// capability for it.
//
// Every prerequisite is required, and each missing one is named. This
// path exists to replace a hard refusal, so falling back silently -- to
// a truncated input, or to no input at all -- would turn a clear failure
// into a wrong answer, which is strictly worse than the refusal it
// replaced.
func (r *Runner) stageTaskInputByReference(node models.Node, input *common.DataSet, attempt int, fencingGeneration int64) (ref, capability, planeURL string, err error) {
	blobs := r.taskBlobStore()
	if blobs == nil {
		return "", "", "", fmt.Errorf(
			"task node %q has %d input rows, over the %d-row inline cap, and this server has no artifact blob store to stage it in",
			node.ID, len(input.Rows), maxInlineTaskInputRows)
	}
	issuer := r.dataCapIssuer
	if issuer == nil {
		return "", "", "", fmt.Errorf(
			"task node %q needs reference-based input (%d rows, over the %d-row cap) but this server issues no data capabilities",
			node.ID, len(input.Rows), maxInlineTaskInputRows)
	}

	digest, err := spillTaskInput(context.Background(), blobs, r.run.ID, input)
	if err != nil {
		return "", "", "", fmt.Errorf("task node %q: %w", node.ID, err)
	}
	token, err := issueInputCapability(issuer, r.orgID, r.run.ID, node.ID, attempt, fencingGeneration, digest)
	if err != nil {
		return "", "", "", fmt.Errorf("task node %q: issue input capability: %w", node.ID, err)
	}
	return digest, token, controlPlaneURL(), nil
}

// fetchTaskInputByReference retrieves an input the dispatcher staged in
// the blob store, using the capability that accompanied it.
//
// The CapabilityStore verifies the content against the digest, so a
// truncated or substituted response is an error here rather than a task
// running on altered rows. Decoded with decodeRow for the same reason
// every other task path does: a plain json.Unmarshal turns every number
// into a float64, and a float64 cannot hold a 64-bit id (#479, #496).
func fetchTaskInputByReference(ctx context.Context, wo *extensions.InstanceWorkOrder, runID, nodeID string) (*common.DataSet, error) {
	if wo.InputCapability == "" || wo.ControlPlaneURL == "" {
		return nil, fmt.Errorf(
			"work order names input %s by reference but carries no %s to fetch it with",
			wo.InputRef, map[bool]string{true: "control plane URL", false: "capability"}[wo.ControlPlaneURL == ""])
	}
	store := &artifact.CapabilityStore{
		BaseURL:      wo.ControlPlaneURL,
		AuthHeader:   workerAuthHeader(),
		RunID:        runID,
		NodeID:       nodeID,
		Attempt:      wo.CapabilityAttempt,
		Capabilities: map[string]string{wo.InputRef: wo.InputCapability},
	}
	rc, err := store.Open(ctx, &artifact.ArtifactRef{Checksum: wo.InputRef})
	if err != nil {
		return nil, fmt.Errorf("fetch task input %s: %w", wo.InputRef, err)
	}
	defer func() { _ = rc.Close() }()

	rows, err := decodeNDJSONRows(rc)
	if err != nil {
		return nil, fmt.Errorf("decode task input %s: %w", wo.InputRef, err)
	}
	return &common.DataSet{Columns: wo.InputColumns, Rows: rows}, nil
}
