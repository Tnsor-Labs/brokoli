package tenancy

import (
	"context"
	"strings"
	"testing"
)

func TestTheZeroScopeSeesNothing(t *testing.T) {
	var s Scope
	if s.Valid() || s.Lists("o", "w") || s.Sees("", "") {
		t.Fatal("the zero scope must see nothing")
	}
	if f, _, _ := s.ListFilter("t_pipelines", ""); f != "1=0" {
		t.Fatalf("zero scope filter %q, want 1=0", f)
	}
	if _, ok := From(context.Background()); ok {
		t.Fatal("a context with no scope must not yield one")
	}
	if _, ok := From(WithScope(context.Background(), s)); ok {
		t.Fatal("a zero scope carried in a context must not count as one")
	}
}

func TestTheRules(t *testing.T) {
	both := New("org1", "wsA", []string{"wsA", "wsB"}, ActorPerson)
	onlyA := New("org1", "wsA", []string{"wsA"}, ActorPerson)
	orgOnly := OrgOnly("org1", "", ActorPerson)
	free := Unrestricted("")
	cases := []struct {
		name        string
		s           Scope
		org, ws     string
		lists, sees bool
	}{
		{"current workspace", onlyA, "org1", "wsA", true, true},
		{"another workspace, not a member", onlyA, "org1", "wsB", false, false},
		{"another workspace, a member", both, "org1", "wsB", false, true},
		{"another organization", both, "org2", "wsA", false, false},
		{"legacy default workspace", onlyA, "org1", "default", false, true},
		{"legacy row with no organization", onlyA, "", "wsB", false, false},
		{"legacy row, no org and no workspace", onlyA, "", "", false, true},
		{"org only: any workspace of the org", orgOnly, "org1", "wsZ", true, true},
		{"org only: another org", orgOnly, "org2", "wsZ", false, false},
		{"unrestricted", free, "org2", "wsZ", true, true},
	}
	for _, c := range cases {
		if got := c.s.Lists(c.org, c.ws); got != c.lists {
			t.Errorf("%s: Lists = %v, want %v", c.name, got, c.lists)
		}
		if got := c.s.Sees(c.org, c.ws); got != c.sees {
			t.Errorf("%s: Sees = %v, want %v", c.name, got, c.sees)
		}
	}
}

func TestMemberIsCopied(t *testing.T) {
	m := []string{"wsA"}
	s := New("org1", "wsA", m, ActorPerson)
	m[0] = "wsB"
	if s.Sees("org1", "wsB") {
		t.Fatal("changing the caller's slice after New widened the scope")
	}
}

func init() {
	Register("t_pipelines", Direct("t_pipelines", "org_id", "workspace_id"))
	Register("t_runs", Via("t_runs", "pipeline_id", "t_pipelines"))
	Register("t_alerts", Via("t_alerts", "pipeline_id", "t_pipelines").AllowNull())
}

func TestListFilters(t *testing.T) {
	s := New("org1", "wsA", []string{"wsA"}, ActorPerson)
	for res, want := range map[string]string{
		"t_pipelines": "(p.org_id = ? AND p.workspace_id = ?)",
		"t_runs":      "(p.pipeline_id IN (SELECT id FROM t_pipelines WHERE org_id = ? AND workspace_id = ?))",
		"t_alerts":    "(p.pipeline_id IS NULL OR p.pipeline_id = '' OR p.pipeline_id IN (SELECT id FROM t_pipelines WHERE org_id = ? AND workspace_id = ?))",
	} {
		got, args, err := s.ListFilter(res, "p")
		if err != nil || got != want || len(args) != 2 || args[0] != "org1" || args[1] != "wsA" {
			t.Errorf("%s: %q %v %v\nwant %q", res, got, args, err, want)
		}
	}
	if got, args, _ := OrgOnly("org1", "", ActorPerson).ListFilter("t_runs", ""); got != "(pipeline_id IN (SELECT id FROM t_pipelines WHERE org_id = ?))" || len(args) != 1 {
		t.Errorf("org-only filter %q %v", got, args)
	}
	if got, _, _ := Unrestricted("").ListFilter("t_runs", ""); got != "1=1" {
		t.Errorf("unrestricted filter %q", got)
	}
	if _, _, err := s.ListFilter("nope", ""); err == nil {
		t.Error("an unregistered resource must be an error, not an unfiltered list")
	}
}

func TestOwnerQueries(t *testing.T) {
	q, _ := OwnerQuery("t_runs")
	if !strings.Contains(q, "JOIN t_pipelines p ON p.id = c.pipeline_id") || strings.Contains(q, "LEFT JOIN") {
		t.Errorf("runs owner query: %s", q)
	}
	Register("t_ownership", Via("t_ownership", "pipeline_id", "t_pipelines").KeyedBy("pipeline_id"))
	if q, _ := OwnerQuery("t_ownership"); !strings.HasSuffix(q, "WHERE c.pipeline_id = ?") {
		t.Errorf("a resource keyed by pipeline_id: %s", q)
	}
	q, _ = OwnerQuery("t_alerts")
	if !strings.Contains(q, "LEFT JOIN") || !strings.Contains(q, "p.id IS NOT NULL") {
		t.Errorf("alerts owner query must keep parentless rows and drop orphans: %s", q)
	}
}

func TestRegisterRefusesWhatIsNotAnIdentifier(t *testing.T) {
	for name, spec := range map[string]Spec{
		"x; DROP TABLE runs": Direct("t", "o", "w"),
		"bad_col":            Direct("t", "org_id) OR (1=1", "w"),
		"no_parent":          Via("t", "pipeline_id", "unregistered"),
		"neither":            {table: "t"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%q: registered", name)
				}
			}()
			Register(name, spec)
		}()
	}
}
