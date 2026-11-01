package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/mcpserver"
)

// Events a distribution raises (ADR-047 section 2). A type registered with
// RegisterTriggerEvent is raised here, and from then on goes the way a run
// event does: to every trigger in the pipeline's workspace that wants it,
// held back by the trigger's cooldown, checked against what the agent may
// see when it is delivered, and kept in the delivery log.

// RaisedEvent is one occurrence of an added event type.
type RaisedEvent struct {
	// Type is a type registered with RegisterTriggerEvent.
	Type string
	// PipelineID is the pipeline the event is about.
	PipelineID string
	// RunID is the run it is about, if any; the run page then lists the
	// agents told.
	RunID string
	// Key names the occurrence when there is no run, such as
	// "2026-11-01" for a deadline missed that day. Each trigger is sent an
	// occurrence of a pipeline once, however often it is raised.
	Key string
	// OccurredAt is when it happened; now when zero.
	OccurredAt time.Time
	// Summary is a sentence for the agent. It is redacted and clipped as
	// a run's error is; never put a credential in it.
	Summary string
}

// ErrTriggersNotRunning is an event raised where the trigger dispatcher
// was not started (no encryption key, or a store without a run event log).
var ErrTriggersNotRunning = errors.New("agent triggers are not running on this server")

var (
	raiserMu sync.RWMutex
	raiser   *triggerDispatcher
)

func setRaiser(d *triggerDispatcher) {
	raiserMu.Lock()
	raiser = d
	raiserMu.Unlock()
}

// RaiseTriggerEvent records ev for every trigger that wants it, and
// reports how many deliveries it made. Any replica may raise: deliveries
// are rows the leader sends.
func RaiseTriggerEvent(ev RaisedEvent) (int, error) {
	raiserMu.RLock()
	d := raiser
	raiserMu.RUnlock()
	if d == nil {
		return 0, ErrTriggersNotRunning
	}
	return d.raise(ev)
}

func (d *triggerDispatcher) raise(ev RaisedEvent) (int, error) {
	var kind *TriggerEventType
	for _, t := range TriggerEventTypes() {
		if t.Name == ev.Type {
			kind = &t
			break
		}
	}
	switch {
	case kind == nil:
		return 0, fmt.Errorf("event type %q is not registered", ev.Type)
	case ev.Type == TriggerEventRunFailed || ev.Type == TriggerEventRunBlocked || ev.Type == TriggerEventRunRecovered ||
		ev.Type == TriggerEventMessageReceived:
		return 0, fmt.Errorf("%s is raised by Brokoli from the run event log", ev.Type)
	case ev.RunID == "" && ev.Key == "":
		return 0, errors.New("a raised event names its run, or a key for the occurrence")
	}
	p, err := d.s.GetPipeline(ev.PipelineID)
	if err != nil {
		return 0, fmt.Errorf("pipeline %q: %w", ev.PipelineID, err)
	}
	var run *models.Run
	if ev.RunID != "" {
		if run, err = d.s.GetRun(ev.RunID); err != nil || run.PipelineID != p.ID {
			return 0, fmt.Errorf("run %q is not a run of pipeline %q", ev.RunID, p.ID)
		}
	}
	now := d.now()
	at := ev.OccurredAt
	if at.IsZero() {
		at = now
	}
	// The unique key is (trigger, run, event): an occurrence with no run
	// takes the run's place, marked so it cannot be read as a run id. A
	// key is per pipeline: a trigger on the whole workspace is told that
	// each pipeline missed "2026-11-01".
	occurrence := ev.RunID
	if occurrence == "" {
		occurrence = "raised:" + p.ID + ":" + ev.Key
	}
	tool := kind.SuggestedTool
	if tool == "" {
		tool = "get_pipeline"
	}
	triggers, err := d.ts.List("", "")
	if err != nil {
		return 0, err
	}
	made := 0
	for i := range triggers {
		t := &triggers[i]
		if !t.Watches(ev.Type, p.ID) || !sameWorkspace(p.WorkspaceID, t.WorkspaceID) || at.Before(t.CreatedAt.Add(-time.Second)) {
			continue
		}
		del := &TriggerDelivery{ID: newDeliveryID(), TriggerID: t.ID, AgentID: t.AgentID, WorkspaceID: t.WorkspaceID,
			PipelineID: p.ID, RunID: occurrence, Event: ev.Type}
		body, err := d.raisedBody(del, t, p, run, at, ev.Summary, tool)
		if err != nil {
			return made, err
		}
		ok, err := d.ts.Enqueue(del, string(body), time.Duration(t.CooldownSeconds)*time.Second, now)
		if err != nil {
			return made, err
		}
		if ok {
			made++
		}
	}
	return made, nil
}

func (d *triggerDispatcher) raisedBody(del *TriggerDelivery, t *AgentTrigger, p *models.Pipeline, run *models.Run,
	at time.Time, summary, tool string) ([]byte, error) {
	if run != nil {
		body, err := d.eventBody(del, t, p, run, models.RunEvent{CreatedAt: at}, false)
		if err != nil {
			return nil, err
		}
		var e TriggerEvent
		if err := json.Unmarshal(body, &e); err != nil {
			return nil, err
		}
		e.Summary = mcpserver.Clip(mcpserver.RedactText(summary), mcpserver.MaxText)
		e.Next.SuggestedTool = tool
		return json.Marshal(e)
	}
	agentName := t.AgentID
	if a, err := d.users.GetAgent(t.AgentID); err == nil {
		agentName = a.Name
	}
	e := TriggerEvent{
		Type: del.Event, ID: del.ID, OccurredAt: at.UTC(), WorkspaceID: t.WorkspaceID, Agent: agentName,
		Pipeline: TriggerPipeline{ID: p.ID, Name: p.Name},
		Summary:  mcpserver.Clip(mcpserver.RedactText(summary), mcpserver.MaxText),
		Next:     TriggerNext{SuggestedTool: tool},
	}
	if d.publicURL != "" {
		e.Next.MCP = d.publicURL + "/api/mcp"
	}
	return json.Marshal(e)
}

// raiseAgentMessage tells an agent's triggers that a person wrote to it
// (ADR-048 section 3): every enabled trigger of the agent in the workspace
// that subscribes to message.received, whatever pipeline it watches, with
// no cooldown. It answers how many deliveries it made; with no dispatcher
// running it makes none, and the agent reads the message when it checks.
func raiseAgentMessage(agentID, agentName, workspaceID string, m *Message, from string) int {
	raiserMu.RLock()
	d := raiser
	raiserMu.RUnlock()
	if d == nil {
		return 0
	}
	triggers, err := d.ts.List("", "")
	if err != nil {
		return 0
	}
	now := d.now()
	made := 0
	for i := range triggers {
		t := &triggers[i]
		if t.AgentID != agentID || !t.Enabled || !sameWorkspace(t.WorkspaceID, workspaceID) || !slices.Contains(t.Events, TriggerEventMessageReceived) {
			continue
		}
		del := &TriggerDelivery{ID: newDeliveryID(), TriggerID: t.ID, AgentID: agentID, WorkspaceID: t.WorkspaceID,
			RunID: "message:" + m.ID, Event: TriggerEventMessageReceived}
		e := TriggerEvent{Type: del.Event, ID: del.ID, OccurredAt: m.CreatedAt.UTC(), WorkspaceID: t.WorkspaceID, Agent: agentName,
			Message: &TriggerMessage{ConversationID: m.ConversationID, MessageID: m.ID, From: from, Text: m.Text},
			Next:    TriggerNext{SuggestedTool: "read_messages"}}
		if d.publicURL != "" {
			e.Next.MCP = d.publicURL + "/api/mcp"
		}
		body, err := json.Marshal(e)
		if err != nil {
			continue
		}
		if ok, err := d.ts.Enqueue(del, string(body), 0, now); err == nil && ok {
			made++
		}
	}
	return made
}
