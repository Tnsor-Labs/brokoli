package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// brokoli agents trigger: the events that start an agent's work (ADR-047).

var agentsTriggerCmd = &cobra.Command{
	Use:   "trigger",
	Short: "Subscribe an agent to run events: a failed or blocked run is delivered to it",
	Long: `A trigger delivers run events to an agent, so it starts work without a
person starting it: a run fails, the agent is told, and it investigates over
MCP with its own token. Deliveries are signed (Standard Webhooks) with a
secret shown once, when the trigger is made. See docs/agent-triggers.md.`,
}

type triggerRecord struct {
	ID              string     `json:"id"`
	PipelineID      string     `json:"pipeline_id"`
	Events          []string   `json:"events"`
	URL             string     `json:"url"`
	CooldownSeconds int        `json:"cooldown_seconds"`
	Enabled         bool       `json:"enabled"`
	SecretRotatedAt *time.Time `json:"secret_rotated_at"`
}

func triggersPath(agentID string, rest ...string) string {
	p := "/api/agents/" + url.PathEscape(agentID) + "/triggers"
	for _, r := range rest {
		p += "/" + url.PathEscape(r)
	}
	return p
}

// triggerBody is what the flags that were set say about a trigger.
func triggerBody(cmd *cobra.Command) (map[string]any, error) {
	body := map[string]any{}
	if cmd.Flags().Changed("events") {
		events, _ := cmd.Flags().GetStringSlice("events")
		body["events"] = events
	}
	if cmd.Flags().Changed("url") {
		u, _ := cmd.Flags().GetString("url")
		body["url"] = u
	}
	if cmd.Flags().Changed("pipeline") {
		p, _ := cmd.Flags().GetString("pipeline")
		body["pipeline_id"] = p
	}
	if cmd.Flags().Changed("cooldown") {
		d, _ := cmd.Flags().GetDuration("cooldown")
		if d < 0 {
			return nil, fmt.Errorf("--cooldown cannot be negative")
		}
		body["cooldown_seconds"] = int(d / time.Second)
	}
	enable, _ := cmd.Flags().GetBool("enable")
	disable, _ := cmd.Flags().GetBool("disable")
	switch {
	case enable && disable:
		return nil, fmt.Errorf("--enable and --disable together")
	case enable:
		body["enabled"] = true
	case disable:
		body["enabled"] = false
	}
	return body, nil
}

func scope(t triggerRecord) string {
	if t.PipelineID == "" {
		return "every pipeline"
	}
	return t.PipelineID
}

var agentsTriggerListCmd = &cobra.Command{
	Use:   "list <agent>",
	Short: "List an agent's triggers in the workspace (never their secrets)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		var triggers []triggerRecord
		if err := agentsCall(cmd, http.MethodGet, triggersPath(a.ID), nil, &triggers); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tEVENTS\tPIPELINE\tURL\tCOOLDOWN\tSTATE")
		for _, t := range triggers {
			state := "enabled"
			if !t.Enabled {
				state = "disabled"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, strings.Join(t.Events, ","), scope(t), t.URL,
				time.Duration(t.CooldownSeconds)*time.Second, state)
		}
		return tw.Flush()
	},
}

var agentsTriggerCreateCmd = &cobra.Command{
	Use:   "create <agent>",
	Short: "Make a trigger; its signing secret is printed once",
	Example: `  brokoli agents trigger create triage-bot --url https://agents.example.com/brokoli --events run.failed
  brokoli agents trigger create triage-bot --url https://agents.example.com/brokoli --events run.failed,run.blocked --pipeline orders_daily --cooldown 1h`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		body, err := triggerBody(cmd)
		if err != nil {
			return err
		}
		var res struct {
			Trigger triggerRecord `json:"trigger"`
			Secret  string        `json:"secret"`
		}
		if err := agentsCall(cmd, http.MethodPost, triggersPath(a.ID), body, &res); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Trigger %s delivers %s about %s to %s.\nIts signing secret is shown only now:\n",
			res.Trigger.ID, strings.Join(res.Trigger.Events, " and "), scope(res.Trigger), res.Trigger.URL)
		fmt.Println(res.Secret)
		return nil
	},
}

var agentsTriggerUpdateCmd = &cobra.Command{
	Use:   "update <agent> <trigger-id>",
	Short: "Change a trigger's events, URL, pipeline or cooldown, or enable or disable it",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		body, err := triggerBody(cmd)
		if err != nil {
			return err
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to change: give --events, --url, --pipeline, --cooldown, --enable or --disable")
		}
		var t triggerRecord
		if err := agentsCall(cmd, http.MethodPatch, triggersPath(a.ID, args[1]), body, &t); err != nil {
			return err
		}
		fmt.Printf("Updated %s: %s about %s to %s.\n", t.ID, strings.Join(t.Events, " and "), scope(t), t.URL)
		return nil
	},
}

var agentsTriggerDeleteCmd = &cobra.Command{
	Use:   "delete <agent> <trigger-id>",
	Short: "Remove a trigger",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		if err := agentsCall(cmd, http.MethodDelete, triggersPath(a.ID, args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Println("Removed.")
		return nil
	},
}

var agentsTriggerRotateCmd = &cobra.Command{
	Use:   "rotate-secret <agent> <trigger-id>",
	Short: "Give a trigger a new signing secret; the old one also signs for 24 hours",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		var res struct {
			Secret string `json:"secret"`
		}
		if err := agentsCall(cmd, http.MethodPost, triggersPath(a.ID, args[1], "secret"), nil, &res); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "New signing secret, shown only now. For 24 hours deliveries carry signatures from both:")
		fmt.Println(res.Secret)
		return nil
	},
}

type deliveryRecord struct {
	ID         string     `json:"id"`
	Event      string     `json:"event"`
	PipelineID string     `json:"pipeline_id"`
	RunID      string     `json:"run_id"`
	Status     string     `json:"status"`
	Attempts   int        `json:"attempts"`
	LastStatus int        `json:"last_status"`
	Reason     string     `json:"reason"`
	LastError  string     `json:"last_error"`
	CreatedAt  time.Time  `json:"created_at"`
	Next       *time.Time `json:"next_attempt_at"`
}

var agentsTriggerDeliveriesCmd = &cobra.Command{
	Use:   "deliveries <agent> <trigger-id>",
	Short: "List a trigger's newest deliveries: what was sent, when, and what came back",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		var out []deliveryRecord
		if err := agentsCall(cmd, http.MethodGet, triggersPath(a.ID, args[1], "deliveries"), nil, &out); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tEVENT\tRUN\tCREATED\tSTATUS\tATTEMPTS\tLAST\tDETAIL")
		for _, d := range out {
			detail := d.Reason
			if detail == "" && d.Status == "pending" && d.Next != nil {
				detail = "next try " + d.Next.Local().Format("15:04:05")
				if d.LastError != "" {
					detail += " (" + d.LastError + ")"
				}
			}
			last := "-"
			if d.LastStatus != 0 {
				last = fmt.Sprint(d.LastStatus)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", d.ID, d.Event, d.RunID, d.CreatedAt.Local().Format("2006-01-02 15:04"),
				d.Status, d.Attempts, last, detail)
		}
		return tw.Flush()
	},
}

var agentsTriggerRedeliverCmd = &cobra.Command{
	Use:   "redeliver <agent> <trigger-id> <delivery-id>",
	Short: "Send a delivery again, now (it keeps its id, so a receiver can tell)",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		if err := agentsCall(cmd, http.MethodPost, triggersPath(a.ID, args[1], "deliveries", args[2], "redeliver"), nil, nil); err != nil {
			return err
		}
		fmt.Println("Queued; it is sent within a few seconds.")
		return nil
	},
}

var agentsTriggerTestCmd = &cobra.Command{
	Use:   "test <agent> <trigger-id>",
	Short: "Deliver a test run.failed (marked \"test\": true), to check the receiver",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		var res struct {
			DeliveryID string `json:"delivery_id"`
			PipelineID string `json:"pipeline_id"`
		}
		if err := agentsCall(cmd, http.MethodPost, triggersPath(a.ID, args[1], "test"), nil, &res); err != nil {
			return err
		}
		fmt.Printf("Queued test delivery %s about pipeline %s; it is sent within a few seconds.\n", res.DeliveryID, res.PipelineID)
		fmt.Printf("Check it with: brokoli agents trigger deliveries %s %s\n", args[0], args[1])
		return nil
	},
}

func init() {
	all := []*cobra.Command{agentsTriggerListCmd, agentsTriggerCreateCmd, agentsTriggerUpdateCmd, agentsTriggerDeleteCmd,
		agentsTriggerRotateCmd, agentsTriggerDeliveriesCmd, agentsTriggerRedeliverCmd, agentsTriggerTestCmd}
	for _, c := range all {
		// A refusal from the server is the answer; the usage text is not.
		c.SilenceUsage = true
		c.Flags().String("server", "http://localhost:8080", "Brokoli server URL (default: the one 'brokoli login' stored)")
		c.Flags().String("api-key", "", "credential to act with (default: the one 'brokoli login' stored)")
		c.Flags().String("workspace", "", "the workspace the trigger belongs to (default: yours)")
	}
	for _, c := range []*cobra.Command{agentsTriggerCreateCmd, agentsTriggerUpdateCmd} {
		c.Flags().String("url", "", "where deliveries are sent (http or https)")
		c.Flags().StringSlice("events", nil, "run.failed, run.blocked, run.recovered")
		c.Flags().String("pipeline", "", "one pipeline id to watch (default: every pipeline in the workspace)")
		c.Flags().Duration("cooldown", 30*time.Minute, "after a delivery, how long to stay quiet about the same pipeline")
	}
	agentsTriggerCreateCmd.Flags().Bool("disable", false, "make it disabled")
	agentsTriggerCreateCmd.Flags().Bool("enable", false, "make it enabled (the default)")
	agentsTriggerUpdateCmd.Flags().Bool("enable", false, "enable it")
	agentsTriggerUpdateCmd.Flags().Bool("disable", false, "disable it")
	_ = agentsTriggerCreateCmd.MarkFlagRequired("url")
	_ = agentsTriggerCreateCmd.MarkFlagRequired("events")
	agentsTriggerCmd.AddCommand(all...)
}
