package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// The plan gate has to hold on both execution paths. It used to live inside
// runNodeLogic, which the streaming path does not call, so a gated node type
// ran anyway as soon as anything upstream of it produced a reference. Nothing
// about the pipeline said so: the run simply succeeded.
//
// That made enforcement depend on a memory-management decision. A pipeline
// under the streaming threshold was gated and the same pipeline over it was
// not, and adding streaming to any upstream node type silently widened the
// hole — which is how it was found, when source_file learned to stream and a
// two-node pipeline started slipping through.
//
// Both subtests run the same gated sink. Only the upstream node differs, and
// with it whether the sink receives a reference or a materialised dataset.
func TestNodeTypeGateHoldsOnBothExecutionPaths(t *testing.T) {
	const blockMsg = "sink_file is not available on your plan"

	for _, tc := range []struct {
		name      string
		streaming bool
	}{
		{"materialising path", false},
		{"streaming path", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, s := newExecCtxTestEngine(t)
			eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
			if tc.streaming {
				eng.SpillThresholdBytes = 1
				eng.StreamThresholdBytes = 1
			} else {
				eng.StreamThresholdBytes = -1
			}

			setNodeTypeGate(t, func(orgID, nodeType string) string {
				if nodeType == string(models.NodeTypeSinkFile) {
					return blockMsg
				}
				return ""
			})

			out := filepath.Join(t.TempDir(), "gated.csv")
			pipeline := &models.Pipeline{
				ID: "p-gate-" + tc.name, Name: "Gate on both paths", Enabled: true,
				Nodes: []models.Node{
					{ID: "gen", Type: models.NodeTypeCode, Name: "Generate",
						Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
						Config: map[string]interface{}{"script": `
output_data = {"columns": ["id"], "rows": [{"id": i} for i in range(2000)]}
`}},
					{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink",
						Config: map[string]interface{}{"path": out, "format": "csv"}},
				},
				Edges: []models.Edge{{From: "gen", To: "sink"}},
			}
			if err := s.CreatePipeline(pipeline); err != nil {
				t.Fatal(err)
			}

			_, err := eng.RunPipeline(pipeline.ID)
			if err == nil {
				t.Fatalf("the gate did not fire on the %s: the run succeeded and the gated sink executed", tc.name)
			}
			if !strings.Contains(err.Error(), blockMsg) {
				t.Errorf("err = %q, want the gate's message %q", err.Error(), blockMsg)
			}
		})
	}
}
