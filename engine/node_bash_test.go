package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

func bashTestNode(command, workDir string) models.Node {
	config := map[string]interface{}{"command": command}
	if workDir != "" {
		config["working_dir"] = workDir
	}
	return models.Node{ID: "bash", Config: config}
}

func bashLogMessages(t *testing.T, r *Runner) []string {
	t.Helper()
	entries, err := r.store.GetLogs(r.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Message)
	}
	return out
}

func requireBash(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the bash node's process-group guarantees are Unix-only")
	}
}

func TestRunBashPassesInputAndUsesConfiguredEnvironment(t *testing.T) {
	requireBash(t)
	workDir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", workDir)
	r := newUnitTestRunner(t)
	input := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": 1}}}
	node := bashTestNode(`test "$TEST_VALUE" = "ok"; test -n "$BROKOLI_RUN_ID"; printf 'stdout\n'; printf 'stderr\n' >&2; printf 'no newline'`, workDir)
	node.Config["env"] = map[string]interface{}{"TEST_VALUE": "ok"}

	output, err := r.runBash(context.Background(), node, input)
	if err != nil {
		t.Fatalf("runBash: %v", err)
	}
	if output != input {
		t.Fatal("bash operator did not pass the input dataset through unchanged")
	}
	logs := strings.Join(bashLogMessages(t, r), "\n")
	for _, want := range []string{"[bash] stdout", "[bash stderr] stderr", "[bash] no newline"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the run log is missing %q:\n%s", want, logs)
		}
	}
}

func TestRunBashFailureCarriesTheStderrTail(t *testing.T) {
	requireBash(t)
	workDir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", workDir)
	_, err := (&Runner{}).runBash(context.Background(), bashTestNode("echo 'the real reason' >&2; exit 7", workDir), nil)
	if err == nil || !strings.Contains(err.Error(), "bash command failed") || !strings.Contains(err.Error(), "the real reason") {
		t.Fatalf("runBash error = %v, want the failure with its stderr", err)
	}
}

// Cancelling the node (a timeout, a user cancel) stops everything the
// command started, not only bash: a child left running would keep
// writing after the node is reported failed, alongside any retry.
func TestRunBashCancelStopsTheWholeProcessTree(t *testing.T) {
	requireBash(t)
	workDir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", workDir)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&Runner{}).runBash(ctx, bashTestNode("bash -c 'sleep 1; touch leaked'; echo after", workDir), nil)
	if err == nil {
		t.Fatal("runBash succeeded after its context was cancelled")
	}
	if elapsed := time.Since(start); elapsed > bashTermGrace {
		t.Fatalf("cancellation took %v", elapsed)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workDir, "leaked")); err == nil {
		t.Fatal("a child of the cancelled command kept running and wrote its file")
	}
}

// A process the command leaves in the background does not outlive the
// node, on success either.
func TestRunBashBackgroundProcessesDoNotOutliveTheNode(t *testing.T) {
	requireBash(t)
	workDir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", workDir)
	if _, err := (&Runner{}).runBash(context.Background(), bashTestNode("(sleep 1; touch leaked) >/dev/null 2>&1 & echo started", workDir), nil); err != nil {
		t.Fatalf("runBash: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workDir, "leaked")); err == nil {
		t.Fatal("a background process outlived the node")
	}
}

// A line longer than any buffer used to stop the reader, and the command,
// blocked writing into a pipe nobody read, never finished.
func TestRunBashLongLinesDoNotHang(t *testing.T) {
	requireBash(t)
	workDir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", workDir)
	r := newUnitTestRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := r.runBash(ctx, bashTestNode(`head -c 3000000 /dev/zero | tr '\0' a; echo; echo tail`, workDir), nil); err != nil {
		t.Fatalf("runBash: %v", err)
	}
	logs := bashLogMessages(t, r)
	var long, tail bool
	for _, line := range logs {
		if strings.HasSuffix(line, "[line truncated]") && len(line) < bashMaxLineBytes+100 {
			long = true
		}
		if line == "[bash] tail" {
			tail = true
		}
	}
	if !long || !tail {
		t.Fatalf("want the long line truncated and the following line logged; truncated=%v tail=%v", long, tail)
	}
}

func TestRunBashLogIsBounded(t *testing.T) {
	requireBash(t)
	workDir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", workDir)
	prev := bashMaxLogLines
	bashMaxLogLines = 5
	t.Cleanup(func() { bashMaxLogLines = prev })
	r := newUnitTestRunner(t)
	if _, err := r.runBash(context.Background(), bashTestNode("seq 1 50", workDir), nil); err != nil {
		t.Fatalf("runBash: %v", err)
	}
	logs := bashLogMessages(t, r)
	if len(logs) != 6 {
		t.Fatalf("want 5 output lines and one notice, got %d: %v", len(logs), logs)
	}
	if !strings.Contains(logs[5], "45 further output lines were not logged") {
		t.Fatalf("notice = %q", logs[5])
	}
}

// Without working_dir the command runs in a private directory that is
// removed afterwards -- and that works when BROKOLI_DATA_DIRS does not
// include the system temp directory, which the shared default did not.
func TestRunBashDefaultWorkingDirectoryIsPrivate(t *testing.T) {
	requireBash(t)
	allowed := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", allowed)
	record := filepath.Join(allowed, "pwd")
	node := bashTestNode(`pwd > "$RECORD"`, "")
	node.Config["env"] = map[string]interface{}{"RECORD": record}
	if _, err := (&Runner{}).runBash(context.Background(), node, nil); err != nil {
		t.Fatalf("runBash: %v", err)
	}
	dir, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimSpace(string(dir))); !os.IsNotExist(err) {
		t.Fatalf("the default working directory %s was left behind (stat err %v)", dir, err)
	}
}

func TestRunBashRefusesWorkingDirOutsideDataDirs(t *testing.T) {
	t.Setenv("BROKOLI_DATA_DIRS", t.TempDir())
	_, err := (&Runner{}).runBash(context.Background(), bashTestNode("true", "/etc"), nil)
	if err == nil || !strings.Contains(err.Error(), "outside allowed directories") {
		t.Fatalf("err = %v, want the data-directory refusal", err)
	}
}

func TestRunBashNamesTheCPULimit(t *testing.T) {
	requireBash(t)
	if runtime.GOOS != "linux" {
		t.Skip("rlimits are applied on Linux")
	}
	workDir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", workDir)
	node := bashTestNode("while :; do :; done", workDir)
	node.Config["max_cpu_seconds"] = float64(1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := (&Runner{}).runBash(ctx, node, nil)
	if err == nil || !strings.Contains(err.Error(), "CPU limit") {
		t.Fatalf("err = %v, want the CPU limit named", err)
	}
}

func TestBashConfigErrors(t *testing.T) {
	if got := bashConfigErrors(map[string]interface{}{}); len(got) != 1 || got[0] != "'command' is required" {
		t.Fatalf("bashConfigErrors(empty) = %v", got)
	}
	if got := bashConfigErrors(map[string]interface{}{"command": "true", "env": "bad"}); len(got) != 1 || got[0] != "'env' must be an object of string values" {
		t.Fatalf("bashConfigErrors(env) = %v", got)
	}
	for _, name := range []string{"MY-VAR", "1ST", "A=B", ""} {
		got := bashConfigErrors(map[string]interface{}{"command": "true", "env": map[string]interface{}{name: "x"}})
		if len(got) != 1 || !strings.Contains(got[0], "not a valid shell variable name") {
			t.Errorf("env name %q: %v", name, got)
		}
	}
	if got := bashConfigErrors(map[string]interface{}{"command": "true", "env": map[string]interface{}{"_OK_1": "x"}}); len(got) != 0 {
		t.Errorf("a valid name was refused: %v", got)
	}
}

func runBashPipeline(t *testing.T, dir string, config map[string]interface{}, params map[string]string) (*models.Run, error) {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "bash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &models.Pipeline{
		ID: "bashp", Name: "bashp", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Source",
				Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sh", Type: models.NodeTypeBash, Name: "Shell", Config: config},
		},
		Edges:     []models.Edge{{From: "src", To: "sh"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	return eng.RunPipeline("bashp", params)
}

// A run parameter reaches a bash command as an environment variable, never
// as shell source: a value carrying shell syntax is data, not a command.
func TestBashParametersReachTheCommandAsData(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	hostile := `x"; touch pwned; echo "`
	run, err := runBashPipeline(t, dir, map[string]interface{}{
		"command":     `printf '%s' "$NAME" > received`,
		"working_dir": dir,
		"env":         map[string]interface{}{"NAME": "${param.name}"},
	}, map[string]string{"name": hostile})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %s", run.Error)
	}
	got, err := os.ReadFile(filepath.Join(dir, "received"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != hostile {
		t.Fatalf("the command received %q, want the parameter verbatim %q", got, hostile)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("the parameter value ran as shell code")
	}
}

// A ${param...} written into the command itself is never substituted
// there, where it would become shell source.
func TestBashCommandIsNotSubstituted(t *testing.T) {
	vc := NewVariableContext(map[string]string{"name": "x; touch pwned"}, "run-1", time.Now())
	node := models.Node{Type: models.NodeTypeBash, Config: map[string]interface{}{
		"command": `echo ${param.name}`,
		"env":     map[string]interface{}{"NAME": "${param.name}"},
	}}
	resolved := resolveNodeConfig(vc, node)
	if resolved["command"] != `echo ${param.name}` {
		t.Fatalf("command = %q, want it untouched", resolved["command"])
	}
	if env := resolved["env"].(map[string]interface{}); env["NAME"] != "x; touch pwned" {
		t.Fatalf("env NAME = %q, want the parameter", env["NAME"])
	}
	// The control: other node types still resolve every field.
	node.Type = models.NodeTypeNotify
	if resolveNodeConfig(vc, node)["command"] != "echo x; touch pwned" {
		t.Fatal("non-bash nodes must keep resolving their config")
	}

	errs := bashConfigErrors(map[string]interface{}{"command": `echo ${param.name}`})
	if len(errs) != 1 || !strings.Contains(errs[0], "${param.name}, which is not substituted in a bash command") {
		t.Fatalf("validation = %v, want the reference refused with the env alternative", errs)
	}
	if errs := bashConfigErrors(map[string]interface{}{"command": `echo "${HOME}" ${x:-a.b}`}); len(errs) != 0 {
		t.Fatalf("shell parameter expansion was refused: %v", errs)
	}
}

// A run of a pipeline whose command names a parameter is refused before
// anything executes, and the parameter never reaches the shell.
func TestBashRunRefusesAReferenceInTheCommand(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	run, err := runBashPipeline(t, dir, map[string]interface{}{
		"command":     `true ${param.name}`,
		"working_dir": dir,
	}, map[string]string{"name": "; touch pwned"})
	if err == nil || !strings.Contains(err.Error(), "not substituted in a bash command") {
		t.Fatalf("run = %v, err = %v; want the run refused by validation", run, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("a parameter spliced into the command ran as shell code")
	}
}

// Past validation too -- a runner started without it -- the command is
// never substituted: the runner's own resolution leaves it alone.
func TestBashRunnerDoesNotSubstituteTheCommand(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	st, err := store.NewSQLiteStore(filepath.Join(dir, "runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := &models.Pipeline{
		ID: "bash-unvalidated", Name: "bash-unvalidated", Enabled: true,
		Params: map[string]string{"name": "; touch pwned"},
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Source",
				Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sh", Type: models.NodeTypeBash, Name: "Shell",
				Config: map[string]interface{}{"command": `true ${param.name}`, "working_dir": dir}},
		},
		Edges:     []models.Edge{{From: "src", To: "sh"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner(st, nil, pipe, nil, nil, nil, nil, "", nil).Execute(); err != nil {
		t.Logf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("the runner spliced a parameter into the bash command")
	}
}
