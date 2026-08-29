package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var backfillCmd = &cobra.Command{
	Use:   "backfill <pipeline-id> --start <RFC3339> --end <RFC3339>",
	Short: "Run a pipeline once per schedule interval across a date range",
	Long: `Enumerates the pipeline's own schedule between --start and --end and
creates one run per complete interval, oldest first, at concurrency 1
(ADR-028). Each run carries its interval, so ${interval.start} and
${interval.end} resolve to that slice.

The command returns as soon as the server accepts the plan; the runs
themselves appear in run history as they execute, trigger "backfill".

Bare dates are accepted: --start 2026-08-01 means midnight UTC, and
--end 2026-08-03 covers through the END of that day (the inclusive-day
reading the old date-only backfill always had).

A pipeline that never references ${interval.*} or ${param.date} is
refused, because every run would do identical work; --force overrides.

Authentication is read from ~/.brokoli/config.json (set via brokoli
login) or overridden with --server and --api-key flags.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pipelineID := args[0]
		serverURL, apiToken := resolveAuth(cmd)
		start, _ := cmd.Flags().GetString("start")
		end, _ := cmd.Flags().GetString("end")
		force, _ := cmd.Flags().GetBool("force")
		if start == "" || end == "" {
			return fmt.Errorf("--start and --end are required (RFC3339 or YYYY-MM-DD)")
		}

		// A bare date becomes the field the API grandfathers for it, so
		// the inclusive-day semantics live in exactly one place (the
		// server) instead of being reimplemented here.
		body := map[string]interface{}{"force": force}
		if isBareDate(start) {
			body["start_date"] = start
		} else {
			body["start"] = start
		}
		if isBareDate(end) {
			body["end_date"] = end
		} else {
			body["end"] = end
		}
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}

		//netguard:allow client-side CLI call to the user's configured Brokoli server
		client := &http.Client{Timeout: 30 * time.Second}
		req, err := http.NewRequest("POST",
			serverURL+"/api/pipelines/"+pipelineID+"/backfill", bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if apiToken != "" {
			req.Header.Set("Authorization", "Bearer "+apiToken)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("backfill: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			raw, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("backfill refused (HTTP %d): %s",
				resp.StatusCode, strings.TrimSpace(string(raw)))
		}

		var plan struct {
			Intervals   int       `json:"intervals"`
			First       time.Time `json:"first_interval_start"`
			Last        time.Time `json:"last_interval_end"`
			Concurrency int       `json:"concurrency"`
			Note        string    `json:"note"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&plan); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("Backfill accepted: %d interval(s), %s .. %s, concurrency %d\n",
			plan.Intervals, plan.First.Format(time.RFC3339), plan.Last.Format(time.RFC3339),
			plan.Concurrency)
		if plan.Note != "" {
			fmt.Println(plan.Note)
		}
		return nil
	},
}

func isBareDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func init() {
	backfillCmd.Flags().String("start", "", "range start, RFC3339 or YYYY-MM-DD (required)")
	backfillCmd.Flags().String("end", "", "range end, RFC3339 or YYYY-MM-DD (end of day for a bare date; required)")
	backfillCmd.Flags().Bool("force", false, "backfill even if the pipeline never references ${interval.*}")
	backfillCmd.Flags().String("server", "http://localhost:8080", "Brokoli server URL")
	backfillCmd.Flags().String("api-key", "", "API key for authentication")
	rootCmd.AddCommand(backfillCmd)
}
