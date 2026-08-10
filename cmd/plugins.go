package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
)

// plugins command group — lists, installs, inspects, tests, and
// removes plugins. Not a daemon operation; all commands act on the
// on-disk plugin directory directly and don't require a running
// Brokoli server.

var pluginsCmd = &cobra.Command{
	Use:   "plugins",
	Short: "Manage Brokoli plugins (connectors and custom node types)",
	Long: `Plugins extend Brokoli with new source/sink/transform node types
from outside the core binary. Each plugin is an executable that speaks
the Brokoli plugin protocol over stdin/stdout; the host launches it
when a pipeline node with that type runs.

Plugins live in ~/.brokoli/plugins/ by default (override with the
BROKOLI_PLUGIN_DIR environment variable). Each plugin is its own
subdirectory containing manifest.json + an executable.`,
}

var pluginsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed plugins and the node types they register",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := plugins.NewManager(plugins.DefaultDir())
		if err != nil {
			return err
		}
		installed := mgr.List()
		if len(installed) == 0 {
			fmt.Printf("No plugins installed in %s\n", mgr.Dir())
			fmt.Println("Install one with: brokoli plugins install <path-or-url>")
			return nil
		}
		fmt.Printf("Plugin directory: %s\n\n", mgr.Dir())
		fmt.Printf("%-20s %-12s %s\n", "NAME", "VERSION", "NODE TYPES")
		for _, p := range installed {
			types := make([]string, 0, len(p.NodeTypes))
			for _, nt := range p.NodeTypes {
				types = append(types, nt.Type)
			}
			fmt.Printf("%-20s %-12s %s\n", p.Name, p.Version, join(types, ", "))
		}
		return nil
	},
}

var pluginsInstallCmd = &cobra.Command{
	Use:   "install <source>",
	Short: "Install a plugin from a package archive (.bkg) or local directory",
	Long: `Install a plugin into the Brokoli plugin directory.

Two source shapes are supported:
  - a package archive (.bkg — see ADR-016): a gzipped tar carrying a
    manifest with per-platform/runtime payloads. Installation selects
    the first payload feasible on this host (native os/arch match, or an
    interpreter satisfying the payload's requirement), verifies its
    integrity hash, and runs the plugin's own 'spec' as a drift check —
    all before anything lands in the plugin directory.
  - a local plugin directory (manifest.json + executable), copied as-is.

Future releases will add installation from a hosted plugin index and
GitHub releases (see issue #110).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		src := args[0]
		srcInfo, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("source %q: %w", src, err)
		}
		if !srcInfo.IsDir() {
			dir := plugins.DefaultDir()
			installed, err := plugins.InstallArchive(src, dir)
			if err != nil {
				return err
			}
			printInstalled(installed)
			return nil
		}
		// Load the source's manifest to learn the plugin name, then
		// copy the directory into ~/.brokoli/plugins/<name>.
		man, err := plugins.LoadManifest(src)
		if err != nil {
			return fmt.Errorf("source %q is not a valid plugin: %w", src, err)
		}
		dir := plugins.DefaultDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create plugin dir %s: %w", dir, err)
		}
		dst := filepath.Join(dir, man.Name)
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("plugin %q is already installed at %s — remove it first with 'brokoli plugins remove %s'",
				man.Name, dst, man.Name)
		}
		if err := copyTree(src, dst); err != nil {
			return fmt.Errorf("copy plugin: %w", err)
		}
		// Verify the freshly-copied manifest loads cleanly, and that the
		// plugin's own spec agrees with it (the drift check package
		// installs get; directory installs deserve it too).
		copied, err := plugins.LoadManifest(dst)
		if err != nil {
			_ = os.RemoveAll(dst)
			return fmt.Errorf("installed plugin failed to load: %w", err)
		}
		if err := plugins.VerifySpec(copied); err != nil {
			_ = os.RemoveAll(dst)
			return err
		}
		printInstalled(copied)
		return nil
	},
}

func printInstalled(m *plugins.Manifest) {
	fmt.Printf("Installed plugin %s %s at %s\n", m.Name, m.Version, m.Dir())
	fmt.Printf("  Registers node types: ")
	for i, nt := range m.NodeTypes {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Print(nt.Type)
	}
	fmt.Println()
}

var pluginsRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"uninstall", "rm"},
	Short:   "Remove an installed plugin",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := plugins.NewManager(plugins.DefaultDir())
		if err != nil {
			return err
		}
		if mgr.Get(args[0]) == nil {
			return fmt.Errorf("plugin %q is not installed", args[0])
		}
		if err := mgr.Remove(args[0]); err != nil {
			return err
		}
		fmt.Printf("Removed plugin %s\n", args[0])
		return nil
	},
}

var pluginsInspectCmd = &cobra.Command{
	Use:     "inspect <name>",
	Aliases: []string{"show"},
	Short:   "Show an installed plugin's manifest",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := plugins.NewManager(plugins.DefaultDir())
		if err != nil {
			return err
		}
		man := mgr.Get(args[0])
		if man == nil {
			return fmt.Errorf("plugin %q is not installed", args[0])
		}
		buf, err := json.MarshalIndent(man, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		fmt.Printf("\nInstalled at: %s\n", man.Dir())
		fmt.Printf("Binary:       %s\n", man.BinaryPath())
		return nil
	},
}

var pluginsTestCmd = &cobra.Command{
	Use:   "test <name>",
	Short: "Run the plugin's check command against an empty config (smoke test)",
	Long: `Launches the plugin with its check subcommand, passing an empty
config. Useful for verifying that an installed plugin is executable and
its protocol handshake works end-to-end. Real connection validation
requires a real config, which the UI's "Test Connection" button
provides.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := plugins.NewManager(plugins.DefaultDir())
		if err != nil {
			return err
		}
		man := mgr.Get(args[0])
		if man == nil {
			return fmt.Errorf("plugin %q is not installed", args[0])
		}
		runner := plugins.NewRunner(man, 30*time.Second)
		runner.LogHandler = func(level plugins.LogLevel, msg string) {
			fmt.Fprintf(os.Stderr, "  [%s] %s\n", level, msg)
		}
		if err := runner.Check(context.Background(), plugins.Config{}); err != nil {
			return fmt.Errorf("check failed: %w", err)
		}
		fmt.Printf("Plugin %s: ok\n", man.Name)
		return nil
	},
}

func init() {
	pluginsCmd.AddCommand(pluginsListCmd)
	pluginsCmd.AddCommand(pluginsInstallCmd)
	pluginsCmd.AddCommand(pluginsRemoveCmd)
	pluginsCmd.AddCommand(pluginsInspectCmd)
	pluginsCmd.AddCommand(pluginsTestCmd)
	rootCmd.AddCommand(pluginsCmd)
}

// ─── helpers ──────────────────────────────────────────────────────

// copyTree recursively copies a directory. Used at install time;
// intentionally simple — no symlinks, no permission preservation
// beyond the executable bit on files that were executable in src.
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyTree(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		info, err := os.Stat(srcPath)
		if err != nil {
			return err
		}
		in, err := os.Open(srcPath)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			out.Close()
			return err
		}
		in.Close()
		out.Close()
	}
	return nil
}

func join(s []string, sep string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += sep
		}
		out += v
	}
	return out
}
