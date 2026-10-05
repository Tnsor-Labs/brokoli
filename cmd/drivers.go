package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
)

// drivers command group manages locally installed native ADBC driver files.
// It intentionally accepts only operator-supplied archives: a network catalog
// install path must wait for a curated, signed catalog.
var driversCmd = &cobra.Command{
	Use:   "drivers",
	Short: "Manage installed native ADBC drivers",
	Long: `Drivers provide native ADBC database connectivity outside the core binary.

Drivers live in ~/.brokoli/drivers/ by default (override with the
BROKOLI_DRIVER_DIR environment variable). Install archives explicitly with a
SHA-256 digest; catalog installation is not available.`,
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
	Use:   "install <archive> --sha256 <digest>",
	Short: "Install a native ADBC driver from a local archive",
	Long: `Install a native ADBC driver archive into the Brokoli driver directory.

The archive SHA-256 digest is required and verified before extraction. Network
or catalog installation is intentionally unavailable until a curated, signed
catalog exists.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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
	Args:    cobra.ExactArgs(1),
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
	_ = driversInstallCmd.MarkFlagRequired("sha256")
	driversCmd.AddCommand(driversListCmd)
	driversCmd.AddCommand(driversInstallCmd)
	driversCmd.AddCommand(driversRemoveCmd)
	driversCmd.AddCommand(driversInspectCmd)
	rootCmd.AddCommand(driversCmd)
}
