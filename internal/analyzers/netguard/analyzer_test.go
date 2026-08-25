package netguard

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(
		t,
		analysistest.TestData(),
		Analyzer,
		"clientconstruction",
		"packagealias",
		"exceptions",
		"example.com/brokoli/pkg/netguard",
		"example.com/brokoli/cmd",
	)
}
