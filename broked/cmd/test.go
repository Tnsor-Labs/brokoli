package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hc12r/broked/engine"
	"github.com/spf13/cobra"
)

var testCmd = &cobra.Command{
	Use:   "test <pipeline.yaml>",
	Short: "Validate a pipeline YAML file for CI/CD",
	Long:  `Parses and validates a pipeline YAML file. Exits 0 on success, 1 on failure.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
			os.Exit(1)
		}

		p, err := engine.ImportPipelineYAML(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: invalid YAML: %v\n", err)
			os.Exit(1)
		}

		ve := engine.ValidatePipeline(p)

		jsonOut, _ := cmd.Flags().GetBool("json")
		if jsonOut {
			result := map[string]interface{}{
				"valid":    !ve.HasErrors(),
				"pipeline": p.Name,
				"nodes":    len(p.Nodes),
				"edges":    len(p.Edges),
				"errors":   ve.Errors,
			}
			json.NewEncoder(os.Stdout).Encode(result)
			if ve.HasErrors() {
				os.Exit(1)
			}
			return nil
		}

		fmt.Printf("Pipeline: %s\n", p.Name)
		fmt.Printf("  Nodes:  %d\n", len(p.Nodes))
		fmt.Printf("  Edges:  %d\n", len(p.Edges))

		if ve.HasErrors() {
			fmt.Fprintf(os.Stderr, "\nValidation FAILED:\n")
			for _, e := range ve.Errors {
				fmt.Fprintf(os.Stderr, "  - %s\n", e)
			}
			os.Exit(1)
		}

		fmt.Println("\nValidation PASSED")
		return nil
	},
}

func init() {
	testCmd.Flags().Bool("json", false, "Output results as JSON")
	rootCmd.AddCommand(testCmd)
}
