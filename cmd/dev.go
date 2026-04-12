package cmd

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var devCmd = &cobra.Command{
	Use:   "dev [directory]",
	Short: "Start development server with file watching and hot-reload",
	Long: `Starts the Brokoli server and watches for pipeline file changes.

When a .yaml or .py file is created or modified in the watched directory,
it is automatically imported or recompiled. The server is started on the
specified port with auto-reload enabled.

Usage:
  brokoli dev                     # watch current directory, port 8080
  brokoli dev ./pipelines         # watch specific directory
  brokoli dev --port 9090         # custom port`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		watchDir := "."
		if len(args) > 0 {
			watchDir = args[0]
		}

		abs, err := filepath.Abs(watchDir)
		if err != nil {
			return fmt.Errorf("resolve path: %w", err)
		}
		watchDir = abs

		devPort, _ := cmd.Flags().GetInt("port")
		devDB, _ := cmd.Flags().GetString("db")

		fmt.Printf("  Brokoli dev mode\n")
		fmt.Printf("  Server:   http://localhost:%d\n", devPort)
		fmt.Printf("  Watching: %s\n", watchDir)
		fmt.Printf("  Database: %s\n", devDB)
		fmt.Println()

		// Import all existing pipeline files on startup.
		initialFiles := scanPipelineFiles(watchDir)
		if len(initialFiles) > 0 {
			fmt.Printf("  Found %d pipeline files, importing...\n", len(initialFiles))
			for _, f := range initialFiles {
				importPipelineFile(f, devDB)
			}
			fmt.Println()
		}

		// Start the server in the background.
		serverArgs := []string{"serve", "--port", fmt.Sprintf("%d", devPort), "--db", devDB}
		serverProc := exec.Command(os.Args[0], serverArgs...)
		serverProc.Stdout = os.Stdout
		serverProc.Stderr = os.Stderr
		if err := serverProc.Start(); err != nil {
			return fmt.Errorf("start server: %w", err)
		}

		// Watch for file changes (simple polling — no fsnotify dependency needed).
		modTimes := make(map[string]time.Time)
		for _, f := range initialFiles {
			if info, err := os.Stat(f); err == nil {
				modTimes[f] = info.ModTime()
			}
		}

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		fmt.Println("  Watching for changes... (Ctrl+C to stop)")

		for range ticker.C {
			files := scanPipelineFiles(watchDir)
			for _, f := range files {
				info, err := os.Stat(f)
				if err != nil {
					continue
				}
				prev, seen := modTimes[f]
				if !seen || info.ModTime().After(prev) {
					modTimes[f] = info.ModTime()
					if seen {
						rel, _ := filepath.Rel(watchDir, f)
						ts := time.Now().Format("15:04:05")
						fmt.Printf("  [%s] Changed: %s", ts, rel)
						if importPipelineFile(f, devDB) {
							fmt.Println(" → reimported")
						} else {
							fmt.Println(" → failed")
						}
					}
				}
			}
		}

		return serverProc.Wait()
	},
}

// scanPipelineFiles finds .yaml and .py files in the directory (non-recursive
// at top level, one level deep for subdirs named "pipelines").
func scanPipelineFiles(dir string) []string {
	var files []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".py") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	// Check one level of subdirectories.
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subEntries, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			if se.IsDir() {
				continue
			}
			name := se.Name()
			if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".py") {
				files = append(files, filepath.Join(dir, e.Name(), name))
			}
		}
	}
	return files
}

// importPipelineFile imports a YAML pipeline file into the local database.
// Python files are compiled via the SDK CLI if available, otherwise skipped.
func importPipelineFile(path, dbPath string) bool {
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".yaml", ".yml":
		return importYAMLFile(path, dbPath)
	case ".py":
		return compilePythonFile(path, dbPath)
	}
	return false
}

func importYAMLFile(path, dbPath string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("    read %s: %v", filepath.Base(path), err)
		return false
	}

	// Use the engine import directly (in-process).
	// We import via the CLI binary to reuse the existing import logic
	// and avoid re-initializing the store on every change.
	cmd := exec.Command(os.Args[0], "import", path, "--db", dbPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = data // read to validate the file exists and is readable
	return cmd.Run() == nil
}

func compilePythonFile(path, dbPath string) bool {
	// Try the brokoli Python SDK CLI first.
	cmd := exec.Command("brokoli", "compile", path)
	output, err := cmd.Output()
	if err != nil {
		// SDK not installed or compile failed — try python3 -m brokoli.
		cmd = exec.Command("python3", "-m", "brokoli", "compile", path)
		output, err = cmd.Output()
		if err != nil {
			log.Printf("    compile %s: SDK not available (install: pip install brokoli)", filepath.Base(path))
			return false
		}
	}

	// The SDK compile command outputs YAML; write to a temp file and import.
	tmpFile := path + ".compiled.yaml"
	if err := os.WriteFile(tmpFile, output, 0o644); err != nil {
		log.Printf("    write compiled yaml: %v", err)
		return false
	}
	defer os.Remove(tmpFile)

	return importYAMLFile(tmpFile, dbPath)
}

func init() {
	devCmd.Flags().Int("port", 8080, "Server port")
	devCmd.Flags().String("db", "brokoli.db", "Database path")
	rootCmd.AddCommand(devCmd)
}
