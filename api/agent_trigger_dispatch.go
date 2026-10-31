package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/mcpserver"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
)

// The trigger dispatcher (ADR-047 sections 3 to 5). On the scheduler's
// leader, every tick:
//
//  1. match: read the run event log from the cursor, turn each event a
//     trigger wants into a delivery, and move the cursor;
//  2. send: post every due delivery, signed, and record the attempt.
//
// Delivery is at least once. A leader that stops between recording a
// delivery and moving the cursor re-reads those events, and the unique
// key on (trigger, run, event) makes the second reading record nothing.

const (
	dispatchInterval = 2 * time.Second
	// recentMargin is re-read on every pass, below the cursor too: on
	// Postgres an event id is drawn before its transaction commits, so a
	// slow transaction can commit an id lower than one already read.
	recentMargin = 2 * time.Minute
	matchBatch   = 500
	sendBatch    = 20
	sendTimeout  = 10 * time.Second
	// retryWindow is how long a delivery is retried before it is given up.
	retryWindow      = 24 * time.Hour
	retryBase        = 30 * time.Second
	retryCap         = time.Hour
	deliveryRetained = 30 * 24 * time.Hour
	responseExcerpt  = 512
)

// The run events that become trigger events.
var triggerSourceEvents = []models.RunEventType{models.RunEventTerminal, models.RunEventRecoveryFailed, models.RunEventCreated}

// triggerEventOf is the trigger event a run event is, or "".
//
//   - run.failed: a run that ended failed (quality checks and contract
//     gates that block end a run this way), or that recovery could not
//     bring back;
//   - run.blocked: a run that did not start because its dependencies were
//     not satisfied, which is recorded only as its creation.
func triggerEventOf(ev models.RunEvent) string {
	if ev.NodeID != "" {
		return ""
	}
	switch {
	case ev.EventType == models.RunEventTerminal && ev.Payload.Status == models.RunStatusFailed,
		ev.EventType == models.RunEventRecoveryFailed:
		return TriggerEventRunFailed
	case ev.EventType == models.RunEventCreated && ev.Payload.Status == models.RunStatusBlocked:
		return TriggerEventRunBlocked
	}
	return ""
}

// TriggerEvent is the body of a delivery (ADR-047 section 5). It carries
// identifiers and redacted context, never a credential: the agent calls
// back over MCP with the token it already holds.
type TriggerEvent struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	OccurredAt  time.Time       `json:"occurred_at"`
	WorkspaceID string          `json:"workspace_id"`
	Agent       string          `json:"agent"`
	Pipeline    TriggerPipeline `json:"pipeline"`
	Run         TriggerRun      `json:"run"`
	Next        TriggerNext     `json:"next"`
}

// TriggerPipeline names the pipeline an event is about.
type TriggerPipeline struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TriggerRun is what the event says about the run.
type TriggerRun struct {
	ID          string              `json:"id"`
	Status      string              `json:"status"`
	FailedNodes []TriggerFailedStep `json:"failed_nodes,omitempty"`
	Error       string              `json:"error,omitempty"`
}

// TriggerFailedStep is a step that failed, with its type: a quality_check
// or contract_gate here means data was stopped, not that code broke.
type TriggerFailedStep struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// TriggerNext is where the agent goes from here.
type TriggerNext struct {
	MCP           string `json:"mcp,omitempty"`
	SuggestedTool string `json:"suggested_tool"`
	RunURL        string `json:"run_url,omitempty"`
}

// leaderGate is the scheduler, or anything that says whether this process
// should do leader-only work.
type leaderGate interface{ IsLeader() bool }

type triggerDispatcher struct {
	s         store.Store
	scan      store.RunEventScanner
	users     *UserStore
	pc        *PermissionChecker
	ts        *TriggerStore
	leader    leaderGate
	publicURL string
	client    *http.Client
	now       func() time.Time
}

// StartAgentTriggers runs the trigger dispatcher until the returned stop
// is called. It does work only while leader says this process leads, so
// every replica can start it. publicURL is where agents reach this server
// (BROKOLI_PUBLIC_URL); events carry links only when it is set.
func StartAgentTriggers(s store.Store, users *UserStore, cc *crypto.Config, leader leaderGate, publicURL string) (stop func(), err error) {
	scan, ok := s.(store.RunEventScanner)
	if !ok {
		log.Printf("agent triggers: this store cannot read the run event log; no deliveries")
		return func() {}, nil
	}
	ts, err := NewTriggerStore(users.db, cc)
	if err != nil {
		return nil, err
	}
	d := &triggerDispatcher{
		s: s, scan: scan, users: users, pc: NewPermissionChecker(s), ts: ts, leader: leader,
		publicURL: strings.TrimRight(publicURL, "/"), client: netguard.Outbound().Client(sendTimeout), now: time.Now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.loop(ctx, dispatchInterval)
	}()
	return func() { cancel(); wg.Wait() }, nil
}

func (d *triggerDispatcher) loop(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !d.leader.IsLeader() || !AgentIdentitiesEnabled {
			continue
		}
		if err := d.match(); err != nil {
			log.Printf("agent triggers: match: %v", err)
		}
		d.send(ctx)
		if now := d.now(); now.Sub(lastPrune) > time.Hour {
			if err := d.ts.PruneDeliveries(now.Add(-deliveryRetained)); err != nil {
				log.Printf("agent triggers: prune: %v", err)
			}
			lastPrune = now
		}
	}
}

// match turns the run events since the cursor into deliveries.
func (d *triggerDispatcher) match() error {
	now := d.now()
	cursor, err := d.ts.Cursor()
	if err != nil {
		return err
	}
	events, err := d.scan.ScanRunEvents(cursor, now.Add(-recentMargin), triggerSourceEvents, matchBatch)
	if err != nil || len(events) == 0 {
		return err
	}
	triggers, err := d.ts.List("", "")
	if err != nil {
		return err
	}
	highest := cursor
	for _, ev := range events {
		if ev.ID > highest {
			highest = ev.ID
		}
		event := triggerEventOf(ev)
		if event == "" || len(triggers) == 0 {
			continue
		}
		run, err := d.s.GetRun(ev.RunID)
		if err != nil {
			continue
		}
		pipelineID := ev.Payload.PipelineID
		if pipelineID == "" {
			pipelineID = run.PipelineID
		}
		p, err := d.s.GetPipeline(pipelineID)
		if err != nil {
			continue
		}
		var body []byte
		for i := range triggers {
			t := &triggers[i]
			if !t.Watches(event, p.ID) || !sameWorkspace(p.WorkspaceID, t.WorkspaceID) {
				continue
			}
			del := &TriggerDelivery{ID: "dlv_" + generateID(), TriggerID: t.ID, AgentID: t.AgentID, WorkspaceID: t.WorkspaceID,
				PipelineID: p.ID, RunID: run.ID, Event: event}
			if body, err = d.eventBody(del, t, p, run, ev); err != nil {
				return err
			}
			if _, err := d.ts.Enqueue(del, string(body), time.Duration(t.CooldownSeconds)*time.Second, now); err != nil {
				return err
			}
		}
	}
	return d.ts.SetCursor(highest)
}

// eventBody is the delivery's body, fixed when the event is matched so
// every retry sends the same bytes.
func (d *triggerDispatcher) eventBody(del *TriggerDelivery, t *AgentTrigger, p *models.Pipeline, run *models.Run, ev models.RunEvent) ([]byte, error) {
	agentName := t.AgentID
	if a, err := d.users.GetAgent(t.AgentID); err == nil {
		agentName = a.Name
	}
	e := TriggerEvent{
		Type: del.Event, ID: del.ID, OccurredAt: ev.CreatedAt.UTC(), WorkspaceID: t.WorkspaceID, Agent: agentName,
		Pipeline: TriggerPipeline{ID: p.ID, Name: p.Name},
		Run: TriggerRun{ID: run.ID, Status: string(run.Status),
			Error: mcpserver.Clip(mcpserver.RedactText(run.Error), mcpserver.MaxText)},
		Next: TriggerNext{SuggestedTool: "explain_run"},
	}
	if del.Event == TriggerEventRunBlocked {
		e.Next.SuggestedTool = "get_lineage"
	}
	if e.Run.Status == "" {
		e.Run.Status = string(ev.Payload.Status)
	}
	if nodeRuns, err := d.s.ListNodeRunsByRun(run.ID); err == nil {
		byID := map[string]models.Node{}
		for _, n := range p.Nodes {
			byID[n.ID] = n
		}
		seen := map[string]bool{}
		for _, nr := range nodeRuns {
			if nr.Status != models.RunStatusFailed || seen[nr.NodeID] {
				continue
			}
			seen[nr.NodeID] = true
			n, ok := byID[nr.NodeID]
			name := n.Name
			if !ok || name == "" {
				name = nr.NodeID
			}
			e.Run.FailedNodes = append(e.Run.FailedNodes, TriggerFailedStep{Name: name, Type: string(n.Type)})
		}
	}
	if d.publicURL != "" {
		e.Next.MCP = d.publicURL + "/api/mcp"
		e.Next.RunURL = d.publicURL + "/#/pipelines/" + p.ID + "/runs?run=" + run.ID
	}
	return json.Marshal(e)
}

// send posts every due delivery.
func (d *triggerDispatcher) send(ctx context.Context) {
	due, err := d.ts.Due(d.now(), sendBatch)
	if err != nil {
		log.Printf("agent triggers: due deliveries: %v", err)
		return
	}
	for i := range due {
		if ctx.Err() != nil {
			return
		}
		if err := d.deliver(ctx, &due[i]); err != nil {
			log.Printf("agent triggers: delivery %s: %v", due[i].ID, err)
		}
	}
}

// deliver makes one attempt at del. The trigger, the pipeline and what the
// agent may see are read now, not when the event was matched: a trigger
// that was disabled, or an agent that may no longer see the pipeline, gets
// nothing.
func (d *triggerDispatcher) deliver(ctx context.Context, del *TriggerDelivery) error {
	now := d.now()
	t, err := d.ts.GetByID(del.TriggerID)
	if err != nil {
		return d.ts.Skip(del, "the trigger was removed", now)
	}
	if !t.Enabled {
		return d.ts.Skip(del, "the trigger is disabled", now)
	}
	p, err := d.s.GetPipeline(del.PipelineID)
	if err != nil {
		return d.ts.Skip(del, "the pipeline no longer exists", now)
	}
	if ok, why := d.users.agentMaySeePipeline(d.pc, t.AgentID, t.WorkspaceID, p); !ok {
		return d.ts.Skip(del, "the agent may not see this pipeline now: "+why, now)
	}
	secrets, err := d.ts.SigningSecrets(t.ID, now)
	if err != nil {
		return d.retry(del, DeliveryAttempt{At: now, Error: "cannot sign: " + err.Error()}, now)
	}

	body := []byte(del.payload)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return d.retry(del, DeliveryAttempt{At: now, Error: err.Error()}, now)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("webhook-id", del.ID)
	req.Header.Set("webhook-timestamp", timestamp)
	req.Header.Set("webhook-signature", signWebhook(secrets, del.ID, timestamp, body))

	start := time.Now()
	resp, err := d.client.Do(req)
	attempt := DeliveryAttempt{At: now, DurationMs: time.Since(start).Milliseconds()}
	if err != nil {
		attempt.Error = mcpserver.RedactText(err.Error())
		return d.retry(del, attempt, now)
	}
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, responseExcerpt))
	_ = resp.Body.Close()
	attempt.StatusCode = resp.StatusCode
	attempt.Response = mcpserver.RedactText(string(excerpt))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return d.ts.RecordAttempt(del, attempt, DeliveryDelivered, "", time.Time{})
	}
	attempt.Error = fmt.Sprintf("answered %d", resp.StatusCode)
	return d.retry(del, attempt, now)
}

// retry records a failed attempt and schedules the next one, or gives the
// delivery up once its 24 hour window has passed.
func (d *triggerDispatcher) retry(del *TriggerDelivery, attempt DeliveryAttempt, now time.Time) error {
	if now.Sub(del.windowStart) >= retryWindow {
		return d.ts.RecordAttempt(del, attempt, DeliveryFailed, "gave up after 24 hours of retries: "+attempt.Error, time.Time{})
	}
	return d.ts.RecordAttempt(del, attempt, DeliveryPending, "", now.Add(retryDelay(del.Attempts+1)))
}

// retryDelay is the wait before attempt n+1: exponential from retryBase,
// capped at retryCap, with jitter in the upper half so deliveries that
// failed together do not all retry at the same instant.
func retryDelay(n int) time.Duration {
	delay := retryBase
	for i := 1; i < n && delay < retryCap; i++ {
		delay *= 2
	}
	if delay > retryCap {
		delay = retryCap
	}
	return delay/2 + rand.N(delay/2+1) // #nosec G404 -- retry jitter, not a secret
}

// signWebhook is the Standard Webhooks webhook-signature header: "v1,"
// and the base64 HMAC-SHA256 of "id.timestamp.body", one per secret,
// separated by spaces (both secrets sign during a rotation).
func signWebhook(secrets []string, id, timestamp string, body []byte) string {
	sigs := make([]string, 0, len(secrets))
	for _, s := range secrets {
		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, triggerSecretPrefix))
		if err != nil {
			continue
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + timestamp + "."))
		mac.Write(body)
		sigs = append(sigs, "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	}
	return strings.Join(sigs, " ")
}
