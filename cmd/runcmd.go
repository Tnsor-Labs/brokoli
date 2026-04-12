package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run <pipeline-id>",
	Short: "Trigger a pipeline run and wait for completion",
	Long: `Triggers a pipeline run via the API and streams progress.

With --follow, connects via WebSocket for real-time log streaming.
Without --follow, polls the run status every 2 seconds.

Authentication is read from ~/.brokoli/config.json (set via brokoli login)
or overridden with --server and --api-key flags.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pipelineID := args[0]
		serverURL, apiToken := resolveAuth(cmd)
		timeout, _ := cmd.Flags().GetInt("timeout")
		follow, _ := cmd.Flags().GetBool("follow")

		run, err := triggerRun(serverURL, apiToken, pipelineID)
		if err != nil {
			return err
		}
		fmt.Printf("Run started: %s (status: %s)\n", run.ID, run.Status)

		if run.Status == "blocked" {
			fmt.Printf("Blocked: %s\n", run.Error)
			return fmt.Errorf("run blocked by unsatisfied dependencies")
		}

		if follow {
			return followRunWS(serverURL, apiToken, run.ID, timeout)
		}
		return pollRunStatus(serverURL, apiToken, run.ID, timeout)
	},
}

type runResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func triggerRun(serverURL, token, pipelineID string) (*runResponse, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", serverURL+"/api/pipelines/"+pipelineID+"/run", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trigger run: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trigger failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var run runResponse
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &run, nil
}

func pollRunStatus(serverURL, token, runID string, timeoutSec int) error {
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		req, err := http.NewRequest("GET", serverURL+"/api/runs/"+runID, nil)
		if err != nil {
			continue
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		var status runResponse
		json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()

		fmt.Printf("  %s\n", status.Status)

		switch status.Status {
		case "success":
			fmt.Println("Run completed successfully")
			return nil
		case "failed":
			return fmt.Errorf("run failed: %s", status.Error)
		case "cancelled":
			return fmt.Errorf("run was cancelled")
		case "blocked":
			return fmt.Errorf("run blocked: %s", status.Error)
		}
	}
	return fmt.Errorf("timeout after %ds waiting for run to complete", timeoutSec)
}

// followRunWS connects to the WebSocket endpoint and streams run events in real time.
// Falls back to polling if the WebSocket handshake fails.
func followRunWS(serverURL, token, runID string, timeoutSec int) error {
	wsURL, err := httpToWS(serverURL)
	if err != nil {
		fmt.Println("WebSocket unavailable, falling back to polling")
		return pollRunStatus(serverURL, token, runID, timeoutSec)
	}

	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"/api/ws", header)
	if err != nil {
		fmt.Printf("WebSocket connect failed (%v), falling back to polling\n", err)
		return pollRunStatus(serverURL, token, runID, timeoutSec)
	}
	defer conn.Close()

	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	conn.SetReadDeadline(deadline)

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return pollRunStatus(serverURL, token, runID, timeoutSec)
		}

		var event struct {
			Type       string `json:"type"`
			RunID      string `json:"run_id"`
			PipelineID string `json:"pipeline_id"`
			NodeID     string `json:"node_id"`
			Status     string `json:"status"`
			Error      string `json:"error"`
			Message    string `json:"message"`
			Level      string `json:"level"`
			RowCount   int    `json:"row_count"`
			DurationMs int64  `json:"duration_ms"`
		}
		if json.Unmarshal(msg, &event) != nil {
			continue
		}

		if event.RunID != runID {
			continue
		}

		switch event.Type {
		case "run.started":
			fmt.Println("  running")
		case "node.started":
			fmt.Printf("  [node] %s started\n", event.NodeID)
		case "node.completed":
			fmt.Printf("  [node] %s completed (%d rows, %dms)\n", event.NodeID, event.RowCount, event.DurationMs)
		case "node.failed":
			fmt.Printf("  [node] %s failed: %s\n", event.NodeID, event.Error)
		case "log":
			if event.Level == "error" || event.Level == "warning" {
				fmt.Printf("  [%s] %s\n", event.Level, event.Message)
			}
		case "run.completed":
			fmt.Println("Run completed successfully")
			return nil
		case "run.failed":
			return fmt.Errorf("run failed: %s", event.Error)
		}
	}
}

func httpToWS(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
	return u.String(), nil
}

func init() {
	runCmd.Flags().String("server", "http://localhost:8080", "Brokoli server URL")
	runCmd.Flags().String("api-key", "", "API key for authentication")
	runCmd.Flags().Int("timeout", 300, "Timeout in seconds")
	runCmd.Flags().Bool("follow", false, "Stream logs via WebSocket instead of polling")
	runCmd.Flags().BoolP("f", "f", false, "Short for --follow")
	rootCmd.AddCommand(runCmd)
}
