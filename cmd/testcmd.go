package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var assertCmd = &cobra.Command{
	Use:   "assert <pipeline-id>",
	Short: "Run a pipeline and validate assertions against its output",
	Long:  `Triggers a pipeline run in test mode and evaluates assertions defined in a YAML file. Exits 0 if all pass, 1 if any fail.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pipelineID := args[0]
		assertionFile, _ := cmd.Flags().GetString("assertions")
		serverURL, _ := cmd.Flags().GetString("server")
		assertAPIKey, _ := cmd.Flags().GetString("api-key")

		// Load assertions from YAML file
		data, err := os.ReadFile(assertionFile)
		if err != nil {
			return fmt.Errorf("failed to read assertions file: %w", err)
		}

		var suite engine.AssertionSuite
		if err := yaml.Unmarshal(data, &suite); err != nil {
			return fmt.Errorf("failed to parse assertions: %w", err)
		}
		suite.PipelineID = pipelineID

		// Post assertions to server for evaluation
		assertionJSON, err := json.Marshal(suite)
		if err != nil {
			return fmt.Errorf("failed to marshal assertions: %w", err)
		}

		client := &http.Client{}
		req, err := http.NewRequest("POST", serverURL+"/api/pipelines/"+pipelineID+"/test",
			bytes.NewReader(assertionJSON))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if assertAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+assertAPIKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("test request failed: %w", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		var results struct {
			Results []engine.AssertionResult `json:"results"`
			Passed  int                      `json:"passed"`
			Failed  int                      `json:"failed"`
			Total   int                      `json:"total"`
		}
		if err := json.Unmarshal(body, &results); err != nil {
			return fmt.Errorf("failed to parse response: %w\n%s", err, string(body))
		}

		// Print results
		for _, r := range results.Results {
			if r.Passed {
				fmt.Printf("  PASS  %s\n", r.Name)
			} else {
				fmt.Printf("  FAIL  %s: %s (expected: %s, got: %s)\n", r.Name, r.Message, r.Expected, r.Actual)
			}
		}

		fmt.Printf("\n%d/%d assertions passed\n", results.Passed, results.Total)

		if results.Failed > 0 {
			os.Exit(1)
		}
		return nil
	},
}

func init() {
	assertCmd.Flags().StringP("assertions", "a", "assertions.yaml", "Assertions YAML file")
	assertCmd.Flags().String("server", "http://localhost:8080", "Brokoli server URL")
	assertCmd.Flags().String("api-key", "", "API key for authentication")
	rootCmd.AddCommand(assertCmd)
}
