package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hc12r/broked/models"
	"github.com/hc12r/brokolisql-go/pkg/common"
)

// runNotify sends a notification via webhook, Slack, or email.
// Passes through the input dataset unchanged.
func (r *Runner) runNotify(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	notifyType, _ := node.Config["notify_type"].(string)
	webhookURL, _ := node.Config["webhook_url"].(string)
	message, _ := node.Config["message"].(string)
	channel, _ := node.Config["channel"].(string)

	if notifyType == "" {
		notifyType = "webhook"
	}

	// Template message with pipeline info
	if message == "" {
		message = fmt.Sprintf("Pipeline %s completed", r.pipe.Name)
	}
	message = strings.ReplaceAll(message, "{{pipeline}}", r.pipe.Name)
	message = strings.ReplaceAll(message, "{{run_id}}", r.run.ID)
	if input != nil {
		message = strings.ReplaceAll(message, "{{rows}}", fmt.Sprintf("%d", len(input.Rows)))
	}

	switch notifyType {
	case "slack":
		return r.notifySlack(node, webhookURL, channel, message, input)
	case "webhook":
		return r.notifyWebhook(node, webhookURL, message, input)
	default:
		return nil, fmt.Errorf("unsupported notify type: %s (use slack or webhook)", notifyType)
	}
}

func (r *Runner) notifySlack(node models.Node, webhookURL, channel, message string, input *common.DataSet) (*common.DataSet, error) {
	if webhookURL == "" {
		return nil, fmt.Errorf("slack webhook_url is required")
	}

	payload := map[string]interface{}{
		"text": message,
	}
	if channel != "" {
		payload["channel"] = channel
	}

	body, _ := json.Marshal(payload)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("slack notification failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("slack returned status %d", resp.StatusCode)
	}

	r.log(node.ID, models.LogLevelInfo, "Slack notification sent")
	return input, nil
}

func (r *Runner) notifyWebhook(node models.Node, webhookURL, message string, input *common.DataSet) (*common.DataSet, error) {
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required")
	}

	payload := map[string]interface{}{
		"pipeline":   r.pipe.Name,
		"run_id":     r.run.ID,
		"message":    message,
		"row_count":  0,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	}
	if input != nil {
		payload["row_count"] = len(input.Rows)
	}

	body, _ := json.Marshal(payload)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("webhook notification failed: %w", err)
	}
	defer resp.Body.Close()

	r.log(node.ID, models.LogLevelInfo, "Webhook notification sent (status %d)", resp.StatusCode)
	return input, nil
}
