package engine

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestResolve_EnvVar(t *testing.T) {
	os.Setenv("BROKED_TEST_VAR", "hello")
	defer os.Unsetenv("BROKED_TEST_VAR")
	// ${env.*} is deny-by-default now: the server's own environment holds
	// its database URL, signing secret and encryption key, and this
	// resolver used to hand back any of them. An operator opts a name in.
	t.Setenv(pipelineEnvAllowEnv, "BROKED_TEST_VAR")

	vc := NewVariableContext(nil, "run-1", time.Now())
	result := vc.Resolve("value is ${env.BROKED_TEST_VAR}")
	if result != "value is hello" {
		t.Errorf("expected 'value is hello', got %q", result)
	}
}

func TestResolve_Param(t *testing.T) {
	vc := NewVariableContext(map[string]string{"date": "2024-01-15"}, "run-1", time.Now())
	result := vc.Resolve("process ${param.date}")
	if result != "process 2024-01-15" {
		t.Errorf("expected 'process 2024-01-15', got %q", result)
	}
}

func TestResolve_RunID(t *testing.T) {
	vc := NewVariableContext(nil, "abc-123", time.Now())
	result := vc.Resolve("run ${run.id}")
	if result != "run abc-123" {
		t.Errorf("expected 'run abc-123', got %q", result)
	}
}

func TestResolve_RunDate(t *testing.T) {
	t0 := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	vc := NewVariableContext(nil, "run-1", t0)
	result := vc.Resolve("date: ${run.date}")
	if result != "date: 2024-03-15" {
		t.Errorf("expected 'date: 2024-03-15', got %q", result)
	}
}

func TestResolve_Secret(t *testing.T) {
	os.Setenv("BROKED_SECRET_DB_PASSWORD", "s3cret")
	defer os.Unsetenv("BROKED_SECRET_DB_PASSWORD")

	vc := NewVariableContext(nil, "run-1", time.Now())
	result := vc.Resolve("postgres://user:${secret.db_password}@host/db")
	if result != "postgres://user:s3cret@host/db" {
		t.Errorf("expected resolved secret, got %q", result)
	}
}

func TestResolve_Multiple(t *testing.T) {
	vc := NewVariableContext(map[string]string{"table": "users"}, "run-1", time.Now())
	result := vc.Resolve("SELECT * FROM ${param.table} WHERE run = '${run.id}'")
	if !strings.Contains(result, "users") || !strings.Contains(result, "run-1") {
		t.Errorf("expected resolved vars, got %q", result)
	}
}

func TestResolve_NoVars(t *testing.T) {
	vc := NewVariableContext(nil, "run-1", time.Now())
	result := vc.Resolve("no variables here")
	if result != "no variables here" {
		t.Errorf("should pass through, got %q", result)
	}
}

func TestResolve_UnknownVar(t *testing.T) {
	vc := NewVariableContext(nil, "run-1", time.Now())
	result := vc.Resolve("${unknown.thing}")
	if result != "${unknown.thing}" {
		t.Errorf("should keep unresolved var, got %q", result)
	}
}

func TestResolveConfig(t *testing.T) {
	vc := NewVariableContext(map[string]string{"path": "/data"}, "run-1", time.Now())
	config := map[string]interface{}{
		"path":  "${param.path}/input.csv",
		"table": "users",
		"nested": map[string]interface{}{
			"key": "${run.id}",
		},
	}
	resolved := vc.ResolveConfig(config)
	if resolved["path"] != "/data/input.csv" {
		t.Errorf("path = %q", resolved["path"])
	}
	nested := resolved["nested"].(map[string]interface{})
	if nested["key"] != "run-1" {
		t.Errorf("nested key = %q", nested["key"])
	}
}

// varFilterContext is a run with a data interval, for the filter cases.
// Every field of the interval start is a distinct two-digit number, so a
// layout that swapped two tokens could not pass by coincidence.
func varFilterContext() *VariableContext {
	start := time.Date(2024, 3, 14, 1, 2, 5, 0, time.UTC)
	end := time.Date(2024, 3, 15, 1, 2, 5, 0, time.UTC)
	vc := NewVariableContext(map[string]string{"table": "users"},
		"run-1", time.Date(2024, 3, 15, 14, 30, 45, 0, time.UTC))
	vc.IntervalStart = &start
	vc.IntervalEnd = &end
	return vc
}

func TestResolve_DateFilter(t *testing.T) {
	vc := varFilterContext()
	result := vc.Resolve("orders-${interval.start|date:YYYYMMDD}.csv")
	if result != "orders-20240314.csv" {
		t.Errorf("expected 'orders-20240314.csv', got %q", result)
	}
}

func TestResolve_DateFilterTokens(t *testing.T) {
	vc := varFilterContext()
	result := vc.Resolve("${interval.start|date:YYYY YY MM DD HH mm ss}")
	if result != "2024 24 03 14 01 02 05" {
		t.Errorf("expected '2024 24 03 14 01 02 05', got %q", result)
	}
}

func TestResolve_DateFilterLiterals(t *testing.T) {
	// Bytes that are not tokens are copied through, T and Z included, so a
	// layout can spell RFC3339 back out.
	vc := varFilterContext()
	result := vc.Resolve("${interval.start|date:YYYY-MM-DDTHH:mm:ssZ}")
	if result != "2024-03-14T01:02:05Z" {
		t.Errorf("expected RFC3339 spelled from tokens, got %q", result)
	}
}

func TestResolve_ShiftThenFormat(t *testing.T) {
	// The headline case: deliver today the file covering yesterday.
	vc := varFilterContext()
	result := vc.Resolve("orders-${interval.start|shift:-1d|date:YYYYMMDD}.csv")
	if result != "orders-20240313.csv" {
		t.Errorf("expected 'orders-20240313.csv', got %q", result)
	}
}

func TestResolve_ShiftUnits(t *testing.T) {
	vc := varFilterContext()
	cases := []struct{ expr, want string }{
		{"${interval.start|shift:-1s}", "2024-03-14T01:02:04Z"},
		{"${interval.start|shift:+1m}", "2024-03-14T01:03:05Z"},
		{"${interval.start|shift:2h}", "2024-03-14T03:02:05Z"},
		{"${interval.start|shift:-1d}", "2024-03-13T01:02:05Z"},
		{"${interval.start|shift:-1w}", "2024-03-07T01:02:05Z"},
		{"${interval.end|shift:-1d|date:YYYYMMDD}", "20240314"},
	}
	for _, c := range cases {
		if got := vc.Resolve(c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestResolve_ShiftAloneMatchesBareReference(t *testing.T) {
	// A shift that moves nothing must render byte for byte like the
	// reference with no filter at all: filters change the format, never
	// the clock or the zone.
	vc := varFilterContext()
	if got, want := vc.Resolve("${interval.start|shift:0s}"), vc.Resolve("${interval.start}"); got != want {
		t.Errorf("shift:0s = %q, bare reference = %q", got, want)
	}
}

func TestResolve_ShiftChaining(t *testing.T) {
	// Compound offsets chain, which is why no unit larger than a week is
	// needed and why none is offered.
	vc := varFilterContext()
	result := vc.Resolve("${interval.start|shift:-1d|shift:-2h|date:YYYYMMDD-HHmm}")
	if result != "20240312-2302" {
		t.Errorf("expected '20240312-2302', got %q", result)
	}
}

func TestResolve_RunStartedAtFilter(t *testing.T) {
	vc := varFilterContext()
	result := vc.Resolve("run-${run.started_at|date:YYYYMMDD-HHmm}")
	if result != "run-20240315-1430" {
		t.Errorf("expected 'run-20240315-1430', got %q", result)
	}
}

func TestResolve_FilterKeepsTheReferencesZone(t *testing.T) {
	// A filter changes the format, never the clock. ${run.started_at} has
	// always printed the zone it carries, so its filtered form must read
	// the same wall clock: forcing UTC here would move a run that started
	// at 09:07 in +05:00 to 04:07 and name the file after the wrong hour.
	zone := time.FixedZone("plus5", 5*60*60)
	vc := NewVariableContext(nil, "run-1", time.Date(2024, 3, 14, 9, 7, 0, 0, zone))
	if got := vc.Resolve("${run.started_at|date:YYYYMMDD-HHmm}"); got != "20240314-0907" {
		t.Errorf("expected the reference's own zone, got %q", got)
	}
	if got, want := vc.Resolve("${run.started_at|shift:0s}"), vc.Resolve("${run.started_at}"); got != want {
		t.Errorf("shift:0s = %q, bare reference = %q", got, want)
	}
}

func TestResolve_FilterWhitespace(t *testing.T) {
	// Spaces around the reference and its filters are ignored once a pipe
	// is present. A reference with no pipe is still matched exactly as it
	// always was, so nothing that resolves today starts resolving now.
	vc := varFilterContext()
	if got := vc.Resolve("${interval.start | shift:-1d | date:YYYYMMDD}"); got != "20240313" {
		t.Errorf("spaced filters = %q, want 20240313", got)
	}
	if got := vc.Resolve("${ interval.start }"); got != "${ interval.start }" {
		t.Errorf("a filterless reference must keep its old exact matching, got %q", got)
	}
}

func TestResolve_FilterOnNonTimestamp(t *testing.T) {
	// Filters read timestamps. Anything else keeps the whole reference
	// visible rather than resolving to a date nobody asked for -- and
	// ${run.date} is excluded on purpose: it is already rendered text, and
	// the instant behind it is ${run.started_at}.
	vc := varFilterContext()
	for _, expr := range []string{
		"${param.table|date:YYYY}",
		"${run.date|shift:-1d}",
		"${run.id|date:YYYY}",
		"${var.partner|date:YYYY}",
		"${env.HOME|date:YYYY}",
	} {
		if got := vc.Resolve(expr); got != expr {
			t.Errorf("%s = %q, want it left visible", expr, got)
		}
	}
}

func TestResolve_FilterWithoutInterval(t *testing.T) {
	// A manual run carries no interval. The reference stays visible with a
	// filter exactly as it does without one: an empty filename is the
	// silent kind of wrong.
	vc := NewVariableContext(nil, "run-1", time.Now())
	expr := "orders-${interval.start|date:YYYYMMDD}.csv"
	if got := vc.Resolve(expr); got != expr {
		t.Errorf("expected the reference left visible, got %q", got)
	}
}

func TestResolve_MalformedFilter(t *testing.T) {
	vc := varFilterContext()
	for _, expr := range []string{
		"${interval.start|date:}",             // no layout
		"${interval.start|date}",              // no argument at all
		"${interval.start|shift:}",            // no offset
		"${interval.start|shift:-1}",          // no unit
		"${interval.start|shift:1y}",          // years are not a fixed duration
		"${interval.start|shift:-1.5d}",       // not an integer count
		"${interval.start|shift:--1d}",        // not a signed integer
		"${interval.start|upper:x}",           // unknown filter
		"${interval.start|date:YYYY|date:MM}", // a filter after date: has text, not a time
		"${interval.start||date:YYYY}",        // empty filter
		"${interval.start|date:yyyy-mm-dd}",   // lowercase typo: would render 2024-02-dd
		"${interval.start|date:YYYYMMDDD}",    // a stray token letter
	} {
		if got := vc.Resolve(expr); got != expr {
			t.Errorf("%s = %q, want it left visible", expr, got)
		}
	}
}

func TestResolve_FilteredIntervalIsStillSliceScoped(t *testing.T) {
	// Backfill refuses a pipeline that does not scope itself to the slice
	// it is handed, by looking for ${interval. in node config. Adding a
	// date format to a path must not quietly turn a slice-scoped pipeline
	// into one backfill rejects.
	p := &models.Pipeline{Nodes: []models.Node{{
		ID: "deliver",
		Config: map[string]interface{}{
			"path": "orders-${interval.start|shift:-1d|date:YYYYMMDD}.csv",
		},
	}}}
	if !pipelineReferencesInterval(p) {
		t.Error("a filtered ${interval.*} reference must still count as slice-scoped")
	}
}
