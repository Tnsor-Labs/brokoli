package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/Tnsor-Labs/brokoli/pkg/mcpserver"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve Brokoli to an AI agent over MCP (stdio)",
	Long: `Runs a Model Context Protocol server on standard input and output, for an
agent client on this machine (a desktop assistant, an IDE). It acts on a
Brokoli server as you: the credentials from 'brokoli login', or --server and
--api-key (or BROKOLI_TOKEN). The agent sees only what you may see, and
never a credential.

Configure your client to run:  brokoli mcp
See docs/mcp.md.`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		server, token := resolveAuth(cmd)
		if token == "" {
			token = os.Getenv("BROKOLI_TOKEN")
		}
		workspace, _ := cmd.Flags().GetString("workspace")
		caller := &mcpserver.HTTPCaller{Server: server, Token: token, Workspace: workspace}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		// Say who the agent will act as, on stderr: stdout is the protocol.
		status, data, err := caller.Do(ctx, http.MethodGet, "/api/auth/me", nil)
		switch {
		case err != nil:
			return err
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return fmt.Errorf("not signed in to %s: run 'brokoli login', or pass --api-key", server)
		case status == http.StatusOK:
			var me struct {
				Username string `json:"username"`
			}
			_ = json.Unmarshal(data, &me)
			fmt.Fprintf(os.Stderr, "brokoli mcp: serving %s as %s\n", server, me.Username)
		}
		return mcpserver.Serve(ctx, caller, rootCmd.Version, &mcp.StdioTransport{})
	},
}

func init() {
	mcpCmd.Flags().String("server", "http://localhost:8080", "Brokoli server URL (default: the one 'brokoli login' stored)")
	mcpCmd.Flags().String("api-key", "", "token to act with (default: the one 'brokoli login' stored, then BROKOLI_TOKEN)")
	mcpCmd.Flags().String("workspace", "", "workspace to act in (default: your default workspace)")
	rootCmd.AddCommand(mcpCmd)
}
