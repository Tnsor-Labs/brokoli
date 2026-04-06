package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
)

// WebhookPayload is the JSON sent to webhook URLs on pipeline events.
type WebhookPayload struct {
	Event      string    `json:"event"`
	Pipeline   string    `json:"pipeline"`
	PipelineID string    `json:"pipeline_id"`
	RunID      string    `json:"run_id"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// SendWebhook fires an HTTP POST to the given URL with the event payload.
func SendWebhook(url string, payload WebhookPayload) {
	if url == "" {
		return
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("webhook: marshal error: %v", err)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("webhook: POST %s failed: %v", url, err)
		return
	}
	resp.Body.Close()
	log.Printf("webhook: POST %s -> %d", url, resp.StatusCode)
}

// sendNotification dispatches an alert via the configured NotificationProvider.
func (r *Runner) sendNotification(event, severity, title, message string) {
	if r.notifier == nil || !r.notifier.Enabled() {
		return
	}
	runID := ""
	if r.run != nil {
		runID = r.run.ID
	}

	extra := make(map[string]string)
	extra["schedule"] = r.pipe.Schedule

	// Duration
	if r.run != nil && r.run.StartedAt != nil {
		end := time.Now()
		if r.run.FinishedAt != nil {
			end = *r.run.FinishedAt
		}
		dur := end.Sub(*r.run.StartedAt)
		if dur < time.Second {
			extra["duration"] = fmt.Sprintf("%dms", dur.Milliseconds())
		} else if dur < time.Minute {
			extra["duration"] = fmt.Sprintf("%.1fs", dur.Seconds())
		} else {
			extra["duration"] = fmt.Sprintf("%.1fm", dur.Minutes())
		}
	}

	// Node stats
	total := len(r.pipe.Nodes)
	extra["nodes"] = fmt.Sprintf("%d", total)

	// Failed node name
	if r.run != nil && severity == "critical" {
		for _, nr := range r.run.NodeRuns {
			if nr.Error != "" {
				node := r.nodeByID(nr.NodeID)
				if node != nil {
					extra["failed_node"] = node.Name
				}
				break
			}
		}
	}

	n := extensions.Notification{
		Event:      event,
		Severity:   severity,
		Title:      title,
		Message:    message,
		PipelineID: r.pipe.ID,
		Pipeline:   r.pipe.Name,
		RunID:      runID,
		Extra:      extra,
	}
	if err := r.notifier.Send(n); err != nil {
		r.log("", models.LogLevelWarning, "Notification (%s) failed: %v", r.notifier.Name(), err)
	}
}

// NotifyPipelineEvent sends webhook notifications if configured.
func NotifyPipelineEvent(pipe *models.Pipeline, run *models.Run, event string, errMsg string) {
	if pipe.WebhookURL == "" {
		return
	}

	go SendWebhook(pipe.WebhookURL, WebhookPayload{
		Event:      event,
		Pipeline:   pipe.Name,
		PipelineID: pipe.ID,
		RunID:      run.ID,
		Status:     string(run.Status),
		Error:      errMsg,
		Timestamp:  time.Now(),
	})
}
