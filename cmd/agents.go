package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tnsor-Labs/brokoli/pkg/mcpserver"
)

// brokoli agents: agent identities (ADR-046), managed from the command line.

var agentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "Manage agent identities: accounts an AI agent acts as, with their own role and tokens",
	Long: `An agent is an account for an AI agent: it has a role (by default "agent":
see pipelines, runs and lineage, and run pipelines), an owner who answers for
it, and tokens of its own that expire and can be revoked one by one. It never
reads secrets, and never has more permissions than its owner.

Give an agent its token instead of your own credentials or the server's
static API key. See docs/agents.md.`,
}

type agentRecord struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Role        string     `json:"role"`
	OwnerName   string     `json:"owner_name"`
	Disabled    bool       `json:"disabled"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

type tokenRecord struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	OnBehalfOf bool       `json:"on_behalf_of_owner"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// agentsCall makes one agents API request with the stored credentials.
func agentsCall(cmd *cobra.Command, method, path string, body, out any) error {
	server, token := resolveAuth(cmd)
	if token == "" {
		token = os.Getenv("BROKOLI_TOKEN")
	}
	c := &mcpserver.HTTPCaller{Server: server, Token: token}
	status, data, err := c.Do(context.Background(), method, path, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		if status == http.StatusUnauthorized {
			return fmt.Errorf("not signed in to %s: run 'brokoli login'", server)
		}
		return fmt.Errorf("%s", e.Error)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// resolveAgent finds an agent by id or name among those the caller sees.
func resolveAgent(cmd *cobra.Command, ref string) (*agentRecord, error) {
	var agents []agentRecord
	if err := agentsCall(cmd, http.MethodGet, "/api/agents", nil, &agents); err != nil {
		return nil, err
	}
	for i := range agents {
		if agents[i].ID == ref || agents[i].Name == ref {
			return &agents[i], nil
		}
	}
	return nil, fmt.Errorf("no agent %q that you can see", ref)
}

func when(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

var agentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the agents you own (every agent, for an administrator)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		var agents []agentRecord
		if err := agentsCall(cmd, http.MethodGet, "/api/agents", nil, &agents); err != nil {
			return err
		}
		if len(agents) == 0 {
			fmt.Println("No agents. Create one with: brokoli agents create <name>")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tROLE\tOWNER\tLAST USED\tSTATE")
		for _, a := range agents {
			state := "enabled"
			if a.Disabled {
				state = "disabled"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.Role, a.OwnerName, when(a.LastUsedAt), state)
		}
		return tw.Flush()
	},
}

var agentsCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create an agent (needs the right to manage users)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		role, _ := cmd.Flags().GetString("role")
		description, _ := cmd.Flags().GetString("description")
		owner, _ := cmd.Flags().GetString("owner")
		var a agentRecord
		if err := agentsCall(cmd, http.MethodPost, "/api/agents",
			map[string]string{"name": args[0], "role": role, "description": description, "owner_id": owner}, &a); err != nil {
			return err
		}
		fmt.Printf("Created agent %s (role %s, owned by %s).\nIssue it a token with: brokoli agents token create %s --name <where it runs>\n",
			a.Name, a.Role, a.OwnerName, a.Name)
		return nil
	},
}

var agentsDisableCmd = &cobra.Command{
	Use:   "disable <agent>",
	Short: "Disable an agent and revoke all its tokens; what it did stays attributed to it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		if err := agentsCall(cmd, http.MethodDelete, "/api/agents/"+url.PathEscape(a.ID), nil, nil); err != nil {
			return err
		}
		fmt.Printf("Disabled %s; its tokens no longer work.\n", a.Name)
		return nil
	},
}

var agentsTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Issue, list and revoke an agent's tokens",
}

var agentsTokenCreateCmd = &cobra.Command{
	Use:   "create <agent>",
	Short: "Issue a token; it is printed once",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		days, _ := cmd.Flags().GetInt("days")
		behalf, _ := cmd.Flags().GetBool("on-behalf")
		var res struct {
			Token  string      `json:"token"`
			Record tokenRecord `json:"record"`
		}
		if err := agentsCall(cmd, http.MethodPost, "/api/agents/"+url.PathEscape(a.ID)+"/tokens",
			map[string]any{"name": name, "expires_in_days": days, "on_behalf_of_owner": behalf}, &res); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Token %q for %s, valid until %s. It is shown only now:\n", res.Record.Name, a.Name,
			res.Record.ExpiresAt.Local().Format("2006-01-02"))
		fmt.Println(res.Token)
		return nil
	},
}

var agentsTokenListCmd = &cobra.Command{
	Use:   "list <agent>",
	Short: "List an agent's tokens (never the tokens themselves)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		var tokens []tokenRecord
		if err := agentsCall(cmd, http.MethodGet, "/api/agents/"+url.PathEscape(a.ID)+"/tokens", nil, &tokens); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tSTARTS\tEXPIRES\tLAST USED\tSTATE")
		for _, t := range tokens {
			state := "active"
			switch {
			case t.RevokedAt != nil:
				state = "revoked"
			case time.Now().After(t.ExpiresAt):
				state = "expired"
			}
			if t.OnBehalfOf {
				state += ", on behalf of the owner"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s...\t%s\t%s\t%s\n", t.ID, t.Name, t.Prefix, t.ExpiresAt.Local().Format("2006-01-02"), when(t.LastUsedAt), state)
		}
		return tw.Flush()
	},
}

var agentsTokenRevokeCmd = &cobra.Command{
	Use:   "revoke <agent> <token-id>",
	Short: "Revoke one of an agent's tokens",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := resolveAgent(cmd, args[0])
		if err != nil {
			return err
		}
		if err := agentsCall(cmd, http.MethodDelete, "/api/agents/"+url.PathEscape(a.ID)+"/tokens/"+url.PathEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Println("Revoked.")
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{agentsListCmd, agentsCreateCmd, agentsDisableCmd, agentsTokenCreateCmd, agentsTokenListCmd, agentsTokenRevokeCmd} {
		c.Flags().String("server", "http://localhost:8080", "Brokoli server URL (default: the one 'brokoli login' stored)")
		c.Flags().String("api-key", "", "credential to act with (default: the one 'brokoli login' stored)")
	}
	agentsCreateCmd.Flags().String("role", "agent", "the agent's role (any but admin)")
	agentsCreateCmd.Flags().String("description", "", "what the agent is for")
	agentsCreateCmd.Flags().String("owner", "", "the owner's user id (default: you)")
	agentsTokenCreateCmd.Flags().String("name", "", "where the token is used, to recognize it later (required)")
	agentsTokenCreateCmd.Flags().Int("days", 90, "days until it expires, at most 365")
	agentsTokenCreateCmd.Flags().Bool("on-behalf", false, "the agent acts on your behalf with this token (you must own it)")
	_ = agentsTokenCreateCmd.MarkFlagRequired("name")
	agentsTokenCmd.AddCommand(agentsTokenCreateCmd, agentsTokenListCmd, agentsTokenRevokeCmd)
	agentsCmd.AddCommand(agentsListCmd, agentsCreateCmd, agentsDisableCmd, agentsTokenCmd)
	rootCmd.AddCommand(agentsCmd)
}
