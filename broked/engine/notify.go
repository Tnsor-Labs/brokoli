package engine

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/hc12r/broked/models"
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
