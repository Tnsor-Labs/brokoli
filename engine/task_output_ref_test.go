package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/datacap"
	"github.com/Tnsor-Labs/brokoli/store"
)

// taskNodeDeclaring builds a task node whose single output port carries
// the given value contract, in the same shape a real interface document
// uses.
func taskNodeDeclaring(value map[string]interface{}) models.Node {
	return models.Node{
		ID:   "task-1",
		Type: models.NodeTypeTask,
		Interface: map[string]interface{}{
			"outputs": map[string]interface{}{
				"result": map[string]interface{}{"value": value},
			},
		},
	}
}

// Which outputs need a store is decided by the DECLARED contract. A
// scalar and a dataset ride inside the result document; an artifact is
// by definition a reference to stored bytes.
func TestOnlyByteProducingOutputsNeedABlobStore(t *testing.T) {
	cases := map[string]struct {
		value map[string]interface{}
		want  bool
	}{
		"scalar": {
			value: map[string]interface{}{"kind": "scalar", "scalar_type": map[string]interface{}{"kind": "int64"}},
			want:  false,
		},
		"dataset": {
			value: map[string]interface{}{"kind": "dataset"},
			want:  false,
		},
		"artifact": {
			value: map[string]interface{}{"kind": "artifact"},
			want:  true,
		},
		"collection of artifacts": {
			value: map[string]interface{}{
				"kind":  "collection",
				"items": map[string]interface{}{"kind": "artifact"},
			},
			want: true,
		},
		// A collection of datasets stays in the result document, so it
		// needs no store -- granting one anyway would widen a compromised
		// worker's reach for nothing.
		"collection of datasets": {
			value: map[string]interface{}{
				"kind":  "collection",
				"items": map[string]interface{}{"kind": "dataset"},
			},
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := taskNeedsOutputBlobStore(taskNodeDeclaring(tc.value)); got != tc.want {
				t.Errorf("taskNeedsOutputBlobStore = %v, want %v", got, tc.want)
			}
		})
	}
}

// A node declaring no interface at all declares no artifact output, so
// it gets no write grant. Absence is honest (ADR-032 section 6).
func TestANodeWithNoInterfaceNeedsNoBlobStore(t *testing.T) {
	if taskNeedsOutputBlobStore(models.Node{ID: "t", Type: models.NodeTypeTask}) {
		t.Error("a node declaring nothing was given a write grant")
	}
}

// The grant names no object, because the ids are content digests the
// task has not produced yet -- and a collection port becomes one object
// per item, a count decided at runtime.
func TestOutputCapabilityIsAttemptScopedNotObjectScoped(t *testing.T) {
	issuer := testIssuer(t)
	token, err := issueOutputCapability(issuer, "org-1", "run-1", "task-1", 0, 4)
	if err != nil {
		t.Fatalf("issueOutputCapability: %v", err)
	}

	// It authorizes writing objects nobody named at issue time.
	for _, obj := range []string{"sha256:one", "sha256:two"} {
		if _, err := issuer.Verify(token, datacap.Request{
			OrgID: "org-1", RunID: "run-1", NodeID: "task-1",
			Attempt: 0, FencingGeneration: 4,
			Direction: datacap.DirectionWrite,
			Namespace: "run-1", ObjectID: obj,
		}); err != nil {
			t.Errorf("write to %s: %v", obj, err)
		}
	}

	// And it authorizes nothing else. A write grant that could also read
	// would let a worker fetch every object in its run.
	if _, err := issuer.Verify(token, datacap.Request{
		OrgID: "org-1", RunID: "run-1", NodeID: "task-1",
		Attempt: 0, FencingGeneration: 4,
		Direction: datacap.DirectionRead,
		Namespace: "run-1", ObjectID: "sha256:one",
	}); err == nil {
		t.Error("the output grant permitted a read")
	}
}

// The property that makes a lost worker harmless: once a retry claims
// the attempt at a higher generation, the old grant stops verifying,
// with nothing having to detect it or revoke it (ADR-017).
func TestASupersededAttemptCannotWriteItsOutput(t *testing.T) {
	issuer := testIssuer(t)
	token, err := issueOutputCapability(issuer, "org-1", "run-1", "task-1", 0, 4)
	if err != nil {
		t.Fatalf("issueOutputCapability: %v", err)
	}
	if _, err := issuer.Verify(token, datacap.Request{
		OrgID: "org-1", RunID: "run-1", NodeID: "task-1",
		Attempt: 0, FencingGeneration: 5, // a retry took over
		Direction: datacap.DirectionWrite,
		Namespace: "run-1", ObjectID: "sha256:anything",
	}); err == nil {
		t.Fatal("a superseded attempt wrote its output")
	}
}

// The claimant turns the grant into its blob store. Without one it gets
// nil, and readTaskArtifactOutput refuses by name -- which is the honest
// answer for a claimant that was never given anywhere to write.
func TestClaimantBuildsItsOutputStoreFromTheGrant(t *testing.T) {
	withGrant := &extensions.InstanceWorkOrder{
		OutputCapability:  "cap-write",
		ControlPlaneURL:   "https://brokoli.example",
		CapabilityAttempt: 2,
	}
	store := workOrderOutputStore(withGrant, "run-1", "task-1")
	if store == nil {
		t.Fatal("a work order carrying a write grant produced no store")
	}
	cs, ok := store.(*artifact.CapabilityStore)
	if !ok {
		t.Fatalf("store is %T, want a CapabilityStore", store)
	}
	if cs.WriteCapability != "cap-write" {
		t.Errorf("WriteCapability = %q, want the order's grant", cs.WriteCapability)
	}
	// The attempt the capability was minted for, not the run's current
	// one: a grant and the attempt it names travel together or neither is
	// usable.
	if cs.Attempt != 2 {
		t.Errorf("Attempt = %d, want the capability's attempt", cs.Attempt)
	}

	for name, wo := range map[string]*extensions.InstanceWorkOrder{
		"no grant":         {ControlPlaneURL: "https://brokoli.example"},
		"no control plane": {OutputCapability: "cap-write"},
		"neither":          {},
	} {
		if got := workOrderOutputStore(wo, "run-1", "task-1"); got != nil {
			t.Errorf("%s: got a store, want nil", name)
		}
	}
}

// A grant is useless without the address to present it to, so the two
// must be dispatched together.
func TestOutputGrantTravelsWithItsControlPlaneURL(t *testing.T) {
	store := workOrderOutputStore(&extensions.InstanceWorkOrder{
		OutputCapability: "cap", ControlPlaneURL: "https://brokoli.example/",
	}, "run-1", "task-1")
	cs := store.(*artifact.CapabilityStore)
	if !strings.HasPrefix(cs.BaseURL, "https://brokoli.example") {
		t.Errorf("BaseURL = %q, want the order's control plane", cs.BaseURL)
	}
}

// ---------------------------------------------------------------------
// Dispatcher wiring.
//
// The unit tests above prove each piece works. This proves the pieces
// are actually connected -- a mutation that stopped the dispatcher
// setting OutputCapability passed every other test in the package, which
// is exactly what an uncovered wiring line looks like.
// ---------------------------------------------------------------------

// captureQueue records the work order and then refuses the enqueue, so
// the dispatcher returns immediately instead of waiting for a worker
// that will never answer. The test is about what was BUILT, not what
// comes back.
type captureQueue struct{ job extensions.RunJob }

func (c *captureQueue) Enqueue(job extensions.RunJob) error {
	c.job = job
	return errors.New("captured")
}
func (c *captureQueue) Dequeue() (extensions.RunJob, error) {
	return extensions.RunJob{}, extensions.ErrQueueClosed
}
func (c *captureQueue) Ack(string) error         { return nil }
func (c *captureQueue) Fail(string, error) error { return nil }
func (c *captureQueue) Len() int                 { return 0 }
func (c *captureQueue) Close() error             { return nil }

// attemptOnlyStore supplies the one thing remote dispatch asks the store
// for. Embedding the interfaces keeps it satisfying them without
// spelling out methods this path never calls.
type attemptOnlyStore struct {
	store.Store
	store.ExecutionAttemptStore
}

func dispatchRigRunner(t *testing.T, q *captureQueue) *Runner {
	t.Helper()
	return &Runner{
		run:              &models.Run{ID: "run-1"},
		pipe:             &models.Pipeline{ID: "pipe-1", OrgID: "org-1"},
		orgID:            "org-1",
		store:            &attemptOnlyStore{},
		artifactStore:    NewLocalDiskArtifactStore(t.TempDir()),
		instanceJobQueue: q,
		dataCapIssuer:    testIssuer(t),
	}
}

func dispatchedOrder(t *testing.T, node models.Node) *extensions.InstanceWorkOrder {
	t.Helper()
	q := &captureQueue{}
	r := dispatchRigRunner(t, q)
	// The enqueue is refused on purpose, so an error here is expected.
	_, _ = r.dispatchTaskInstanceRemotely(node, "sha256:bundle", nil, 60, 0, 7)
	if q.job.WorkOrder == nil {
		t.Fatalf("no work order was enqueued (job = %+v)", q.job)
	}
	return q.job.WorkOrder
}

// A node declaring an artifact output is dispatched WITH a write grant,
// and with the address to present it to -- a grant without one cannot be
// used at all.
func TestDispatchCarriesAWriteGrantForAnArtifactOutput(t *testing.T) {
	wo := dispatchedOrder(t, taskNodeDeclaring(map[string]interface{}{"kind": "artifact"}))

	if wo.OutputCapability == "" {
		t.Fatal("a task declaring an artifact output was dispatched with no write grant")
	}
	if wo.ControlPlaneURL == "" {
		t.Error("a write grant was dispatched with nowhere to present it")
	}
	if wo.CapabilityAttempt != 0 {
		t.Errorf("CapabilityAttempt = %d, want the attempt the grant names", wo.CapabilityAttempt)
	}

	// And it really is the attempt-scoped grant, not something reused.
	cap, err := testIssuer(t).Verify(wo.OutputCapability, datacap.Request{
		OrgID: "org-1", RunID: "run-1", NodeID: "task-1",
		Attempt: 0, FencingGeneration: 7,
		Direction: datacap.DirectionWrite,
		Namespace: "run-1", ObjectID: "sha256:anything",
	})
	if err != nil {
		t.Fatalf("the dispatched grant does not authorize the write it exists for: %v", err)
	}
	if cap.ObjectID != datacap.AnyObject {
		t.Errorf("grant names object %q, want the attempt-scoped wildcard", cap.ObjectID)
	}
}

// A task whose outputs all travel inside the result document gets NO
// write grant. Handing one out anyway would widen a compromised worker's
// reach for no gain.
func TestDispatchWithholdsAWriteGrantWhenNothingIsStored(t *testing.T) {
	wo := dispatchedOrder(t, taskNodeDeclaring(map[string]interface{}{
		"kind": "scalar", "scalar_type": map[string]interface{}{"kind": "int64"},
	}))
	if wo.OutputCapability != "" {
		t.Error("a scalar-only task was dispatched with a write grant it cannot need")
	}
}

// Which store a claimant writes through. A claimant holding its own
// takes the direct path; one without falls back to the grant. Getting
// this backwards would send an in-process worker's bytes out over HTTP
// and back, once per collection item.
func TestClaimantPrefersItsOwnBlobStoreOverTheGrant(t *testing.T) {
	withGrant := &extensions.InstanceWorkOrder{
		OutputCapability: "cap-write",
		ControlPlaneURL:  "https://brokoli.example",
	}

	// Holding a real store: use it, even though a grant is present.
	local := NewLocalDiskArtifactStore(t.TempDir())
	got := taskOutputStore(local, withGrant, "run-1", "task-1")
	if got == nil {
		t.Fatal("a claimant with its own store got none")
	}
	if _, isCapability := got.(*artifact.CapabilityStore); isCapability {
		t.Error("an in-process claimant was routed over HTTP instead of writing directly")
	}

	// Holding none: fall back to the grant.
	got = taskOutputStore(nil, withGrant, "run-1", "task-1")
	if _, isCapability := got.(*artifact.CapabilityStore); !isCapability {
		t.Errorf("a remote claimant got %T, want the capability store", got)
	}

	// Holding neither: nil, so readTaskArtifactOutput refuses by name
	// rather than a nil-store panic deep in the write path.
	if got := taskOutputStore(nil, &extensions.InstanceWorkOrder{}, "run-1", "task-1"); got != nil {
		t.Errorf("got %T, want nil so the refusal is named", got)
	}
}
