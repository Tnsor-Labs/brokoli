package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// runNotify sends a notification via webhook, Slack, or email.
// Passes through the input dataset unchanged.
// runNotify posts under ctx, the attempt's context, so a slow endpoint
// cannot hold the attempt past its timeout. It used to post with no
// context and only the client's own 10s limit.
func (r *Runner) runNotify(ctx context.Context, node models.Node, input *common.DataSet) (*common.DataSet, error) {
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
		return r.notifySlack(ctx, node, webhookURL, channel, message, input)
	case "webhook":
		return r.notifyWebhook(ctx, node, webhookURL, message, input)
	default:
		return nil, fmt.Errorf("unsupported notify type: %s (use slack or webhook)", notifyType)
	}
}

func (r *Runner) notifySlack(ctx context.Context, node models.Node, webhookURL, channel, message string, input *common.DataSet) (*common.DataSet, error) {
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
	client := netguard.Outbound().Client(10 * time.Second)
	resp, err := postJSON(ctx, client, webhookURL, body)
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

func (r *Runner) notifyWebhook(ctx context.Context, node models.Node, webhookURL, message string, input *common.DataSet) (*common.DataSet, error) {
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required")
	}

	payload := map[string]interface{}{
		"pipeline":  r.pipe.Name,
		"run_id":    r.run.ID,
		"message":   message,
		"row_count": 0,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if input != nil {
		payload["row_count"] = len(input.Rows)
	}

	body, _ := json.Marshal(payload)
	client := netguard.Outbound().Client(10 * time.Second)
	resp, err := postJSON(ctx, client, webhookURL, body)
	if err != nil {
		return nil, fmt.Errorf("webhook notification failed: %w", err)
	}
	defer resp.Body.Close()

	r.log(node.ID, models.LogLevelInfo, "Webhook notification sent (status %d)", resp.StatusCode)
	return input, nil
}

// postJSON posts body as JSON with ctx on the request.
func postJSON(ctx context.Context, client *http.Client, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return client.Do(req)
}
