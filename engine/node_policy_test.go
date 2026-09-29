package engine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func policyTestPipeline(id string, nodes ...models.Node) *models.Pipeline {
	all := append([]models.Node{{ID: "src", Type: models.NodeTypeSourceFile, Name: "Source",
		Config: map[string]interface{}{"path": "/data/in.csv", "format": "csv"}}}, nodes...)
	var edges []models.Edge
	for _, n := range nodes {
		edges = append(edges, models.Edge{From: "src", To: n.ID})
	}
	return &models.Pipeline{ID: id, Name: id, Enabled: true, Nodes: all, Edges: edges,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}

func TestDisabledNodeTypesAreParsed(t *testing.T) {
	t.Setenv(DisabledNodeTypesEnv, " Code, bash,,bash ")
	got := strings.Join(DisabledNodeTypes(), ",")
	if got != "bash,code" {
		t.Fatalf("DisabledNodeTypes() = %q, want bash,code", got)
	}
	t.Setenv(DisabledNodeTypesEnv, "")
	if got := DisabledNodeTypes(); got == nil || len(got) != 0 {
		t.Fatalf("unset: %#v, want an empty list", got)
	}
}

// A disabled node type fails validation, which is what refuses it on save
// and on run; the other node types, and the same pipeline without the
// setting, are unaffected.
func TestDisabledNodeTypesFailValidation(t *testing.T) {
	pipe := policyTestPipeline("disabled",
		models.Node{ID: "sh", Type: models.NodeTypeBash, Name: "Shell", Config: map[string]interface{}{"command": "true"}},
		models.Node{ID: "py", Type: models.NodeTypeCode, Name: "Py", Config: map[string]interface{}{"language": "python", "script": "pass"}},
	)
	t.Setenv(DisabledNodeTypesEnv, "")
	if ve := ValidatePipeline(pipe); strings.Contains(ve.Error(), "disabled on this deployment") {
		t.Fatalf("nothing is disabled, yet: %v", ve)
	}

	t.Setenv(DisabledNodeTypesEnv, "bash")
	ve := ValidatePipeline(pipe)
	if !strings.Contains(ve.Error(), `Node "Shell": bash nodes are disabled on this deployment (BROKOLI_DISABLED_NODE_TYPES)`) {
		t.Fatalf("validation = %v, want the bash node refused by name", ve)
	}
	if strings.Contains(ve.Error(), "code nodes are disabled") {
		t.Fatalf("only bash is disabled: %v", ve)
	}

	detailed := &NodeValidationResult{}
	validateNodeConfigDetailed(pipe.Nodes[1], detailed)
	if len(detailed.Errors) == 0 || !strings.Contains(strings.Join(detailed.Errors, "\n"), "bash nodes are disabled") {
		t.Fatalf("the editor's per-node validation = %v, want the refusal", detailed.Errors)
	}
}

// The worker refuses a disabled type where it would execute, whatever the
// server that validated and dispatched the run was configured with.
func TestDisabledNodeTypeIsRefusedWhereItWouldRun(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	t.Setenv(DisabledNodeTypesEnv, "bash")
	st, err := store.NewSQLiteStore(filepath.Join(dir, "policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := policyTestPipeline("disabled-runtime",
		models.Node{ID: "sh", Type: models.NodeTypeBash, Name: "Shell",
			Config: map[string]interface{}{"command": "touch ran", "working_dir": dir}})
	pipe.Nodes[0].Config["path"] = csv
	if err := st.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	// NewRunner(...).Execute runs without validation, as a worker does.
	run, _ := NewRunner(st, nil, pipe, nil, nil, nil, nil, "", nil).Execute()
	if run == nil || run.Status == models.RunStatusSuccess {
		t.Fatalf("run = %+v, want it failed", run)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Fatal("a disabled bash node ran")
	}
	if !strings.Contains(run.Error, "bash nodes are disabled on this deployment") {
		t.Fatalf("run error = %q, want the refusal", run.Error)
	}
}

// A code node's script keeps its ${param...} references: other references
// still resolve, and every other field still resolves parameters.
func TestCodeScriptDoesNotSubstituteParameters(t *testing.T) {
	vc := NewVariableContext(map[string]string{"name": `"; import os #`}, "run-7", time.Now())
	node := models.Node{Type: models.NodeTypeCode, Config: map[string]interface{}{
		"script": `x = "${param.name}"; y = "${param.name|date:YYYY}"; run = "${run.id}"`,
		"label":  "${param.name}",
	}}
	resolved := resolveNodeConfig(vc, node)
	if got := resolved["script"]; got != `x = "${param.name}"; y = "${param.name|date:YYYY}"; run = "run-7"` {
		t.Fatalf("script = %q", got)
	}
	if resolved["label"] != `"; import os #` {
		t.Fatalf("a non-script field lost its substitution: %q", resolved["label"])
	}

	msg := codeScriptParamError(node.Config)
	if !strings.Contains(msg, "${param.name}, which is not substituted in code") || !strings.Contains(msg, `params["name"]`) {
		t.Fatalf("validation message = %q", msg)
	}
	if msg := codeScriptParamError(map[string]interface{}{"script": `name = params["name"]`}); msg != "" {
		t.Fatalf("a script reading params was refused: %s", msg)
	}
	pipe := policyTestPipeline("code-param", models.Node{ID: "py", Type: models.NodeTypeCode, Name: "Py", Config: node.Config})
	if ve := ValidatePipeline(pipe); !strings.Contains(ve.Error(), "not substituted in code") {
		t.Fatalf("validation = %v, want the reference refused", ve)
	}
}

// Past validation too, a run parameter never becomes part of a code
// node's script: the hostile value below would create a file if it were
// spliced into the Python source.
func TestCodeRunnerDoesNotSpliceParametersIntoTheScript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	t.Setenv("BROKOLI_CODE_POOL", "0")
	st, err := store.NewSQLiteStore(filepath.Join(dir, "code.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pwned := filepath.Join(dir, "pwned")
	pipe := policyTestPipeline("code-unvalidated",
		models.Node{ID: "py", Type: models.NodeTypeCode, Name: "Py", Config: map[string]interface{}{
			"language": "python",
			"script":   "x = \"${param.name}\"\noutput_data = {\"columns\": columns, \"rows\": rows}\n",
		}})
	pipe.Nodes[0].Config["path"] = csv
	pipe.Params = map[string]string{"name": `"; open(` + strconv.Quote(pwned) + `, "w").close() #`}
	if err := st.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner(st, nil, pipe, nil, nil, nil, nil, "", nil).Execute(); err != nil {
		t.Logf("run: %v", err)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("a run parameter was spliced into the script and ran as Python")
	}
}
