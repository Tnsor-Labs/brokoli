package cmd

import (
	"fmt"
	"os"

	"github.com/hc12r/broked/engine"
	"github.com/hc12r/broked/store"
	"github.com/spf13/cobra"
)

var importCmd = &cobra.Command{
	Use:   "import <file.yaml>",
	Short: "Import a pipeline from a YAML file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}

		p, err := engine.ImportPipelineYAML(data)
		if err != nil {
			return fmt.Errorf("parse YAML: %w", err)
		}

		s, err := store.NewStore(dbPath)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer s.Close()

		if err := s.CreatePipeline(p); err != nil {
			return fmt.Errorf("create pipeline: %w", err)
		}

		fmt.Printf("Imported pipeline %q (ID: %s)\n", p.Name, p.ID)
		fmt.Printf("  Nodes: %d\n", len(p.Nodes))
		fmt.Printf("  Edges: %d\n", len(p.Edges))
		return nil
	},
}

var exportCmd = &cobra.Command{
	Use:   "export <pipeline-id>",
	Short: "Export a pipeline to YAML",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store.NewStore(dbPath)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer s.Close()

		p, err := s.GetPipeline(args[0])
		if err != nil {
			return fmt.Errorf("get pipeline: %w", err)
		}

		data, err := engine.ExportPipelineYAML(p)
		if err != nil {
			return fmt.Errorf("export YAML: %w", err)
		}

		outFile, _ := cmd.Flags().GetString("output")
		if outFile != "" {
			if err := os.WriteFile(outFile, data, 0o644); err != nil {
				return fmt.Errorf("write file: %w", err)
			}
			fmt.Printf("Exported to %s\n", outFile)
		} else {
			fmt.Print(string(data))
		}
		return nil
	},
}

func init() {
	exportCmd.Flags().StringP("output", "o", "", "Output file (defaults to stdout)")
	rootCmd.AddCommand(importCmd)
	rootCmd.AddCommand(exportCmd)
}
