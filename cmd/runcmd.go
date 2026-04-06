package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run <pipeline-id>",
	Short: "Trigger a pipeline run and wait for completion",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pipelineID := args[0]
		serverURL, _ := cmd.Flags().GetString("server")
		runAPIKey, _ := cmd.Flags().GetString("api-key")
		timeout, _ := cmd.Flags().GetInt("timeout")

		// Trigger the run
		client := &http.Client{}
		req, err := http.NewRequest("POST", serverURL+"/api/pipelines/"+pipelineID+"/run", nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		if runAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+runAPIKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to trigger run: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("trigger failed: %s", string(body))
		}

		var run struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
		fmt.Printf("Run started: %s\n", run.ID)

		// Poll for completion
		deadline := time.Now().Add(time.Duration(timeout) * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)

			pollReq, err := http.NewRequest("GET", serverURL+"/api/runs/"+run.ID, nil)
			if err != nil {
				continue
			}
			if runAPIKey != "" {
				pollReq.Header.Set("Authorization", "Bearer "+runAPIKey)
			}
			pollResp, err := client.Do(pollReq)
			if err != nil {
				continue
			}

			var status struct {
				Status string `json:"status"`
			}
			json.NewDecoder(pollResp.Body).Decode(&status)
			pollResp.Body.Close()

			fmt.Printf("Status: %s\n", status.Status)

			switch status.Status {
			case "completed":
				fmt.Println("Run completed successfully")
				return nil
			case "failed":
				return fmt.Errorf("run failed")
			case "cancelled":
				return fmt.Errorf("run was cancelled")
			}
		}
		return fmt.Errorf("timeout waiting for run to complete")
	},
}

func init() {
	runCmd.Flags().String("server", "http://localhost:8080", "Brokoli server URL")
	runCmd.Flags().String("api-key", "", "API key for authentication")
	runCmd.Flags().Int("timeout", 300, "Timeout in seconds")
	rootCmd.AddCommand(runCmd)
}
