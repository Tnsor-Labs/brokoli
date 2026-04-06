package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// AlertChannel defines a notification channel.
type AlertChannel struct {
	Type   string            `json:"type"`   // slack, pagerduty, email, webhook
	Config map[string]string `json:"config"` // channel-specific config
}

// AlertPayload is the standardized alert data.
type AlertPayload struct {
	Pipeline   string `json:"pipeline"`
	PipelineID string `json:"pipeline_id"`
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Duration   string `json:"duration,omitempty"`
	Timestamp  string `json:"timestamp"`
}

// SendAlert dispatches an alert to a channel.
func SendAlert(channel AlertChannel, payload AlertPayload) {
	switch channel.Type {
	case "slack":
		sendSlack(channel.Config, payload)
	case "pagerduty":
		sendPagerDuty(channel.Config, payload)
	case "email":
		sendEmail(channel.Config, payload)
	case "webhook":
		sendWebhookAlert(channel.Config, payload)
	default:
		log.Printf("alert: unknown channel type: %s", channel.Type)
	}
}

func sendSlack(config map[string]string, p AlertPayload) {
	webhookURL := config["webhook_url"]
	if webhookURL == "" {
		return
	}

	emoji := ":white_check_mark:"
	color := "#22c55e"
	if p.Status == "failed" {
		emoji = ":x:"
		color = "#ef4444"
	}

	blocks := map[string]interface{}{
		"attachments": []map[string]interface{}{
			{
				"color": color,
				"blocks": []map[string]interface{}{
					{
						"type": "section",
						"text": map[string]string{
							"type": "mrkdwn",
							"text": fmt.Sprintf("%s *Pipeline %s*\nStatus: `%s`", emoji, p.Pipeline, p.Status),
						},
					},
					{
						"type": "context",
						"elements": []map[string]string{
							{"type": "mrkdwn", "text": fmt.Sprintf("Run: `%s` | %s", p.RunID[:8], p.Timestamp)},
						},
					},
				},
			},
		},
	}
	if p.Error != "" {
		blocks["attachments"].([]map[string]interface{})[0]["blocks"] = append(
			blocks["attachments"].([]map[string]interface{})[0]["blocks"].([]map[string]interface{}),
			map[string]interface{}{
				"type": "section",
				"text": map[string]string{"type": "mrkdwn", "text": fmt.Sprintf("```%s```", p.Error)},
			},
		)
	}

	body, _ := json.Marshal(blocks)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("slack alert failed: %v", err)
		return
	}
	resp.Body.Close()
}

func sendPagerDuty(config map[string]string, p AlertPayload) {
	routingKey := config["routing_key"]
	if routingKey == "" || p.Status != "failed" {
		return // only alert on failures
	}

	event := map[string]interface{}{
		"routing_key":  routingKey,
		"event_action": "trigger",
		"payload": map[string]interface{}{
			"summary":   fmt.Sprintf("Pipeline %s failed", p.Pipeline),
			"severity":  "critical",
			"source":    "brokoli",
			"component": p.Pipeline,
			"custom_details": map[string]string{
				"pipeline_id": p.PipelineID,
				"run_id":      p.RunID,
				"error":       p.Error,
			},
		},
	}

	body, _ := json.Marshal(event)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("https://events.pagerduty.com/v2/enqueue", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("pagerduty alert failed: %v", err)
		return
	}
	resp.Body.Close()
}

func sendEmail(config map[string]string, p AlertPayload) {
	smtpHost := config["smtp_host"]
	smtpPort := config["smtp_port"]
	from := config["from"]
	to := config["to"]
	password := config["password"]

	if smtpHost == "" || from == "" || to == "" {
		return
	}
	if smtpPort == "" {
		smtpPort = "587"
	}

	subject := fmt.Sprintf("Pipeline %s: %s", p.Pipeline, strings.ToUpper(p.Status))
	body := fmt.Sprintf("Pipeline: %s\nStatus: %s\nRun ID: %s\nTime: %s\n",
		p.Pipeline, p.Status, p.RunID, p.Timestamp)
	if p.Error != "" {
		body += fmt.Sprintf("\nError:\n%s\n", p.Error)
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s", from, to, subject, body)

	auth := smtp.PlainAuth("", from, password, smtpHost)
	err := smtp.SendMail(smtpHost+":"+smtpPort, auth, from, []string{to}, []byte(msg))
	if err != nil {
		log.Printf("email alert failed: %v", err)
	}
}

func sendWebhookAlert(config map[string]string, p AlertPayload) {
	url := config["url"]
	if url == "" {
		return
	}
	body, _ := json.Marshal(p)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("webhook alert failed: %v", err)
		return
	}
	resp.Body.Close()
}
