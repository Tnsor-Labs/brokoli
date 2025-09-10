package cmd

import (
	"strings"

	"github.com/fatih/color"
)

// GetColoredBoxBanner returns a colorful ASCII art banner for BrokoliSQL
func GetColoredBoxBanner() string {
	// Define colors
	brightGreen := color.New(color.FgGreen, color.Bold).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()
	yellow := color.New(color.FgYellow, color.Bold).SprintFunc()
	magenta := color.New(color.FgMagenta, color.Bold).SprintFunc()

	// Create the banner with colors
	var lines []string

	// Line 1
	lines = append(lines, brightGreen("____             _        _ _ ")+cyan(" ___  ___  _     "))

	// Line 2
	lines = append(lines, brightGreen("|  _ \\           | |      | (_)")+cyan("/ __|/ _ \\| |    "))

	// Line 3
	lines = append(lines, brightGreen("| |_) |_ __ ___  | | _____| |_")+cyan("| (__| | | | |    "))

	// Line 4
	lines = append(lines, brightGreen("|  _ <| '__/ _ \\ | |/ / _ \\ | |")+cyan("\\___ \\ | | | |    "))

	// Line 5
	lines = append(lines, brightGreen("| |_) | | | (_) ||   <  __/ | |")+yellow("____) | |_| | |____"))

	// Line 6
	lines = append(lines, brightGreen("|____/|_|  \\___/ |_|\\_\\___|_|_|")+yellow("_____/ \\__\\_\\______|"))

	// Line 7 - empty line
	lines = append(lines, "")

	// Line 8 - tagline
	lines = append(lines, magenta("   Convert structured data to SQL with ease!"))

	// Join all lines with newlines
	return strings.Join(lines, "\n")
}
