package cmd

import (
	"os"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/spf13/cobra"
)

// workerCmd owns internal worker entry points. The public worker process is
// still started through `serve --mode worker`; `task` is a one-shot child used
// to isolate optional native libraries from the parent worker.
var workerCmd = &cobra.Command{
	Use:    "worker",
	Hidden: true,
}

var workerTaskCmd = &cobra.Command{
	Use:    "task",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return engine.RunNativeFlightSQLWorker(cmd.Context(), os.Stdin, os.Stdout)
	},
}

func init() {
	workerCmd.AddCommand(workerTaskCmd)
	rootCmd.AddCommand(workerCmd)
}
