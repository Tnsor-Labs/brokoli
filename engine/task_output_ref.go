package engine

import (
	"fmt"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/datacap"
	"github.com/Tnsor-Labs/brokoli/pkg/taskinterface"
)

// Reference-based OUTPUT for remotely dispatched task nodes (ADR-033
// sections 6 and 8) -- the counterpart to task_input_ref.go.
//
// A task's artifact and collection outputs are opaque bytes that belong
// in the content-addressed blob store, and the node's output becomes the
// reference rather than the content. On a LOCAL run the engine holds
// that store directly. A remote claimant holds nothing, so until now an
// artifact output was refused by name on remote dispatch: honest, but it
// meant a whole class of task simply could not be distributed.
//
// Now the order can carry a write grant, and the claimant's blob store
// becomes an artifact.CapabilityStore that presents it.

// outputCapabilityTTL bounds how long a worker may write its results.
//
// Longer than the input grant's window because it must outlive the task's
// own execution rather than just the dispatch delay, and still far short
// of datacap.MaxTTL: nothing revokes a bearer token mid-flight, so a
// grant that outlives the attempt it belongs to is a grant that outlives
// its reason to exist.
const outputCapabilityTTL = 2 * time.Hour

// taskNeedsOutputBlobStore reports whether any declared output port
// produces bytes that must be stored rather than carried in the result.
//
// Decided from the DECLARED interface, never from what the task happens
// to return, for the same reason the read path decides that way: the
// contract is the authority on what a port produces. Deciding from the
// returned value would mean a task could obtain a write grant simply by
// returning a different shape -- exactly the escalation the declared
// contract exists to prevent.
func taskNeedsOutputBlobStore(node models.Node) bool {
	iface := effectiveNodeInterface(node)
	if iface == nil {
		return false
	}
	outputs, _ := iface["outputs"].(map[string]interface{})
	for name := range outputs {
		pv, ok := portValueFromInterface(iface, "outputs", name)
		if !ok {
			continue
		}
		if portValueStoresBytes(pv) {
			return true
		}
	}
	return false
}

// portValueStoresBytes reports whether a port's value kind puts bytes in
// the blob store.
//
// A scalar or a dataset travels inside the result document, so neither
// needs a store. An artifact is by definition a reference to stored
// bytes. A collection needs one exactly when its items do, which is why
// this recurses rather than assuming.
func portValueStoresBytes(pv taskinterface.PortValue) bool {
	switch pv.Kind {
	case taskinterface.ValueArtifact:
		return true
	case taskinterface.ValueCollection:
		if pv.Items == nil {
			// An unspecified item contract could be an artifact. Assume it
			// might be: withholding a grant here turns a working pipeline
			// into a refusal, while granting one is bounded by every other
			// binding on it.
			return true
		}
		return portValueStoresBytes(*pv.Items)
	default:
		return false
	}
}

// issueOutputCapability mints the attempt-scoped write grant.
//
// datacap.AnyObject rather than a named object, because the ids are
// content digests the task has not produced yet and there may be many of
// them. Bound to this attempt's fencing generation like every other
// grant, so a superseded attempt cannot write results the winner would
// be blamed for.
func issueOutputCapability(issuer *datacap.Issuer, orgID, runID, nodeID string, attempt int, fencingGeneration int64) (string, error) {
	return issuer.Issue(datacap.Capability{
		OrgID:             orgID,
		RunID:             runID,
		NodeID:            nodeID,
		Attempt:           attempt,
		FencingGeneration: fencingGeneration,
		Direction:         datacap.DirectionWrite,
		Namespace:         runID,
		ObjectID:          datacap.AnyObject,
		NotAfter:          time.Now().Add(outputCapabilityTTL),
	})
}

// stageTaskOutputCapability issues the write grant a remotely dispatched
// task needs for its artifact or collection outputs.
//
// Both prerequisites are required and each missing one is named. This
// replaces a hard refusal, so falling back silently -- dispatching
// without a grant and letting the worker discover it has nowhere to put
// its output -- would move a clear failure to the far side of the
// network and report it as a task error rather than a configuration one.
func (r *Runner) stageTaskOutputCapability(node models.Node, attempt int, fencingGeneration int64) (capability, planeURL string, err error) {
	if r.taskBlobStore() == nil {
		return "", "", fmt.Errorf(
			"task node %q declares an output that must be stored, but this server has no artifact blob store to hold it",
			node.ID)
	}
	issuer := r.dataCapIssuer
	if issuer == nil {
		return "", "", fmt.Errorf(
			"task node %q declares an output that must be stored, but this server issues no data capabilities",
			node.ID)
	}
	token, err := issueOutputCapability(issuer, r.orgID, r.run.ID, node.ID, attempt, fencingGeneration)
	if err != nil {
		return "", "", fmt.Errorf("task node %q: issue output capability: %w", node.ID, err)
	}
	return token, controlPlaneURL(), nil
}

// taskOutputStore picks where a claimant's outputs go.
//
// A claimant holding its own blob store uses it -- an in-process worker
// on the server, where the bytes never need to cross a network. One
// without it, which is the ordinary remote case, writes through the
// control plane under the attempt-scoped grant its order carries.
//
// Local first, deliberately: a direct store write avoids an HTTP round
// trip per object, and a collection output is one object per item, so
// the difference is a multiple rather than a constant.
func taskOutputStore(artifacts ArtifactStore, wo *extensions.InstanceWorkOrder, runID, nodeID string) artifact.Store {
	if blobs := workOrderBlobStore(artifacts); blobs != nil {
		return blobs
	}
	return workOrderOutputStore(wo, runID, nodeID)
}

// workOrderOutputStore builds the blob store a claimant writes its
// outputs through, or nil when the order carries no write grant.
//
// Returning nil rather than an erroring store is deliberate: a task with
// no artifact output never touches this, and readTaskArtifactOutput
// already refuses by name when a store is genuinely needed and absent.
func workOrderOutputStore(wo *extensions.InstanceWorkOrder, runID, nodeID string) artifact.Store {
	if wo.OutputCapability == "" || wo.ControlPlaneURL == "" {
		return nil
	}
	return &artifact.CapabilityStore{
		BaseURL:         wo.ControlPlaneURL,
		AuthHeader:      workerAuthHeader(),
		RunID:           runID,
		NodeID:          nodeID,
		Attempt:         wo.CapabilityAttempt,
		WriteCapability: wo.OutputCapability,
	}
}
