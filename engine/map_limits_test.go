package engine

// Tests for the server-wide mapping limits (ADR-050, #967): defaults and
// ceilings an operator sets, each failing loudly with the limit and the
// count, never truncating the collection.

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestMapLimitsDefaultsWithoutEnvironment(t *testing.T) {
	for _, name := range []string{envMapDefaultMaxItems, envMapMaxItems, envMapDefaultMaxParallel, envMapMaxParallel, envMapMaxRemoteParallel} {
		t.Setenv(name, "")
	}
	got := currentMapLimits()
	want := mapLimits{DefaultItems: 1000, MaxItems: 100000, DefaultParallel: 4, MaxParallel: 64, MaxRemoteParallel: 16}
	if got.DefaultItems != want.DefaultItems || got.MaxItems != want.MaxItems || got.DefaultParallel != want.DefaultParallel ||
		got.MaxParallel != want.MaxParallel || got.MaxRemoteParallel != want.MaxRemoteParallel || len(got.Warnings) != 0 {
		t.Fatalf("limits = %+v, want %+v", got, want)
	}
}

func TestMapLimitsComeFromTheEnvironment(t *testing.T) {
	t.Setenv(envMapDefaultMaxItems, "20")
	t.Setenv(envMapMaxItems, "50")
	t.Setenv(envMapDefaultMaxParallel, "2")
	t.Setenv(envMapMaxParallel, "8")
	t.Setenv(envMapMaxRemoteParallel, "3")

	cfg, err := parseExpansionConfig(mapNode(map[string]interface{}{"over": "src"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxInstances != 20 || cfg.parallelism(false) != 2 || cfg.parallelism(true) != 3 {
		t.Fatalf("map defaults: items=%d local=%d remote=%d, want 20, 2, 3", cfg.MaxInstances, cfg.parallelism(false), cfg.parallelism(true))
	}

	// A declared max_parallel up to the ceiling is honoured locally, and
	// lowered to the remote bound for remote items.
	cfg, err = parseExpansionConfig(mapNode(map[string]interface{}{"over": "src", "max_items": float64(50), "max_parallel": float64(8)}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxInstances != 50 || cfg.parallelism(false) != 8 || cfg.parallelism(true) != 3 {
		t.Fatalf("declared: items=%d local=%d remote=%d, want 50, 8, 3", cfg.MaxInstances, cfg.parallelism(false), cfg.parallelism(true))
	}

	legacy := models.Node{ID: "fan", Name: "Fan", Type: models.NodeTypeCode, Config: map[string]interface{}{
		"script": "x", "expansion": map[string]interface{}{"over": map[string]interface{}{"f": "src"}},
	}}
	cfg, err = parseExpansionConfig(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxInstances != 20 || cfg.parallelism(false) != 1 {
		t.Fatalf("expansion: items=%d local=%d, want the server default 20 and still one at a time", cfg.MaxInstances, cfg.parallelism(false))
	}
}

// A block may not declare more than the server allows, in either form,
// and the refusal names the setting, the value, the ceiling and the
// variable. The validator reports it when the pipeline is saved.
func TestMapLimitsRefuseADeclarationAboveTheCeiling(t *testing.T) {
	t.Setenv(envMapMaxItems, "50")
	t.Setenv(envMapMaxParallel, "8")

	cases := []struct {
		node models.Node
		want string
	}{
		{mapNode(map[string]interface{}{"over": "src", "max_items": float64(51)}),
			"'map.max_items' is 51, above this server's ceiling of 50 items (BROKOLI_MAP_MAX_ITEMS)"},
		{mapNode(map[string]interface{}{"over": "src", "max_parallel": float64(9)}),
			"'map.max_parallel' is 9, above this server's ceiling of 8 (BROKOLI_MAP_MAX_PARALLEL)"},
		{models.Node{ID: "fan", Name: "Fan", Type: models.NodeTypeCode, Config: map[string]interface{}{
			"script": "x", "expansion": map[string]interface{}{"over": map[string]interface{}{"f": "src"}, "max_instances": float64(51)}}},
			"'expansion.max_instances' is 51, above this server's ceiling of 50 items (BROKOLI_MAP_MAX_ITEMS)"},
	}
	for _, c := range cases {
		if _, err := parseExpansionConfig(c.node); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("parse: err = %v, want it to contain %q", err, c.want)
		}
		p := &models.Pipeline{ID: "p", Name: "p", Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": "x.csv", "format": "csv"}},
			c.node,
		}, Edges: []models.Edge{{From: "src", To: "fan"}}}
		if ve := ValidatePipeline(p); !strings.Contains(ve.Error(), c.want) {
			t.Errorf("validate: %v, want it to contain %q", ve.Error(), c.want)
		}
	}
}

func TestMapLimitsIgnoreBadValuesAndKeepDefaultsUnderCeilings(t *testing.T) {
	t.Setenv(envMapMaxItems, "lots")
	t.Setenv(envMapMaxRemoteParallel, "0")
	t.Setenv(envMapMaxParallel, "6")
	t.Setenv(envMapDefaultMaxParallel, "10")
	l := currentMapLimits()
	if l.MaxItems != defaultMapMaxItemsCeiling || l.MaxRemoteParallel != maxConcurrentRemoteExpansionDispatch || l.DefaultParallel != 6 {
		t.Fatalf("limits = %+v", l)
	}
	t.Setenv(envMapMaxItems, "30")
	t.Setenv(envMapDefaultMaxItems, "40")
	if clamped := currentMapLimits(); clamped.DefaultItems != 30 {
		t.Fatalf("default items above the ceiling = %d, want the ceiling 30", clamped.DefaultItems)
	}
	joined := strings.Join(l.Warnings, "\n")
	for _, want := range []string{
		`ignoring BROKOLI_MAP_MAX_ITEMS="lots"`,
		`ignoring BROKOLI_MAP_MAX_REMOTE_PARALLEL="0"`,
		"BROKOLI_MAP_DEFAULT_MAX_PARALLEL=10 is above BROKOLI_MAP_MAX_PARALLEL=6; using 6",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q do not contain %q", joined, want)
		}
	}
}

// A collection over the server default fails the run, and the message
// says the limit is the server's default and how far a block may raise it.
func TestMapRunOverTheServerDefaultFailsNamingIt(t *testing.T) {
	t.Setenv(envMapDefaultMaxItems, "2")
	t.Setenv(envMapMaxItems, "500")
	run, _, rows, err := runMapPipeline(t, "map-server-default", map[string]interface{}{"over": "files"}, "path\na\nb\nc\n",
		`output_data = {"columns": columns, "rows": rows}`)
	if err == nil || run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %+v, err = %v; want a failed run", run, err)
	}
	want := "expansion would create 3 instances, exceeding the configured limit of 2 (this server's default, BROKOLI_MAP_DEFAULT_MAX_ITEMS; set map.max_items to raise it, up to 500)"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused collection wrote %d rows; it must not be truncated to fit", len(rows))
	}
}

// The server's default parallelism is what a block without max_parallel
// gets: with 2, six items never run more than two at once.
func TestMapRunUsesTheServerDefaultParallelism(t *testing.T) {
	t.Setenv(envMapDefaultMaxParallel, "2")
	run, _, rows, err := runMapPipeline(t, "map-server-parallel", map[string]interface{}{"over": "files", "key": "path"}, "path\na\nb\nc\nd\ne\nf\n", timingScript)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if got := maxOverlap(rows); got != 2 {
		t.Fatalf("items running at once = %d, want 2 (BROKOLI_MAP_DEFAULT_MAX_PARALLEL)", got)
	}
}

// A limit variable the server ignores is reported in the log of every
// mapped node, so an operator's typo does not pass unnoticed.
func TestMapRunLogsIgnoredLimitVariables(t *testing.T) {
	t.Setenv(envMapMaxItems, "lots")
	run, st, _, err := runMapPipeline(t, "map-limit-warning", map[string]interface{}{"over": "files"}, "path\na\n",
		`output_data = {"columns": columns, "rows": rows}`)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	logs, err := st.GetLogs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range logs {
		if e.NodeID == "fan" && e.Level == models.LogLevelWarning && strings.Contains(e.Message, `ignoring BROKOLI_MAP_MAX_ITEMS="lots"`) {
			return
		}
	}
	t.Fatalf("no warning about BROKOLI_MAP_MAX_ITEMS in the node's log: %+v", logs)
}
