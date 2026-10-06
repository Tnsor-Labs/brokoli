package cmd

import (
	"errors"
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
		// The parent passes the data and result channels as fds 3 and 4
		// (see engine/native_worker_protocol.go); stdout stays the driver's.
		data, result := os.NewFile(3, "native-data"), os.NewFile(4, "native-result")
		if data == nil || result == nil {
			return errors.New("worker task is started by the server with its data and result channels; it is not run by hand")
		}
		defer result.Close()
		return engine.RunNativeADBCWorker(cmd.Context(), os.Stdin, data, result)
	},
}

func init() {
	workerCmd.AddCommand(workerTaskCmd)
	rootCmd.AddCommand(workerCmd)
}
