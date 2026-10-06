package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
)

// drivers command group manages locally installed native ADBC driver builds:
// from an operator-supplied archive and its digest, or from the curated
// catalog the operator configured (BROKOLI_DRIVER_INDEX), the same one the
// server's Drivers page installs from.
var driversCmd = &cobra.Command{
	Use:   "drivers",
	Short: "Manage installed native ADBC drivers",
	Long: `Drivers provide native ADBC database connectivity outside the core binary.

Drivers live in ~/.brokoli/drivers/ by default (override with the
BROKOLI_DRIVER_DIR environment variable). Install an archive with its SHA-256
digest, or a release from the curated catalog named by BROKOLI_DRIVER_INDEX.
The catalog is not signed yet, so there is no default one: configuring it is
deciding to trust the native code it lists.`,
}

var driversListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed native ADBC drivers",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := drivers.NewManager(drivers.DefaultDir())
		if err != nil {
			return err
		}
		installed := mgr.List()
		if len(installed) == 0 {
			fmt.Printf("No drivers installed in %s\n", mgr.Dir())
			fmt.Println("Install one with: brokoli drivers install <archive> --sha256 <digest>")
			fmt.Println("                or brokoli drivers install <name> --catalog")
			return nil
		}
		fmt.Printf("Driver directory: %s\n\n", mgr.Dir())
		fmt.Printf("%-20s %-12s %-16s %s\n", "NAME", "VERSION", "PLATFORM", "ENTRYPOINT")
		for _, driver := range installed {
			fmt.Printf("%-20s %-12s %-16s %s\n", driver.Name, driver.Version, driver.OS+"/"+driver.Arch, driver.Entrypoint)
		}
		return nil
	},
}

var driversInstallCmd = &cobra.Command{
	Use:   "install (<archive> --sha256 <digest> | <name> --catalog [--version <v>])",
	Short: "Install a native ADBC driver from an archive or the catalog",
	Long: `Install a native ADBC driver build into the Brokoli driver directory.

From an archive: the archive's SHA-256 digest is required and verified before
extraction.

From the catalog (--catalog): the release is looked up in the catalog named by
BROKOLI_DRIVER_INDEX, downloaded, checked against the digest the catalog lists,
and refused unless its manifest names the same driver and version.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fromCatalog, _ := cmd.Flags().GetBool("catalog")
		if fromCatalog {
			version, _ := cmd.Flags().GetString("version")
			manager, err := drivers.NewManager(drivers.DefaultDir())
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			installed, err := manager.InstallFromCatalogVersion(ctx, args[0], version)
			if err != nil {
				return err
			}
			fmt.Printf("Installed driver %s %s at %s\n", installed.Name, installed.Version, installed.Dir())
			return nil
		}
		digest, err := cmd.Flags().GetString("sha256")
		if err != nil {
			return err
		}
		installed, err := drivers.InstallArchive(args[0], drivers.DefaultDir(), digest)
		if err != nil {
			return err
		}
		fmt.Printf("Installed driver %s %s at %s\n", installed.Name, installed.Version, installed.Dir())
		fmt.Printf("  Library: %s\n", installed.LibraryPath())
		return nil
	},
}

var driversRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"uninstall", "rm"},
	Short:   "Remove an installed native ADBC driver",
	Long: `Remove every installed build of a native ADBC driver.

Unlike removal through the server, this does not check which connections are
pinned to the driver: it has no access to them. Runs using a pinned connection
fail until the build is installed again.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := drivers.NewManager(drivers.DefaultDir())
		if err != nil {
			return err
		}
		if mgr.Get(args[0]) == nil {
			return fmt.Errorf("driver %q is not installed", args[0])
		}
		if err := mgr.Remove(args[0]); err != nil {
			return err
		}
		fmt.Printf("Removed driver %s\n", args[0])
		return nil
	},
}

var driversInspectCmd = &cobra.Command{
	Use:     "inspect <name>",
	Aliases: []string{"show"},
	Short:   "Show an installed native ADBC driver's manifest",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := drivers.NewManager(drivers.DefaultDir())
		if err != nil {
			return err
		}
		driver := mgr.Get(args[0])
		if driver == nil {
			return fmt.Errorf("driver %q is not installed", args[0])
		}
		buf, err := json.MarshalIndent(driver, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		fmt.Printf("\nInstalled at: %s\n", driver.Dir())
		fmt.Printf("Library:      %s\n", driver.LibraryPath())
		return nil
	},
}

func init() {
	driversInstallCmd.Flags().String("sha256", "", "SHA-256 digest of the driver archive")
	driversInstallCmd.Flags().Bool("catalog", false, "install the named release from the catalog in BROKOLI_DRIVER_INDEX")
	driversInstallCmd.Flags().String("version", "", "with --catalog, the exact version (default: newest installable)")
	driversInstallCmd.MarkFlagsMutuallyExclusive("sha256", "catalog")
	driversCmd.AddCommand(driversListCmd)
	driversCmd.AddCommand(driversInstallCmd)
	driversCmd.AddCommand(driversRemoveCmd)
	driversCmd.AddCommand(driversInspectCmd)
	rootCmd.AddCommand(driversCmd)
}
