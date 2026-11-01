package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/tenancy"
)

// world is one organization with two workspaces, and a second
// organization, each holding one pipeline.
type world struct {
	st                  *SQLiteStore
	pipeA, pipeB, pipe2 string // org1/wsA, org1/wsB, org2/wsX
}

func newWorld(t *testing.T) world {
	t.Helper()
	st := newTestStore(t)
	mk := func(org, ws string) string {
		p := &models.Pipeline{ID: common.NewID(), Name: "p-" + ws, OrgID: org, WorkspaceID: ws, Enabled: true,
			CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := st.CreatePipeline(p); err != nil {
			t.Fatalf("CreatePipeline: %v", err)
		}
		return p.ID
	}
	return world{st: st, pipeA: mk("org1", "wsA"), pipeB: mk("org1", "wsB"), pipe2: mk("org2", "wsX")}
}

// seeders put one row of each registered resource under a pipeline and
// answer its id. A resource registered without one fails the test below,
// so registering a resource is not enough to skip its isolation test.
var seeders = map[string]func(t *testing.T, w world, pipelineID, org string) string{
	"pipelines": func(t *testing.T, w world, pipelineID, _ string) string { return pipelineID },
	"runs": func(t *testing.T, w world, pipelineID, org string) string {
		r := &models.Run{ID: common.NewID(), PipelineID: pipelineID, Status: models.RunStatusSuccess, OrgID: org, StartedAt: ptrTime(time.Now())}
		if err := w.st.CreateRun(r); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		return r.ID
	},
	"dead_letter_queue": func(t *testing.T, w world, pipelineID, _ string) string {
		id := common.NewID()
		if _, err := w.st.db.Exec(`INSERT INTO dead_letter_queue (id, pipeline_id, run_id, error, created_at) VALUES (?, ?, 'r', 'boom', '2026-11-01T00:00:00Z')`, id, pipelineID); err != nil {
			t.Fatal(err)
		}
		return id
	},
	"alerts": func(t *testing.T, w world, pipelineID, org string) string {
		a := &models.Alert{ID: common.NewID(), OrgID: org, Kind: "run_failed", Severity: "error", Title: "x", PipelineID: pipelineID, CreatedAt: time.Now()}
		if err := w.st.CreateAlert(a); err != nil {
			t.Fatalf("CreateAlert: %v", err)
		}
		return a.ID
	},
}

// ADR-049 section 5: every registered resource, list and by id, for a
// member of one workspace, a member of both, and another organization.
func TestEveryRegisteredResourceIsIsolated(t *testing.T) {
	onlyA := tenancy.New("org1", "wsA", []string{"wsA"}, tenancy.ActorPerson)
	bothInB := tenancy.New("org1", "wsB", []string{"wsA", "wsB"}, tenancy.ActorPerson)
	other := tenancy.New("org2", "wsX", []string{"wsX"}, tenancy.ActorPerson)

	for _, res := range tenancy.Resources() {
		if len(res) > 2 && res[:2] == "t_" {
			continue // the tenancy package's own test fixtures
		}
		seed, ok := seeders[res]
		if !ok {
			t.Errorf("%s is registered but this test has no seeder for it: add one", res)
			continue
		}
		t.Run(res, func(t *testing.T) {
			w := newWorld(t)
			inA, inB, in2 := seed(t, w, w.pipeA, "org1"), seed(t, w, w.pipeB, "org1"), seed(t, w, w.pipe2, "org2")

			byID := []struct {
				scope tenancy.Scope
				id    string
				want  bool
				what  string
			}{
				{onlyA, inA, true, "member of A, a row in A"},
				{onlyA, inB, false, "member of A only, a row in B"},
				{onlyA, in2, false, "member of A, another organization's row"},
				{bothInB, inA, true, "member of both, a row in A while in B"},
				{other, inA, false, "another organization, a row in A"},
				{tenancy.Scope{}, inA, false, "no scope at all"},
			}
			for _, c := range byID {
				if got := Scoped(w.st, c.scope).Visible(res, c.id); got != c.want {
					t.Errorf("by id, %s: visible = %v, want %v", c.what, got, c.want)
				}
			}
			if Scoped(w.st, onlyA).Visible(res, "no-such-id") {
				t.Error("a missing id is visible")
			}

			list := func(s tenancy.Scope) map[string]bool {
				filter, args, err := s.ListFilter(res, "")
				if err != nil {
					t.Fatal(err)
				}
				rows, err := w.st.db.Query(fmt.Sprintf("SELECT id FROM %s WHERE %s", tenancy.Table(res), filter), args...)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				got := map[string]bool{}
				for rows.Next() {
					var id string
					_ = rows.Scan(&id)
					got[id] = true
				}
				return got
			}
			if got := list(onlyA); !got[inA] || got[inB] || got[in2] || len(got) != 1 {
				t.Errorf("list for a member of A: %v, want only %s", got, inA)
			}
			// A member of both sees the current workspace's rows in a list,
			// not every workspace they belong to.
			if got := list(bothInB); !got[inB] || got[inA] || len(got) != 1 {
				t.Errorf("list in B for a member of both: %v, want only %s", got, inB)
			}
			if got := list(other); !got[in2] || len(got) != 1 {
				t.Errorf("list for the other organization: %v, want only %s", got, in2)
			}
		})
	}
}

// An alert of a deleted pipeline is visible to nobody; an alert about no
// pipeline is the organization's, visible in each of its workspaces.
func TestAlertsWithoutAPipeline(t *testing.T) {
	w := newWorld(t)
	onlyA := tenancy.New("org1", "wsA", []string{"wsA"}, tenancy.ActorPerson)
	orgWide := seeders["alerts"](t, w, "", "org1")
	orphan := seeders["alerts"](t, w, "deleted-pipeline", "org1")
	if !Scoped(w.st, onlyA).Visible("alerts", orgWide) {
		t.Error("an organization-level alert is not visible in its workspace")
	}
	if Scoped(w.st, onlyA).Visible("alerts", orphan) {
		t.Error("an alert of a deleted pipeline is visible")
	}
	got, err := Scoped(w.st, onlyA).Alerts(AlertQuery{OrgID: "org1"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, a := range got {
		ids[a.ID] = true
	}
	if !ids[orgWide] || ids[orphan] {
		t.Errorf("scoped alert list %v: want the org-level alert and not the orphan", ids)
	}
}

// The scoped list methods the handlers use apply the same filter.
func TestScopedListMethods(t *testing.T) {
	w := newWorld(t)
	onlyA := tenancy.New("org1", "wsA", []string{"wsA"}, tenancy.ActorPerson)
	runA := seeders["runs"](t, w, w.pipeA, "org1")
	runB := seeders["runs"](t, w, w.pipeB, "org1")
	for _, id := range []string{runA, runB} {
		if err := w.st.SetRunAttribution(id, &models.RunAttribution{Kind: models.RunTriggerKindUser, UserID: "u1"}); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := Scoped(w.st, onlyA).RunIDsStartedBy("u1", 10)
	if err != nil || len(ids) != 1 || ids[0] != runA {
		t.Fatalf("runs started by u1 in A: %v %v, want [%s]", ids, err, runA)
	}
	seeders["alerts"](t, w, w.pipeA, "org1")
	seeders["alerts"](t, w, w.pipeB, "org1")
	n, err := Scoped(w.st, onlyA).CountUnreadAlerts("u1")
	if err != nil || n != 1 {
		t.Fatalf("unread in A: %d %v, want 1", n, err)
	}
	alerts, _ := Scoped(w.st, onlyA).Alerts(AlertQuery{OrgID: "org1", UserID: "u1"})
	if len(alerts) != 1 || alerts[0].PipelineID != w.pipeA {
		t.Fatalf("alerts in A: %+v", alerts)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
