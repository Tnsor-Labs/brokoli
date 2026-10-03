package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/store"
)

func TestRunRedactionSet(t *testing.T) {
	run := "run-redaction-unit"
	t.Cleanup(func() { dropRunRedactions(run) })

	addRunSecret(run, "s3cr3t-pass/word")
	addRunSecret(run, "short") // under the floor: never masked
	// Under the floor too, though its URL-escaped spelling ("a%2Fb%26c") is
	// long enough to mask: the floor is the value's length, not a form's.
	addRunSecret(run, "a/b&c")
	addRunExtraSecrets(run, `{"sslmode":"disable","region":"us-east-1","access_key":"AKIAEXAMPLE1",
		"headers":{"Authorization":"Bearer sk-live-0123456789","Accept":"application/json"},
		"credentials":{"type":"service_account","private_key":"-----BEGIN KEY-----abc"}}`)

	msg := "bare=sk-live-0123456789 dsn=postgres://u:s3cr3t-pass%2Fword@h/db pw=s3cr3t-pass/word auth=Bearer sk-live-0123456789 " +
		"key=AKIAEXAMPLE1 pk=-----BEGIN KEY-----abc mode=disable region=us-east-1 short accept=application/json"
	got := redactRun(run, msg)
	for _, secret := range []string{"s3cr3t-pass/word", "s3cr3t-pass%2Fword", "sk-live-0123456789", "AKIAEXAMPLE1", "BEGIN KEY-----abc"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q survived redaction: %s", secret, got)
		}
	}
	if !strings.Contains(redactRun(run, "q=a%2Fb%26c"), "a%2Fb%26c") {
		t.Error("a value under the floor was masked through its escaped form")
	}
	for _, setting := range []string{"mode=disable", "region=us-east-1", " short ", "application/json"} {
		if !strings.Contains(got, setting) {
			t.Errorf("a setting was redacted, %q missing: %s", setting, got)
		}
	}

	// Other runs, and a run once it ends, are untouched.
	if redactRun("another-run", msg) != msg {
		t.Error("one run's secrets redacted another run's text")
	}
	dropRunRedactions(run)
	if redactRun(run, msg) != msg {
		t.Error("a finished run's secrets are still being redacted")
	}
}

// End to end: a connection's stored, encrypted credential is resolved for
// a run, the target echoes it back in its error, and nothing the run
// records -- node log, node error, run error, events -- carries it.
func TestRunDoesNotRecordResolvedCredentials(t *testing.T) {
	const token = "sk-live-9f8e7d6c5b4a3f2e1d0c"
	const password = "hunter2-correct-horse"
	// The target redirects to an address that refuses connections, with
	// the bearer token in the query string: the fetch error quotes that
	// URL, so the credential reaches the run's error text.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pw, _ := r.BasicAuth()
		got := r.Header.Get("X-Api-Token")
		http.Redirect(w, r, "http://127.0.0.1:1/revoked?token="+got+"&pw="+pw, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))

	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "redact.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := testKey(3)
	extra, _ := json.Marshal(map[string]interface{}{"headers": map[string]string{"X-Api-Token": token}})
	sealed, err := key.Encrypt(string(extra))
	if err != nil {
		t.Fatal(err)
	}
	// The node names the full URL; the connection supplies its headers.
	sealedPW, err := key.Encrypt(password)
	if err != nil {
		t.Fatal(err)
	}
	conn := &models.Connection{ID: "c-redact", ConnID: "echoing-api", Type: models.ConnTypeHTTP,
		Host: "api.example.com", Login: "svc", Password: sealedPW, PasswordRef: "encrypted://" + sealedPW, Extra: sealed, ExtraRef: "encrypted://" + sealed,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := st.CreateConnection(conn); err != nil {
		t.Fatal(err)
	}
	pipe := &models.Pipeline{ID: "redact-p", Name: "redact-p", Enabled: true,
		Nodes: []models.Node{{ID: "api", Type: models.NodeTypeSourceAPI, Name: "API",
			Config: map[string]interface{}{"conn_id": "echoing-api", "url": srv.URL + "/items", "method": "GET"}}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := st.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	enc := secrets.NewEncryptedResolver(key)
	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, secrets.NewChain(enc, enc))
	run, _ := eng.RunPipeline(pipe.ID)
	if run == nil {
		t.Fatal("no run")
	}

	var recorded []string
	got, _ := st.GetRun(run.ID)
	recorded = append(recorded, "run.error: "+got.Error)
	logs, _ := st.GetLogs(run.ID)
	for _, l := range logs {
		recorded = append(recorded, "log: "+l.Message)
	}
	if nrs, err := st.ListNodeRunsByRun(run.ID); err == nil {
		for _, nr := range nrs {
			recorded = append(recorded, "node error: "+nr.Error)
		}
	}
	all := strings.Join(recorded, "\n")
	if !strings.Contains(all, "revoked?token=") {
		t.Fatalf("the target's error never reached the run, so this proves nothing (status %s):\n%s", run.Status, all)
	}
	for _, secret := range []string{token, password} {
		if strings.Contains(all, secret) {
			t.Fatalf("a resolved credential (%s...) was recorded:\n%s", secret[:4], all)
		}
	}
	if !strings.Contains(all, recordedSecretMask) {
		t.Fatalf("no redaction marker where the credential was:\n%s", all)
	}
	if _, ok := runRedactions.Load(run.ID); ok {
		t.Error("the run's redaction set outlived the run")
	}
}

// A remote source_api page resolves its connection on the worker, outside
// any run's runner, and its error is settled from there: it is masked by
// the page's own set, which does not outlive the call.
func TestRemotePageErrorsAreRedacted(t *testing.T) {
	const apiKey = "pk-live-5c4b3a29181716"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/denied?key="+r.Header.Get("X-Api-Key"), http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))

	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "page.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := testKey(4)
	sealed, err := key.Encrypt(`{"headers":{"X-Api-Key":"` + apiKey + `"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateConnection(&models.Connection{ID: "c-page", ConnID: "page-api", Type: models.ConnTypeHTTP,
		Host: "unused.invalid", Extra: sealed, ExtraRef: "encrypted://" + sealed,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	enc := secrets.NewEncryptedResolver(key)
	cr := NewConnectionResolver(st, secrets.NewChain(enc, enc))

	before := 0
	runRedactions.Range(func(_, _ interface{}) bool { before++; return true })
	_, err = executeSourceAPIPageWorkOrder(context.Background(), &extensions.InstanceWorkOrder{
		NodeType: string(models.NodeTypeSourceAPI), SourceType: "rest", PageURL: srv.URL,
		Config: map[string]interface{}{"conn_id": "page-api", "url": srv.URL},
	}, cr)
	if err == nil || !strings.Contains(err.Error(), "denied?key=") {
		t.Fatalf("the leak path was not exercised: %v", err)
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("the page error carries the API key: %v", err)
	}
	after := 0
	runRedactions.Range(func(_, _ interface{}) bool { after++; return true })
	if after != before {
		t.Errorf("the page's redaction set outlived the call (%d sets before, %d after)", before, after)
	}
}
