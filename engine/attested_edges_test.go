package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Attested column edges (ADR-039): an identity edge is promoted from
// declared to attested only when the run the profile came from proves the
// bytes did not change -- one input, a digest on both sides, and the two
// equal. Every case that falls short of that must stay declared, because
// an attested edge claims a fact, and a wrong fact is worse than an
// honest declaration.

func attestationGraph(t *testing.T, qcNode models.Node, qcInputs []models.Edge, prov *models.NodeProvenance) *LineageGraph {
	t.Helper()
	nodes := []models.Node{
		{ID: "src", Type: models.NodeTypeSourceFile, Config: map[string]interface{}{"path": "/in.csv"}},
		{ID: "src2", Type: models.NodeTypeSourceFile, Config: map[string]interface{}{"path": "/in2.csv"}},
		qcNode,
		{ID: "dst", Type: models.NodeTypeSinkFile, Config: map[string]interface{}{"path": "/out.csv"}},
	}
	edges := append(append([]models.Edge{}, qcInputs...), models.Edge{From: qcNode.ID, To: "dst"})
	profiles := map[string]LineageProfile{
		profileKey("p1", "src"):  profileWith("id", "amount"),
		profileKey("p1", "src2"): profileWith("id", "amount"),
	}
	qcProfile := profileWith("id", "amount")
	qcProfile.Provenance = prov
	profiles[profileKey("p1", qcNode.ID)] = qcProfile
	return BuildLineageGraphWithProfiles([]models.Pipeline{{ID: "p1", Name: "p1", Nodes: nodes, Edges: edges}}, profiles)
}

// edgesInto returns the evidence of every column edge into the node.
func edgesInto(g *LineageGraph, nodeSuffix string) map[string]LineageColumnEdge {
	out := map[string]LineageColumnEdge{}
	for _, e := range g.ColumnEdges {
		if strings.HasSuffix(e.To, nodeSuffix) {
			out[e.FromColumn+">"+e.ToColumn] = e
		}
	}
	return out
}

func record(inputs []models.DatasetFact, out string) *models.NodeProvenance {
	return &models.NodeProvenance{RunID: "run-1", NodeID: "qc", Inputs: inputs, Output: &models.DatasetFact{Digest: out}}
}

var qualityCheck = models.Node{ID: "qc", Type: models.NodeTypeQualityCheck}

func TestEqualDigestsAttestAPassThrough(t *testing.T) {
	g := attestationGraph(t, qualityCheck, []models.Edge{{From: "src", To: "qc"}},
		record([]models.DatasetFact{{Node: "src", Digest: "sha256:a"}}, "sha256:a"))

	edges := edgesInto(g, ":qc")
	if len(edges) != 2 {
		t.Fatalf("edges into qc = %v, want id and amount", edges)
	}
	for k, e := range edges {
		if e.Evidence != EvidenceAttested {
			t.Errorf("%s: evidence = %q, want attested", k, e.Evidence)
		}
		if !strings.Contains(e.MappingReason, "run-1") {
			t.Errorf("%s: reason %q does not name the run that proved it", k, e.MappingReason)
		}
	}
}

// Every way to fall short of proof stays declared.
func TestAnythingShortOfProofStaysDeclared(t *testing.T) {
	for _, tc := range []struct {
		name   string
		node   models.Node
		inputs []models.Edge
		prov   *models.NodeProvenance
	}{
		{"no provenance at all", qualityCheck, []models.Edge{{From: "src", To: "qc"}}, nil},
		{"digests differ", qualityCheck, []models.Edge{{From: "src", To: "qc"}},
			record([]models.DatasetFact{{Node: "src", Digest: "sha256:a"}}, "sha256:b")},
		// An absent digest means the dataset was not stored. "" == "" must
		// not read as identical bytes.
		{"neither side stored", qualityCheck, []models.Edge{{From: "src", To: "qc"}},
			record([]models.DatasetFact{{Node: "src"}}, "")},
		{"only the input stored", qualityCheck, []models.Edge{{From: "src", To: "qc"}},
			record([]models.DatasetFact{{Node: "src", Digest: "sha256:a"}}, "")},
		// With two inputs, an output matching one says nothing about the
		// other.
		// A join's left columns each come from one source, so this is the
		// case only the single-input rule refuses; a union's would also be
		// refused by the identity rule and could hide its absence.
		{"two inputs, a join", models.Node{ID: "qc", Type: models.NodeTypeJoin,
			Config: map[string]interface{}{"left_key": "id"}},
			[]models.Edge{{From: "src", To: "qc"}, {From: "src2", To: "qc"}},
			record([]models.DatasetFact{{Node: "src", Digest: "sha256:a"}, {Node: "src2", Digest: "sha256:c"}}, "sha256:a")},
		{"two inputs, a union", models.Node{ID: "qc", Type: models.NodeTypeUnion},
			[]models.Edge{{From: "src", To: "qc"}, {From: "src2", To: "qc"}},
			record([]models.DatasetFact{{Node: "src", Digest: "sha256:a"}, {Node: "src2", Digest: "sha256:c"}}, "sha256:a")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := attestationGraph(t, tc.node, tc.inputs, tc.prov)
			edges := edgesInto(g, ":qc")
			if len(edges) == 0 {
				t.Fatal("no column edges into qc at all; the case is not exercising promotion")
			}
			for k, e := range edges {
				if e.Evidence == EvidenceAttested {
					t.Errorf("%s was attested without proof", k)
				}
			}
		})
	}
}

// Only identity edges are promoted. A derived column is not the input's
// column passed through, whatever the digests say.
func TestARenamedColumnIsNeverAttested(t *testing.T) {
	// One source column, so nothing but the identity rule stands between
	// this edge and promotion. Contrived, like the case below: a real
	// rename changes the header, so the digests could not match. The rule
	// is still what keeps an attested edge meaning "the same column,
	// untouched", whatever a record claims.
	tf := models.Node{ID: "qc", Type: models.NodeTypeTransform, Config: map[string]interface{}{
		"rules": []interface{}{map[string]interface{}{"type": "rename", "mapping": map[string]interface{}{"amount": "total"}}},
	}}
	g := attestationGraph(t, tf, []models.Edge{{From: "src", To: "qc"}},
		record([]models.DatasetFact{{Node: "src", Digest: "sha256:a"}}, "sha256:a"))

	var saw bool
	for k, e := range edgesInto(g, ":qc") {
		if e.ToColumn == "total" {
			saw = true
			if e.Evidence == EvidenceAttested {
				t.Errorf("%s: a renamed column was attested", k)
			}
		}
	}
	if !saw {
		t.Fatal("no edge into the renamed column; the case is not exercising the rule")
	}
}

func TestADerivedColumnIsNeverAttested(t *testing.T) {
	tf := models.Node{ID: "qc", Type: models.NodeTypeTransform, Config: map[string]interface{}{
		"rules": []interface{}{map[string]interface{}{"type": "add_column", "name": "total", "expression": "amount * id"}},
	}}
	// Contrived: equal digests on a node that declares a derived column.
	// The digest claim is about bytes; the edge claim is about columns, and
	// only an identity edge is what equal bytes prove.
	g := attestationGraph(t, tf, []models.Edge{{From: "src", To: "qc"}},
		record([]models.DatasetFact{{Node: "src", Digest: "sha256:a"}}, "sha256:a"))

	for k, e := range edgesInto(g, ":qc") {
		if e.ToColumn == "total" && e.Evidence == EvidenceAttested {
			t.Errorf("%s: a derived column was attested", k)
		}
	}
}
