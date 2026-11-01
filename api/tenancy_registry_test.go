package api

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/tenancy"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ADR-049 section 5, for every resource registered anywhere this package
// links (the store's and this package's own): list and by id, for a
// member of one workspace, a member of both, and another organization.
// A resource registered without a seeder here fails the test.

type regWorld struct {
	st                  *store.SQLiteStore
	db                  *sql.DB
	pipeA, pipeB, pipe2 string
}

var regSeeders = map[string]func(t *testing.T, w regWorld, pipelineID, org, ws string) string{
	"pipelines": func(_ *testing.T, _ regWorld, pipelineID, _, _ string) string { return pipelineID },
	"runs": func(t *testing.T, w regWorld, pipelineID, org, _ string) string {
		return regExec(t, w, `INSERT INTO runs (id, pipeline_id, status, org_id) VALUES (?, ?, 'success', ?)`, pipelineID, org)
	},
	"alerts": func(t *testing.T, w regWorld, pipelineID, org, _ string) string {
		return regExec(t, w, `INSERT INTO alerts (id, org_id, kind, title, pipeline_id, created_at) VALUES (?, ?, 'run_failed', 'x', ?, '2026-11-01T00:00:00Z')`, org, pipelineID)
	},
	"dead_letter_queue": func(t *testing.T, w regWorld, pipelineID, _, _ string) string {
		return regExec(t, w, `INSERT INTO dead_letter_queue (id, pipeline_id, run_id, error, created_at) VALUES (?, ?, 'r', 'boom', '2026-11-01T00:00:00Z')`, pipelineID)
	},
	"change_requests": func(t *testing.T, w regWorld, pipelineID, _, _ string) string {
		return regExec(t, w, `INSERT INTO change_requests (id, pipeline_id, title, status, base_version, proposed, created_by, created_at)
			VALUES (?, ?, 'x', 'open', 1, '{}', 'u', '2026-11-01T00:00:00Z')`, pipelineID)
	},
	"agent_triggers": func(t *testing.T, w regWorld, _, _, ws string) string {
		return regExec(t, w, `INSERT INTO agent_triggers (id, agent_id, workspace_id, events, cooldown_seconds, created_by, created_at, updated_at)
			VALUES (?, 'agent', ?, '[]', 0, 'u', '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`, ws)
	},
	"agent_conversations": func(t *testing.T, w regWorld, _, _, ws string) string {
		return regExec(t, w, `INSERT INTO agent_conversations (id, workspace_id, agent_id, person_id, created_at, last_message_at)
			VALUES (?, ?, 'agent', 'p-`+ws+`', '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`, ws)
	},
}

func regExec(t *testing.T, w regWorld, query string, args ...any) string {
	t.Helper()
	id := common.NewID()
	if _, err := w.db.Exec(query, append([]any{id}, args...)...); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

func newRegWorld(t *testing.T) regWorld {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "reg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.RawDB().(*sql.DB)
	if _, err := NewChangeRequestStore(db); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTriggerStore(db, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMessageStore(db); err != nil {
		t.Fatal(err)
	}
	mk := func(org, ws string) string {
		p := &models.Pipeline{ID: common.NewID(), Name: ws, OrgID: org, WorkspaceID: ws, Enabled: true, Edges: []models.Edge{}}
		if err := st.CreatePipeline(p); err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	return regWorld{st: st, db: db, pipeA: mk("org1", "wsA"), pipeB: mk("org1", "wsB"), pipe2: mk("org2", "wsX")}
}

func TestEveryRegisteredResourceIsIsolatedInTheAPI(t *testing.T) {
	onlyA := tenancy.New("org1", "wsA", []string{"wsA"}, tenancy.ActorPerson)
	bothInB := tenancy.New("org1", "wsB", []string{"wsA", "wsB"}, tenancy.ActorPerson)
	other := tenancy.New("org2", "wsX", []string{"wsX"}, tenancy.ActorPerson)
	for _, res := range tenancy.Resources() {
		seed, ok := regSeeders[res]
		if !ok {
			t.Errorf("%s is registered but has no seeder in this test: add one", res)
			continue
		}
		t.Run(res, func(t *testing.T) {
			w := newRegWorld(t)
			inA, inB, in2 := seed(t, w, w.pipeA, "org1", "wsA"), seed(t, w, w.pipeB, "org1", "wsB"), seed(t, w, w.pipe2, "org2", "wsX")
			for _, c := range []struct {
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
				{tenancy.Scope{}, inA, false, "no scope"},
			} {
				if got := store.Scoped(w.st, c.scope).Visible(res, c.id); got != c.want {
					t.Errorf("by id, %s: %v, want %v", c.what, got, c.want)
				}
			}
			list := func(s tenancy.Scope) map[string]bool {
				f, args, err := s.ListFilter(res, "")
				if err != nil {
					t.Fatal(err)
				}
				rows, err := w.db.Query(fmt.Sprintf("SELECT id FROM %s WHERE %s", tenancy.Table(res), f), args...)
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
			if got := list(onlyA); !got[inA] || len(got) != 1 {
				t.Errorf("list for a member of A: %v, want only %s", got, inA)
			}
			if got := list(bothInB); !got[inB] || len(got) != 1 {
				t.Errorf("list in B for a member of both: %v, want only %s", got, inB)
			}
			if got := list(other); !got[in2] || len(got) != 1 {
				t.Errorf("list for another organization: %v, want only %s", got, in2)
			}
		})
	}
}
