package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
)

// verifyStandardWebhook checks a delivery the way a receiver following
// the Standard Webhooks specification would, written here from the
// specification rather than from signWebhook.
func verifyStandardWebhook(secret string, h http.Header, body []byte, now time.Time) bool {
	ts, err := strconv.ParseInt(h.Get("webhook-timestamp"), 10, 64)
	if err != nil || now.Sub(time.Unix(ts, 0)).Abs() > 5*time.Minute {
		return false
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(h.Get("webhook-id") + "." + h.Get("webhook-timestamp") + "." + string(body)))
	want := mac.Sum(nil)
	for _, part := range strings.Fields(h.Get("webhook-signature")) {
		version, sig, ok := strings.Cut(part, ",")
		got, err := base64.StdEncoding.DecodeString(sig)
		if ok && version == "v1" && err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

type receiver struct {
	mu      sync.Mutex
	srv     *httptest.Server
	answers []int // status per request; the last one repeats
	got     []received
}

type received struct {
	header http.Header
	body   []byte
}

func newReceiver(t *testing.T, answers ...int) *receiver {
	rc := &receiver{answers: answers}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.got = append(rc.got, received{header: r.Header.Clone(), body: body})
		status := http.StatusOK
		if len(rc.answers) > 0 {
			status = rc.answers[min(len(rc.got)-1, len(rc.answers)-1)]
		}
		rc.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *receiver) requests() []received {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]received(nil), rc.got...)
}

// leading is a leader gate the test flips while the dispatcher reads it.
type leading struct{ v atomic.Bool }

func (l *leading) IsLeader() bool { return l.v.Load() }

// dispatchBed is a trigger bed with a dispatcher on a clock the test moves.
type dispatchBed struct {
	*agentBed
	d       *triggerDispatcher
	clock   time.Time
	leader  leading
	agentID string
}

func newDispatchBed(t *testing.T) *dispatchBed {
	t.Helper()
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	b := &dispatchBed{agentBed: newTriggerBed(t), clock: time.Now().UTC()}
	b.leader.v.Store(true)
	b.agentID, _ = b.agent("triage-bot", AgentRole, "eve", "eve", true)
	b.d = &triggerDispatcher{s: b.s, scan: b.s.(store.RunEventScanner), users: b.users, pc: NewPermissionChecker(b.s),
		ts: b.triggerStore(), leader: &b.leader, publicURL: "https://brokoli.example.com",
		client: netguard.Outbound().Client(5 * time.Second), now: func() time.Time { return b.clock }}
	return b
}

// trigger makes a trigger through the API and returns its id and secret.
func (b *dispatchBed) trigger(body string) (string, string) {
	b.t.Helper()
	code, res, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers", b.tokens["eve"], body)
	if code != http.StatusCreated {
		b.t.Fatalf("create trigger: %d %s", code, raw)
	}
	return res["trigger"].(map[string]any)["id"].(string), res["secret"].(string)
}

func (b *dispatchBed) pipelineWithNodes(id, workspace string) {
	b.t.Helper()
	now := time.Now().UTC()
	p := &models.Pipeline{ID: id, Name: id + " pipeline", Enabled: true, WorkspaceID: workspace, CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "fetch", Name: "Fetch orders", Type: models.NodeTypeSourceAPI},
			{ID: "gate", Name: "Orders contract", Type: models.NodeTypeQualityCheck},
		}}
	if err := b.s.CreatePipeline(p); err != nil {
		b.t.Fatal(err)
	}
}

// runEvent records a run of pipelineID and the event it ended with.
func (b *dispatchBed) runEvent(pipelineID string, typ models.RunEventType, status models.RunStatus, runErr string) string {
	b.t.Helper()
	now := time.Now().UTC()
	rid := "run-" + generateID()[:10]
	if err := b.s.CreateRun(&models.Run{ID: rid, PipelineID: pipelineID, Status: status, StartedAt: &now, Error: runErr}); err != nil {
		b.t.Fatal(err)
	}
	if status == models.RunStatusFailed {
		if err := b.s.CreateNodeRun(&models.NodeRun{ID: generateID(), RunID: rid, NodeID: "fetch", Status: models.RunStatusFailed, StartedAt: &now}); err != nil {
			b.t.Fatal(err)
		}
	}
	if err := b.s.AppendEvent(&models.RunEvent{RunID: rid, EventType: typ, Payload: models.RunEventPayload{Status: status}}); err != nil {
		b.t.Fatal(err)
	}
	return rid
}

func (b *dispatchBed) pass() {
	b.t.Helper()
	if err := b.d.match(); err != nil {
		b.t.Fatalf("match: %v", err)
	}
	b.d.send(context.Background())
}

func (b *dispatchBed) deliveries(triggerID string) []TriggerDelivery {
	b.t.Helper()
	out, err := b.d.ts.Deliveries(triggerID, 100)
	if err != nil {
		b.t.Fatal(err)
	}
	return out
}

func TestAFailedRunIsDeliveredSigned(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, secret := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `/hook"}`)
	runID := b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed,
		"GET https://shop.example.com/v2/orders: 404; db postgres://app:hunter2@db/x; Authorization: Bearer abcdefghijkl1234")

	b.pass()
	got := rc.requests()
	if len(got) != 1 {
		t.Fatalf("received %d requests, want 1", len(got))
	}
	r := got[0]
	if !verifyStandardWebhook(secret, r.header, r.body, time.Now()) {
		t.Fatalf("signature does not verify: %v", r.header)
	}
	var ev TriggerEvent
	if err := json.Unmarshal(r.body, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != TriggerEventRunFailed || ev.ID != r.header.Get("webhook-id") || ev.Agent != "triage-bot" ||
		ev.Pipeline.ID != "orders" || ev.Pipeline.Name != "orders pipeline" || ev.Run.ID != runID || ev.Run.Status != "failed" {
		t.Errorf("event = %+v", ev)
	}
	if len(ev.Run.FailedNodes) != 1 || ev.Run.FailedNodes[0] != (TriggerFailedStep{Name: "Fetch orders", Type: "source_api"}) {
		t.Errorf("failed nodes = %+v", ev.Run.FailedNodes)
	}
	if strings.Contains(string(r.body), "hunter2") || strings.Contains(string(r.body), "abcdefghijkl1234") || !strings.Contains(ev.Run.Error, "404") {
		t.Errorf("error not redacted, or lost: %q", ev.Run.Error)
	}
	if ev.Next.SuggestedTool != "explain_run" || ev.Next.MCP != "https://brokoli.example.com/api/mcp" ||
		ev.Next.RunURL != "https://brokoli.example.com/#/pipelines/orders/runs?run="+runID {
		t.Errorf("next = %+v", ev.Next)
	}
	if strings.Contains(string(r.body), secret[6:]) || strings.Contains(string(r.body), "bka_") {
		t.Error("the event carries a credential")
	}
	ds := b.deliveries(id)
	if len(ds) != 1 || ds[0].Status != DeliveryDelivered || ds[0].Attempts != 1 || len(ds[0].History) != 1 || ds[0].History[0].StatusCode != 200 {
		t.Fatalf("delivery = %+v", ds)
	}
}

func TestEventsAreMatchedToWhatTriggersWant(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	b.pipelineWithNodes("billing", models.DefaultWorkspaceID)
	b.pipelineWithNodes("elsewhere", "ws-other")
	failedID, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)
	blockedID, _ := b.trigger(`{"events":["run.blocked"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)
	billingID, _ := b.trigger(`{"events":["run.failed","run.blocked"],"url":"` + rc.srv.URL + `","pipeline_id":"billing","cooldown_seconds":0}`)

	b.runEvent("orders", models.RunEventTerminal, models.RunStatusSuccess, "")           // nothing
	failed := b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x") // failed
	recovered := b.runEvent("orders", models.RunEventRecoveryFailed, models.RunStatusFailed, "")
	blocked := b.runEvent("orders", models.RunEventCreated, models.RunStatusBlocked, "")
	b.runEvent("orders", models.RunEventCreated, models.RunStatusRunning, "")              // nothing
	b.runEvent("elsewhere", models.RunEventTerminal, models.RunStatusFailed, "x")          // another workspace
	billing := b.runEvent("billing", models.RunEventTerminal, models.RunStatusFailed, "x") // both failed and billing
	b.pass()

	runs := func(id string) map[string]string {
		out := map[string]string{}
		for _, d := range b.deliveries(id) {
			out[d.RunID] = d.Event
		}
		return out
	}
	if got := runs(failedID); len(got) != 3 || got[failed] != "run.failed" || got[recovered] != "run.failed" || got[billing] != "run.failed" {
		t.Errorf("the run.failed trigger got %v", got)
	}
	if got := runs(blockedID); len(got) != 1 || got[blocked] != "run.blocked" {
		t.Errorf("the run.blocked trigger got %v", got)
	}
	if got := runs(billingID); len(got) != 1 || got[billing] != "run.failed" {
		t.Errorf("the billing-only trigger got %v", got)
	}
}

func TestReadingTheLogAgainRecordsNothingTwice(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.pass()
	if _, err := b.users.db.Exec(`DELETE FROM agent_trigger_cursor`); err != nil {
		t.Fatal(err)
	}
	b.pass()
	b.pass()
	if ds := b.deliveries(id); len(ds) != 1 || len(rc.requests()) != 1 {
		t.Fatalf("after re-reading the whole log: %d deliveries, %d requests; want 1 and 1", len(ds), len(rc.requests()))
	}
}

// The scan re-reads recent events, which a trigger created after them must
// not be sent: it was made to hear about what fails from then on.
func TestATriggerIsNotSentWhatFailedBeforeIt(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	before := b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	id, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)
	created := time.Now().UTC().Add(time.Minute) // as if made a minute after that failure
	if _, err := b.users.db.Exec(`UPDATE agent_triggers SET created_at = ? WHERE id = ?`, created.Format(triggerTimeFormat), id); err != nil {
		t.Fatal(err)
	}
	b.pass()
	if ds := b.deliveries(id); len(ds) != 0 {
		t.Fatalf("a failure from before the trigger was delivered: %+v (run %s)", ds, before)
	}
}

func TestACooldownReportsAFailingPipelineOnce(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `","cooldown_seconds":1800}`)
	for i := 0; i < 3; i++ {
		b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	}
	b.pass()
	if ds := b.deliveries(id); len(ds) != 1 {
		t.Fatalf("three failures within the cooldown: %d deliveries, want 1", len(ds))
	}
	b.clock = b.clock.Add(31 * time.Minute)
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.pass()
	if ds := b.deliveries(id); len(ds) != 2 {
		t.Fatalf("a failure after the cooldown: %d deliveries, want 2", len(ds))
	}
}

func TestAFailedDeliveryIsRetriedThenDelivered(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t, http.StatusInternalServerError, http.StatusOK)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, secret := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `"}`)
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")

	b.pass()
	d := b.deliveries(id)[0]
	if d.Status != DeliveryPending || d.Attempts != 1 || d.NextAttempt == nil || d.LastStatus != 500 {
		t.Fatalf("after a 500: %+v", d)
	}
	if wait := d.NextAttempt.Sub(b.clock); wait < retryBase/2 || wait > retryBase {
		t.Errorf("first retry in %s, want between %s and %s", wait, retryBase/2, retryBase)
	}
	b.pass() // not due yet
	if n := len(rc.requests()); n != 1 {
		t.Fatalf("retried before it was due: %d requests", n)
	}
	b.clock = d.NextAttempt.Add(time.Second)
	b.pass()
	got := rc.requests()
	d = b.deliveries(id)[0]
	if len(got) != 2 || d.Status != DeliveryDelivered || d.Attempts != 2 || len(d.History) != 2 {
		t.Fatalf("after the retry: %d requests, %+v", len(got), d)
	}
	if got[0].header.Get("webhook-id") != got[1].header.Get("webhook-id") || string(got[0].body) != string(got[1].body) {
		t.Error("a retry is not the same delivery: id or body changed")
	}
	if !verifyStandardWebhook(secret, got[1].header, got[1].body, b.clock) {
		t.Error("the retry's signature does not verify")
	}
}

func TestADeliveryIsGivenUpAfter24Hours(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t, http.StatusServiceUnavailable)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `"}`)
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.pass()
	b.clock = b.clock.Add(retryWindow + time.Minute)
	b.pass()
	d := b.deliveries(id)[0]
	if d.Status != DeliveryFailed || !strings.Contains(d.Reason, "gave up after 24 hours") || d.FinishedAt == nil {
		t.Fatalf("after 24 hours of 503s: %+v", d)
	}
	b.clock = b.clock.Add(time.Hour)
	b.pass()
	if n := len(rc.requests()); n != 2 {
		t.Errorf("a given-up delivery was sent again: %d requests", n)
	}
	// A person can send it again, with a fresh window.
	rc.mu.Lock()
	rc.answers = []int{http.StatusOK}
	rc.mu.Unlock()
	code, _, raw := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers/"+id+"/deliveries/"+d.ID+"/redeliver", b.tokens["eve"], "")
	if code != http.StatusAccepted {
		t.Fatalf("redeliver: %d %s", code, raw)
	}
	b.clock = time.Now().UTC().Add(time.Second)
	b.pass()
	if d := b.deliveries(id)[0]; d.Status != DeliveryDelivered || d.ID != rc.requests()[2].header.Get("webhook-id") {
		t.Fatalf("after redelivery: %+v", d)
	}
}

func TestNothingIsSentToAnAgentThatMayNoLongerSeeIt(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)

	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	if err := b.d.match(); err != nil {
		t.Fatal(err)
	}
	if err := b.users.DisableAgent(b.agentID); err != nil {
		t.Fatal(err)
	}
	b.d.send(context.Background())
	if n := len(rc.requests()); n != 0 {
		t.Fatalf("a disabled agent was sent %d events", n)
	}
	if d := b.deliveries(id)[0]; d.Status != DeliverySkipped || !strings.Contains(d.Reason, "disabled") {
		t.Fatalf("delivery = %+v", d)
	}
}

func TestADisabledTriggerSendsNothing(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `"}`)
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	if err := b.d.match(); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := b.do(http.MethodPatch, "/api/agents/"+b.agentID+"/triggers/"+id, b.tokens["eve"], `{"enabled":false}`); code != http.StatusOK {
		t.Fatalf("disable: %d %s", code, raw)
	}
	b.d.send(context.Background())
	if d := b.deliveries(id)[0]; d.Status != DeliverySkipped || len(rc.requests()) != 0 {
		t.Fatalf("delivery = %+v, %d requests", d, len(rc.requests()))
	}
}

func TestBothSecretsSignDuringARotation(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, old := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `"}`)
	_, rot, _ := b.do(http.MethodPost, "/api/agents/"+b.agentID+"/triggers/"+id+"/secret", b.tokens["eve"], "")
	fresh := rot["secret"].(string)
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.pass()
	r := rc.requests()[0]
	if n := len(strings.Fields(r.header.Get("webhook-signature"))); n != 2 {
		t.Fatalf("%d signatures during a rotation, want 2", n)
	}
	if !verifyStandardWebhook(old, r.header, r.body, time.Now()) || !verifyStandardWebhook(fresh, r.header, r.body, time.Now()) {
		t.Error("a receiver with either secret cannot verify the delivery")
	}
}

func TestRetryDelayGrowsAndIsCapped(t *testing.T) {
	for n, bounds := range map[int][2]time.Duration{1: {15 * time.Second, 30 * time.Second}, 2: {30 * time.Second, time.Minute},
		3: {time.Minute, 2 * time.Minute}, 40: {30 * time.Minute, time.Hour}} {
		for i := 0; i < 50; i++ {
			if d := retryDelay(n); d < bounds[0] || d > bounds[1] {
				t.Fatalf("retryDelay(%d) = %s, want within %s..%s", n, d, bounds[0], bounds[1])
			}
		}
	}
}

func TestTheDispatcherWorksOnlyWhileItLeads(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	id, _ := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `"}`)
	b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "x")
	b.d.now = time.Now

	b.leader.v.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.d.loop(ctx, 10*time.Millisecond); close(done) }()
	time.Sleep(150 * time.Millisecond)
	if ds := b.deliveries(id); len(ds) != 0 {
		t.Fatalf("a follower recorded %d deliveries", len(ds))
	}
	b.leader.v.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for len(rc.requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if len(rc.requests()) != 1 {
		t.Fatalf("the leader sent %d deliveries, want 1", len(rc.requests()))
	}
}

// A run that really fails in the engine reaches the agent: the run event
// log is written where the trigger expects it.
func TestARealRunFailureReachesTheAgent(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	now := time.Now().UTC()
	p := &models.Pipeline{ID: "broken", Name: "broken", Enabled: true, CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{{ID: "src", Name: "Missing file", Type: models.NodeTypeSourceFile,
			Config: map[string]interface{}{"path": t.TempDir() + "/does-not-exist.csv", "format": "csv"}}}}
	if err := b.s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	id, secret := b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `"}`)

	// RunPipeline runs synchronously and answers the failure as its error.
	if _, err := engine.NewEngine(b.s).RunPipeline("broken"); err == nil {
		t.Fatal("the run did not fail")
	}
	runs, err := b.s.ListRunsByPipeline("broken", 5)
	if err != nil || len(runs) != 1 || runs[0].Status != models.RunStatusFailed {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	run := runs[0]
	b.clock = time.Now().UTC()
	b.pass()
	got := rc.requests()
	if len(got) != 1 || !verifyStandardWebhook(secret, got[0].header, got[0].body, time.Now()) {
		t.Fatalf("received %d deliveries (want 1, signed)", len(got))
	}
	var ev TriggerEvent
	_ = json.Unmarshal(got[0].body, &ev)
	if ev.Run.ID != run.ID || len(ev.Run.FailedNodes) != 1 || ev.Run.FailedNodes[0].Name != "Missing file" || ev.Run.Error == "" {
		t.Errorf("event = %+v", ev)
	}
	if d := b.deliveries(id); len(d) != 1 || d[0].Status != DeliveryDelivered {
		t.Errorf("delivery = %+v", d)
	}
}
