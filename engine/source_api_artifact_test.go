package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/fetchers"
)

// A source_api node with response="artifact" running inside a real pipeline
// stores its response and returns a reference to it. This is the wiring the
// unit tests cannot prove: that the engine actually hands the fetcher a sink
// scoped to the run.
func TestSourceAPI_ArtifactResponse_StoredByReference(t *testing.T) {
	body := strings.Repeat("some opaque payload; not tabular at all. ", 200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	eng, s := newResumeTestEngine(t)

	pipeline := &models.Pipeline{
		ID: "p-artifact-response", Name: "Artifact Response", Enabled: true,
		Nodes: []models.Node{{
			ID: "source", Type: models.NodeTypeSourceAPI, Name: "Fetch Artifact",
			Config: map[string]interface{}{
				"url":      server.URL,
				"response": "artifact",
			},
		}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	// The node's output is the reference, and the artifact store the engine
	// wired must be able to resolve it back to the exact response body.
	store, ok := eng.ArtifactStore.(BlobStoreProvider)
	if !ok {
		t.Fatal("the engine's artifact store does not expose a blob store, so nothing was wired")
	}

	ds, err := eng.ArtifactStore.ReadArtifact(run.ID, "source")
	if err != nil {
		t.Fatalf("read node output: %v", err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(ds.Rows))
	}
	row := ds.Rows[0]
	if _, inlined := row["value"]; inlined {
		t.Error("the response body is still inlined — the artifact sink was not used")
	}

	uri, _ := row[fetchers.ArtifactColURI].(string)
	sum, _ := row[fetchers.ArtifactColChecksum].(string)
	if uri == "" || sum == "" {
		t.Fatalf("node output does not carry a reference: %+v", row)
	}

	rc, err := store.Blobs().Open(context.Background(), &artifact.ArtifactRef{URI: uri, Checksum: sum})
	if err != nil {
		t.Fatalf("the reference stored as node output does not resolve: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != body {
		t.Errorf("resolved artifact is %d bytes, want the %d-byte response", len(got), len(body))
	}
}

// Purging a run must reclaim artifacts a node fetched, not only the outputs
// the engine wrote for resume — they share the run's namespace precisely so
// that one deletion covers both.
func TestSourceAPI_ArtifactResponse_PurgedWithTheRun(t *testing.T) {
	body := "payload to be purged"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	eng, s := newResumeTestEngine(t)
	pipeline := &models.Pipeline{
		ID: "p-artifact-purge", Name: "Artifact Purge", Enabled: true,
		Nodes: []models.Node{{
			ID: "source", Type: models.NodeTypeSourceAPI, Name: "Fetch Artifact",
			Config: map[string]interface{}{"url": server.URL, "response": "artifact"},
		}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run: %v status=%v", err, run.Status)
	}

	ds, err := eng.ArtifactStore.ReadArtifact(run.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	ref := &artifact.ArtifactRef{
		URI:      ds.Rows[0][fetchers.ArtifactColURI].(string),
		Checksum: ds.Rows[0][fetchers.ArtifactColChecksum].(string),
	}
	blobs := eng.ArtifactStore.(BlobStoreProvider).Blobs()
	if rc, err := blobs.Open(context.Background(), ref); err != nil {
		t.Fatalf("artifact should exist before the purge: %v", err)
	} else {
		rc.Close()
	}

	if err := eng.ArtifactStore.DeleteRunArtifacts(run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Open(context.Background(), ref); err == nil {
		t.Error("the fetched artifact survived DeleteRunArtifacts")
	}
}
