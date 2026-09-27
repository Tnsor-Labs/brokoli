package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/store"
)

// #753: a paginated source_api page dispatched to a worker carried the
// node's resolved config, so the connection's password and auth headers
// travelled in every work order on the queue. A page now names its
// connection and the worker resolves it.

const (
	pagePassword = "page-s3cret-753"
	pageAPIKey   = "page-key-753"
)

// recordingPageQueue runs each page job on a "worker" with the given
// resolver, and keeps the job exactly as it would cross the queue.
type recordingPageQueue struct {
	store     store.Store
	artifacts ArtifactStore
	resolver  *ConnectionResolver

	mu   sync.Mutex
	sent []string
}

func (q *recordingPageQueue) Enqueue(job extensions.RunJob) error {
	wire, err := json.Marshal(job)
	if err != nil {
		return err
	}
	q.mu.Lock()
	q.sent = append(q.sent, string(wire))
	q.mu.Unlock()
	go func() { _ = ExecuteInstanceJobResolving(q.store, q.artifacts, q.resolver, job) }()
	return nil
}
func (q *recordingPageQueue) Dequeue() (extensions.RunJob, error) {
	return extensions.RunJob{}, extensions.ErrQueueClosed
}
func (q *recordingPageQueue) Ack(string) error         { return nil }
func (q *recordingPageQueue) Fail(string, error) error { return nil }
func (q *recordingPageQueue) Len() int                 { return 0 }
func (q *recordingPageQueue) Close() error             { return nil }

func (q *recordingPageQueue) wire() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.sent...)
}

// credentialedPagesServer serves two pages of rows, and refuses any
// request without the connection's Basic Auth password and API key header.
func credentialedPagesServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "loader" || pass != pagePassword || r.Header.Get("X-Api-Key") != pageAPIKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		switch offset {
		case 0:
			_, _ = w.Write([]byte(`{"results":[{"id":1},{"id":2}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"results":[{"id":3}]}`))
		default:
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runCredentialedPages(t *testing.T, workerHasResolver bool, connWorkspace, pipelineWorkspace string) (*models.Run, []string, string) {
	t.Helper()
	srv := credentialedPagesServer(t)
	eng, s := newResumeTestEngine(t)

	key := testKey(3)
	password, err := key.Encrypt(pagePassword)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := key.Encrypt(`{"headers":{"X-Api-Key":"` + pageAPIKey + `"}}`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.CreateConnection(&models.Connection{
		ID: "api", ConnID: "partner-api", Type: models.ConnTypeHTTP, Host: "unused.invalid", Login: "loader",
		Password: password, PasswordRef: "encrypted://" + password,
		Extra: extra, ExtraRef: "encrypted://" + extra,
		WorkspaceID: connWorkspace, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	enc := secrets.NewEncryptedResolver(key)
	resolver := NewConnectionResolver(s, secrets.NewChain(enc, enc))
	eng.ConnResolver = resolver

	outPath := filepath.Join(t.TempDir(), "out.csv")
	pipeline := &models.Pipeline{
		ID: "remote-pages-credentials", Name: "Remote pages with a connection", Enabled: true,
		WorkspaceID: pipelineWorkspace, CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceAPI, Name: "Source", Config: map[string]interface{}{
				// An absolute URL is kept by connection resolution, which
				// still adds the connection's Basic Auth and headers.
				"url": srv.URL, "conn_id": "partner-api", "records": "results", "max_retries": float64(0),
				"pagination": map[string]interface{}{"strategy": "offset", "page_size": float64(2)},
				"execution":  map[string]interface{}{"max_concurrency": float64(2), "page_max_retries": float64(0)},
			}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": outPath, "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	q := &recordingPageQueue{store: s, artifacts: eng.ArtifactStore}
	if workerHasResolver {
		q.resolver = resolver
	}
	eng.InstanceJobQueue = q
	// A failed run is also returned with an error; the callers judge it.
	run, _ := eng.RunPipeline(pipeline.ID)
	if run == nil {
		t.Fatal("RunPipeline returned no run")
	}
	return run, q.wire(), outPath
}

func TestRemotePagesCarryTheConnectionByReference(t *testing.T) {
	run, wire, outPath := runCredentialedPages(t, true, "", "")
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}
	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1", "2", "3"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output %q lacks id %s", out, want)
		}
	}

	if len(wire) < 2 {
		t.Fatalf("only %d page work orders crossed the queue; the test did not exercise remote pages", len(wire))
	}
	for i, w := range wire {
		for _, secret := range []string{pagePassword, pageAPIKey, "auth_password"} {
			if strings.Contains(w, secret) {
				t.Errorf("work order %d carries %q:\n%s", i, secret, w)
			}
		}
		if !strings.Contains(w, `"conn_id":"partner-api"`) {
			t.Errorf("work order %d does not name its connection:\n%s", i, w)
		}
	}
}

// A worker that cannot resolve connections fails the page, saying so,
// instead of sending the request without the credentials.
func TestARemotePageFailsOnAWorkerThatCannotResolveItsConnection(t *testing.T) {
	run, wire, _ := runCredentialedPages(t, false, "", "")
	if len(wire) == 0 {
		t.Fatal("no page work order crossed the queue")
	}
	if run.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", run.Status)
	}
	if !strings.Contains(run.Error, `connection "partner-api"`) || !strings.Contains(run.Error, "no connection resolver") {
		t.Errorf("run error %q does not say the worker could not resolve the connection", run.Error)
	}
}

// The worker resolves in the pipeline's workspace, which the page carries.
// Without it, a worker would resolve another workspace's connection and
// send its credentials, which the dispatcher itself refuses to do.
func TestARemotePageCannotUseAnotherWorkspacesConnection(t *testing.T) {
	run, wire, _ := runCredentialedPages(t, true, "ws-b", "ws-a")
	if len(wire) == 0 {
		t.Fatal("no page work order crossed the queue")
	}
	if !strings.Contains(wire[0], `"workspace_id":"ws-a"`) {
		t.Errorf("the page does not carry its pipeline's workspace:\n%s", wire[0])
	}
	if run.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s: a page resolved another workspace's connection", run.Status)
	}
	if !strings.Contains(run.Error, "not found in this pipeline's workspace") {
		t.Errorf("run error %q does not name the refusal", run.Error)
	}
}

// A page whose connection does not resolve at all fails with the reason,
// rather than going out with no base URL and no credentials.
func TestARemotePageWithAMissingConnectionFails(t *testing.T) {
	_, s := newResumeTestEngine(t)
	cr := NewConnectionResolver(s, nil)
	_, err := ExecuteInstanceWorkOrderResolving(context.Background(), &extensions.InstanceWorkOrder{
		NodeType: string(models.NodeTypeSourceAPI),
		Config:   map[string]interface{}{"conn_id": "no-such-connection", "url": "/v1/items"},
	}, cr)
	if err == nil || !strings.Contains(err.Error(), "no-such-connection") {
		t.Fatalf("err = %v, want the missing connection named", err)
	}
}
