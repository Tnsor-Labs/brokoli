package main

import (
	"fmt"
	"os"

	"github.com/Tnsor-Labs/brokoli/cmd"
)

// Version information injected at build time via goreleaser ldflags:
//   -X main.version={{.Version}} -X main.commit={{.Commit}} -X main.date={{.Date}}
//
// For `go install`-style builds without ldflags, these stay as their
// default values and `brokoli --version` prints "dev".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cmd.SetVersion(version, commit, date)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
