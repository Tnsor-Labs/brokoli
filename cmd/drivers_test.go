package cmd

import (
	"strings"
	"testing"
)

func TestDriversInstallRequiresSHA256(t *testing.T) {
	driversInstallCmd.Flags().Set("sha256", "")
	t.Cleanup(func() { driversInstallCmd.Flags().Set("sha256", "") })

	err := driversInstallCmd.RunE(driversInstallCmd, []string{"driver.tar.gz"})
	if err == nil || !strings.Contains(err.Error(), "caller must supply an archive SHA-256 digest") {
		t.Fatalf("install without digest error = %v", err)
	}
}

func TestDriversOnlyExposeLocalManagementCommands(t *testing.T) {
	want := map[string]bool{"list": true, "install": true, "remove": true, "inspect": true}
	for _, command := range driversCmd.Commands() {
		if !want[command.Name()] {
			t.Fatalf("unexpected drivers command %q", command.Name())
		}
		delete(want, command.Name())
	}
	if len(want) != 0 {
		t.Fatalf("missing drivers commands: %v", want)
	}
}
