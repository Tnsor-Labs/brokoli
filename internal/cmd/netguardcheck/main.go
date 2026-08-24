package main

import (
	"github.com/Tnsor-Labs/brokoli/internal/analyzers/netguard"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(netguard.Analyzer)
}
