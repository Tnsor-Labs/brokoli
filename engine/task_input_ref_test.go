package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/datacap"
)

func testIssuer(t *testing.T) *datacap.Issuer {
	t.Helper()
	i, err := datacap.NewIssuer([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func bigInput(rows int) *common.DataSet {
	ds := &common.DataSet{Columns: []string{"id", "city"}}
	for i := 0; i < rows; i++ {
		ds.Rows = append(ds.Rows, common.DataRow{"id": int64(i), "city": "porto"})
	}
	return ds
}

// Above the inline cap, a task's input is staged in the blob store and
// the order carries a digest -- not rows, and not a refusal.
//
// This is what replaced a hard "over the N-row cap" error that left a
// pipeline no way forward.
func TestTaskInputOverTheCapIsStagedByReference(t *testing.T) {
	blobs := artifact.NewLocalDiskStore(t.TempDir())
	input := bigInput(maxInlineTaskInputRows + 1)

	digest, err := spillTaskInput(context.Background(), blobs, "run-1", input)
	if err != nil {
		t.Fatalf("spillTaskInput: %v", err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest = %q, want a sha256 reference", digest)
	}

	// The staged bytes must be exactly what a harness reads: NDJSON, one
	// row per line, decoded by the same path every other task input uses.
	rc, err := blobs.Open(context.Background(), &artifact.ArtifactRef{
		URI: mustResolve(t, blobs, "run-1", digest).URI, Checksum: digest,
	})
	if err != nil {
		t.Fatalf("open staged input: %v", err)
	}
	defer func() { _ = rc.Close() }()
	rows, err := decodeNDJSONRows(rc)
	if err != nil {
		t.Fatalf("decode staged input: %v", err)
	}
	if len(rows) != len(input.Rows) {
		t.Fatalf("staged %d rows, want %d", len(rows), len(input.Rows))
	}
	// 64-bit fidelity holds through the staging round trip too.
	if got, ok := rows[0]["id"].(int64); !ok || got != 0 {
		t.Errorf("row 0 id = %#v (%T), want int64", rows[0]["id"], rows[0]["id"])
	}
}

func mustResolve(t *testing.T, blobs artifact.Store, ns, digest string) *artifact.ArtifactRef {
	t.Helper()
	r, ok := blobs.(artifact.DigestResolver)
	if !ok {
		t.Fatal("blob store cannot resolve by digest")
	}
	ref, err := r.ResolveDigest(ns, digest)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// The capability minted for a staged input is bound to the attempt that
// staged it, so a superseded attempt cannot fetch with it -- the same
// fencing property that governs writes (ADR-017).
func TestInputCapabilityIsBoundToItsAttempt(t *testing.T) {
	issuer := testIssuer(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	tok, err := issueInputCapability(issuer, "org-1", "run-1", "task", 0, 7, digest)
	if err != nil {
		t.Fatalf("issueInputCapability: %v", err)
	}

	base := datacap.Request{
		OrgID: "org-1", RunID: "run-1", NodeID: "task", Attempt: 0,
		FencingGeneration: 7, Direction: datacap.DirectionRead,
		Namespace: "run-1", ObjectID: digest,
	}
	if _, err := issuer.Verify(tok, base); err != nil {
		t.Fatalf("the capability does not verify for its own attempt: %v", err)
	}

	superseded := base
	superseded.FencingGeneration = 8
	if _, err := issuer.Verify(tok, superseded); err == nil {
		t.Fatal("a superseded attempt could still fetch the input")
	}

	// And it grants reading only: staging an input must not hand a worker
	// a way to write.
	asWrite := base
	asWrite.Direction = datacap.DirectionWrite
	if _, err := issuer.Verify(tok, asWrite); err == nil {
		t.Fatal("an input capability authorized a write")
	}
}

// A reference with no capability is refused rather than attempted. The
// digest alone grants nothing, which is the property that makes it safe
// to put in a work order at all.
func TestFetchTaskInputRefusesAReferenceWithNoCapability(t *testing.T) {
	_, err := fetchTaskInputByReference(context.Background(), &extensions.InstanceWorkOrder{
		InputRef:        "sha256:" + strings.Repeat("b", 64),
		ControlPlaneURL: "http://127.0.0.1:1",
	}, "run-1", "task")
	if err == nil {
		t.Fatal("a reference with no capability was fetched")
	}
	if !strings.Contains(err.Error(), "capability") {
		t.Errorf("err = %v, want it to name the missing capability", err)
	}
}

// Staging needs a blob store and an issuer. Each missing one is a named
// refusal, never a silent fallback to a truncated input -- that would
// turn the clear failure this path replaced into a wrong answer.
func TestStagingRefusesByNameWithoutItsPrerequisites(t *testing.T) {
	r := &Runner{run: &models.Run{ID: "run-1"}, orgID: "org-1"}
	node := models.Node{ID: "task"}

	_, _, _, err := r.stageTaskInputByReference(node, bigInput(3), 0, 7)
	if err == nil || !strings.Contains(err.Error(), "blob store") {
		t.Fatalf("err = %v, want a named refusal about the missing blob store", err)
	}

	r.artifactStore = NewLocalDiskArtifactStore(t.TempDir())
	_, _, _, err = r.stageTaskInputByReference(node, bigInput(3), 0, 7)
	if err == nil || !strings.Contains(err.Error(), "capabilities") {
		t.Fatalf("err = %v, want a named refusal about the missing issuer", err)
	}

	r.dataCapIssuer = testIssuer(t)
	ref, tok, plane, err := r.stageTaskInputByReference(node, bigInput(3), 0, 7)
	if err != nil {
		t.Fatalf("staging failed with both prerequisites present: %v", err)
	}
	if ref == "" || tok == "" || plane == "" {
		t.Fatalf("staging returned an incomplete reference: ref=%q tok=%q plane=%q", ref, tok, plane)
	}
	_ = time.Now
}

// The fetch has to address the attempt the capability was minted for.
// The capability binds an attempt and the server checks it against the
// URL, so a work order whose CapabilityAttempt never reached the URL
// would fail authorization at run time for a reason that looks nothing
// like the cause.
//
// Served by a stub rather than the real handler because engine cannot
// import api (api imports engine). The client/handler contract itself is
// covered in api by TestBlobEndpointAndCapabilityStoreAgree; what is
// verified here is only the wiring this package owns.
func TestFetchTaskInputAddressesTheCapabilityAttempt(t *testing.T) {
	staged := "{\"id\":9007199254740993,\"city\":\"porto\"}\n"
	sum := sha256.Sum256([]byte(staged))
	digest := "sha256:" + hex.EncodeToString(sum[:])

	var gotPath, gotCap string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCap = r.Header.Get("X-Brokoli-Data-Capability")
		_, _ = io.WriteString(w, staged)
	}))
	defer srv.Close()

	ds, err := fetchTaskInputByReference(context.Background(), &extensions.InstanceWorkOrder{
		InputRef:          digest,
		InputCapability:   "the-capability",
		ControlPlaneURL:   srv.URL,
		CapabilityAttempt: 3,
		InputColumns:      []string{"id", "city"},
	}, "run-1", "task")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	if !strings.Contains(gotPath, "/attempts/3/") {
		t.Errorf("path = %q, want it to name attempt 3 -- the attempt the capability is bound to", gotPath)
	}
	if !strings.HasSuffix(gotPath, "/blobs/"+digest) {
		t.Errorf("path = %q, want it to end in the object digest", gotPath)
	}
	if gotCap != "the-capability" {
		t.Errorf("capability header = %q", gotCap)
	}

	// And the rows arrive intact, 64-bit ids included.
	if len(ds.Rows) != 1 {
		t.Fatalf("rows = %#v, want one", ds.Rows)
	}
	if got, ok := ds.Rows[0]["id"].(int64); !ok || got != 9007199254740993 {
		t.Errorf("id = %#v (%T), want int64(9007199254740993)", ds.Rows[0]["id"], ds.Rows[0]["id"])
	}
}

// Content that does not match the digest is refused, so a task never
// runs on altered rows.
func TestFetchTaskInputDetectsAlteredContent(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "{\"id\":1}\n")
	}))
	defer srv.Close()

	_, err := fetchTaskInputByReference(context.Background(), &extensions.InstanceWorkOrder{
		InputRef: digest, InputCapability: "cap", ControlPlaneURL: srv.URL,
	}, "run-1", "task")
	if err == nil {
		t.Fatal("content that did not match the digest was accepted")
	}
}
