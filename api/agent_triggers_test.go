package api

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/golang-jwt/jwt/v5"
)

// A public address, so the outbound policy allows it without a DNS lookup.
const hookURL = "https://93.184.216.34/hooks/brokoli"

func testKey() *crypto.Config {
	return &crypto.Config{Key: []byte("0123456789abcdef0123456789abcdef")}
}

// newTriggerBed is newAgentBed with the server's encryption key, which
// trigger secrets are sealed with.
func newTriggerBed(t *testing.T) *agentBed {
	t.Helper()
	b := newAgentBed(t)
	close(b.srv.evictDone)
	b.srv = NewServer(0, b.s, engine.NewEngine(b.s), nil, nil, b.users, nil, nil, testKey())
	return b
}

func (b *agentBed) pipeline(id, workspace string) {
	b.t.Helper()
	now := time.Now().UTC()
	if err := b.s.CreatePipeline(&models.Pipeline{ID: id, Name: id, Enabled: true, WorkspaceID: workspace, CreatedAt: now, UpdatedAt: now}); err != nil {
		b.t.Fatal(err)
	}
}

func (b *agentBed) triggerStore() *TriggerStore {
	b.t.Helper()
	ts, err := NewTriggerStore(b.users.db, testKey())
	if err != nil {
		b.t.Fatal(err)
	}
	return ts
}

func TestATriggerIsMadeWithItsSecretShownOnce(t *testing.T) {
	b := newTriggerBed(t)
	agentID, _ := b.agent("triage-bot", AgentRole, "eve", "eve", true)

	code, res, raw := b.do(http.MethodPost, "/api/agents/"+agentID+"/triggers", b.tokens["eve"],
		`{"events":["run.failed"],"url":"`+hookURL+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, raw)
	}
	secret, _ := res["secret"].(string)
	if !strings.HasPrefix(secret, triggerSecretPrefix) || len(secret) < len(triggerSecretPrefix)+40 {
		t.Fatalf("secret = %q, want whsec_ and 32 random bytes", secret)
	}
	tr, _ := res["trigger"].(map[string]any)
	id, _ := tr["id"].(string)
	if tr["cooldown_seconds"] != float64(1800) || tr["enabled"] != true || tr["delivery"] != "webhook" || tr["workspace_id"] != models.DefaultWorkspaceID {
		t.Errorf("trigger = %v, want the defaults: 30 minute cooldown, enabled, webhook, default workspace", tr)
	}

	for _, path := range []string{"/api/agents/" + agentID + "/triggers", "/api/agents/" + agentID + "/triggers/" + id} {
		code, _, raw := b.do(http.MethodGet, path, b.tokens["eve"], "")
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, code, raw)
		}
		if strings.Contains(raw, secret[len(triggerSecretPrefix):]) || strings.Contains(raw, `"secret"`) {
			t.Fatalf("GET %s returned the secret: %s", path, raw)
		}
	}

	// Stored sealed, and it signs with exactly what was shown.
	var stored string
	if err := b.users.db.QueryRow(`SELECT secret FROM agent_triggers WHERE id = ?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == secret || strings.Contains(stored, secret[len(triggerSecretPrefix):]) {
		t.Fatal("the secret is stored in the clear")
	}
	signing, err := b.triggerStore().SigningSecrets(id, time.Now())
	if err != nil || len(signing) != 1 || signing[0] != secret {
		t.Fatalf("signing secrets = %v, %v", signing, err)
	}
}

func TestTriggersWithoutTheServerKeyAreRefused(t *testing.T) {
	b := newAgentBed(t) // no encryption key
	agentID, _ := b.agent("triage-bot", AgentRole, "eve", "eve", true)
	code, _, raw := b.do(http.MethodPost, "/api/agents/"+agentID+"/triggers", b.tokens["eve"], `{"events":["run.failed"],"url":"`+hookURL+`"}`)
	if code != http.StatusServiceUnavailable || !strings.Contains(raw, "encryption key") {
		t.Fatalf("create without a key = %d %s, want 503 naming the key", code, raw)
	}
}

func TestTriggerValidation(t *testing.T) {
	b := newTriggerBed(t)
	agentID, _ := b.agent("triage-bot", AgentRole, "eve", "eve", true)
	b.pipeline("p-here", models.DefaultWorkspaceID)
	b.pipeline("p-elsewhere", "ws-other")

	cases := map[string]struct{ body, want string }{
		"no events":         {`{"events":[],"url":"` + hookURL + `"}`, "at least one event"},
		"unknown event":     {`{"events":["run.exploded"],"url":"` + hookURL + `"}`, "unknown event"},
		"event twice":       {`{"events":["run.failed","run.failed"],"url":"` + hookURL + `"}`, "listed twice"},
		"not http":          {`{"events":["run.failed"],"url":"ftp://93.184.216.34/x"}`, "http or https"},
		"credentials":       {`{"events":["run.failed"],"url":"https://u:p@93.184.216.34/x"}`, "must not carry credentials"},
		"loopback":          {`{"events":["run.failed"],"url":"http://127.0.0.1:9000/hook"}`, "outbound network policy"},
		"cooldown too long": {`{"events":["run.failed"],"url":"` + hookURL + `","cooldown_seconds":604801}`, "cooldown_seconds"},
		"no such pipeline":  {`{"events":["run.failed"],"url":"` + hookURL + `","pipeline_id":"p-none"}`, "no pipeline p-none"},
		"other workspace":   {`{"events":["run.failed"],"url":"` + hookURL + `","pipeline_id":"p-elsewhere"}`, "no pipeline p-elsewhere"},
	}
	for name, c := range cases {
		code, _, raw := b.do(http.MethodPost, "/api/agents/"+agentID+"/triggers", b.tokens["eve"], c.body)
		if code != http.StatusBadRequest || !strings.Contains(raw, c.want) {
			t.Errorf("%s: %d %s, want 400 with %q", name, code, raw, c.want)
		}
	}
	code, res, raw := b.do(http.MethodPost, "/api/agents/"+agentID+"/triggers", b.tokens["eve"],
		`{"events":["run.failed","run.blocked"],"url":"`+hookURL+`","pipeline_id":"p-here","cooldown_seconds":0}`)
	if code != http.StatusCreated {
		t.Fatalf("a valid trigger on one pipeline: %d %s", code, raw)
	}
	if tr := res["trigger"].(map[string]any); tr["pipeline_id"] != "p-here" || tr["cooldown_seconds"] != float64(0) {
		t.Errorf("trigger = %v", tr)
	}
}

func TestWhoManagesTriggers(t *testing.T) {
	b := newTriggerBed(t)
	agentID, agentToken := b.agent("triage-bot", AgentRole, "eve", "eve", true)
	body := `{"events":["run.failed"],"url":"` + hookURL + `"}`
	path := "/api/agents/" + agentID + "/triggers"

	if code, _, raw := b.do(http.MethodPost, path, b.tokens["eve"], body); code != http.StatusCreated {
		t.Fatalf("its owner: %d %s", code, raw)
	}
	if code, _, raw := b.do(http.MethodPost, path, b.tokens["ada"], body); code != http.StatusCreated {
		t.Fatalf("an administrator: %d %s", code, raw)
	}
	if code, _, _ := b.do(http.MethodPost, path, b.tokens["vic"], body); code != http.StatusNotFound {
		t.Errorf("someone else's agent = %d, want 404 (its existence is not theirs to learn)", code)
	}
	if code, _, _ := b.do(http.MethodGet, path, b.tokens["vic"], ""); code != http.StatusNotFound {
		t.Errorf("someone else listing = %d, want 404", code)
	}
	if code, _, _ := b.do(http.MethodPost, path, agentToken, body); code != http.StatusForbidden {
		t.Errorf("the agent itself = %d, want 403: an agent cannot subscribe itself", code)
	}
	if code, _, raw := b.do(http.MethodDelete, "/api/agents/"+agentID, b.tokens["eve"], ""); code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("disable: %d %s", code, raw)
	}
	if code, _, _ := b.do(http.MethodPost, path, b.tokens["eve"], body); code != http.StatusConflict {
		t.Errorf("a disabled agent = %d, want 409", code)
	}
}

// An agent whose role cannot see pipelines is refused when the trigger is
// made, not silently skipped at every delivery.
func TestATriggerNeedsAnAgentThatCanSeeWhatItIsToldAbout(t *testing.T) {
	b := newTriggerBed(t)
	if err := b.s.CreateRole(&models.Role{ID: "blind", Name: "blind", Permissions: []models.Permission{models.PermPipelinesView}}); err != nil {
		t.Fatal(err)
	}
	agentID, _ := b.agent("blind-bot", "blind", "eve", "eve", true)
	code, _, raw := b.do(http.MethodPost, "/api/agents/"+agentID+"/triggers", b.tokens["eve"], `{"events":["run.failed"],"url":"`+hookURL+`"}`)
	if code != http.StatusBadRequest || !strings.Contains(raw, "runs.view") {
		t.Fatalf("an agent that cannot see runs = %d %s, want 400 naming runs.view", code, raw)
	}
}

func TestAgentMaySeePipelineFollowsTheAgentAndItsOwner(t *testing.T) {
	b := newTriggerBed(t)
	agentID, _ := b.agent("triage-bot", AgentRole, "eve", "eve", true)
	pc := NewPermissionChecker(b.s)
	p := &models.Pipeline{ID: "p1", WorkspaceID: models.DefaultWorkspaceID}
	may := func() (bool, string) { return b.users.agentMaySeePipeline(pc, agentID, models.DefaultWorkspaceID, p) }

	if ok, why := may(); !ok {
		t.Fatalf("baseline: refused (%s)", why)
	}
	if ok, why := b.users.agentMaySeePipeline(pc, agentID, "ws-other", p); ok || !strings.Contains(why, "another workspace") {
		t.Errorf("a trigger in another workspace: %v %q", ok, why)
	}

	t.Run("a distribution refuses the identity", func(t *testing.T) {
		RequestIdentityFunc = func(r *http.Request, c *jwt.MapClaims) IdentityRefinement {
			return IdentityRefinement{Status: http.StatusForbidden, Message: "membership ended"}
		}
		t.Cleanup(func() { RequestIdentityFunc = nil })
		if ok, why := may(); ok || !strings.Contains(why, "membership ended") {
			t.Errorf("got %v %q", ok, why)
		}
	})
	t.Run("a distribution resolves narrower permissions", func(t *testing.T) {
		RequestIdentityFunc = func(r *http.Request, c *jwt.MapClaims) IdentityRefinement {
			return IdentityRefinement{Permissions: []models.Permission{models.PermPipelinesView}}
		}
		t.Cleanup(func() { RequestIdentityFunc = nil })
		if ok, why := may(); ok || !strings.Contains(why, "runs.view") {
			t.Errorf("got %v %q", ok, why)
		}
	})
	t.Run("the pipeline is another organization's", func(t *testing.T) {
		OrgResolverFunc = func(string) string { return "org-a" }
		t.Cleanup(func() { OrgResolverFunc = nil })
		other := &models.Pipeline{ID: "p2", OrgID: "org-b", WorkspaceID: models.DefaultWorkspaceID}
		if ok, _ := b.users.agentMaySeePipeline(pc, agentID, models.DefaultWorkspaceID, other); ok {
			t.Error("an agent saw another organization's pipeline")
		}
		mine := &models.Pipeline{ID: "p3", OrgID: "org-a", WorkspaceID: models.DefaultWorkspaceID}
		if ok, why := b.users.agentMaySeePipeline(pc, agentID, models.DefaultWorkspaceID, mine); !ok {
			t.Errorf("its own organization's pipeline refused: %s", why)
		}
	})
	t.Run("the owner is disabled", func(t *testing.T) {
		if _, err := b.users.db.Exec(`UPDATE users SET disabled_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), b.ids["eve"]); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = b.users.db.Exec(`UPDATE users SET disabled_at = '' WHERE id = ?`, b.ids["eve"]) })
		if ok, why := may(); ok || !strings.Contains(why, "owner") {
			t.Errorf("got %v %q", ok, why)
		}
	})
	t.Run("identities are turned off", func(t *testing.T) {
		AgentIdentitiesEnabled = false
		t.Cleanup(func() { AgentIdentitiesEnabled = true })
		if ok, _ := may(); ok {
			t.Error("allowed with agent identities off")
		}
	})
	if err := b.users.DisableAgent(agentID); err != nil {
		t.Fatal(err)
	}
	if ok, why := may(); ok || !strings.Contains(why, "disabled") {
		t.Errorf("a disabled agent: %v %q", ok, why)
	}
}

func TestRotatingASecretKeepsTheOldOneForADay(t *testing.T) {
	b := newTriggerBed(t)
	agentID, _ := b.agent("triage-bot", AgentRole, "eve", "eve", true)
	_, res, _ := b.do(http.MethodPost, "/api/agents/"+agentID+"/triggers", b.tokens["eve"], `{"events":["run.failed"],"url":"`+hookURL+`"}`)
	old := res["secret"].(string)
	id := res["trigger"].(map[string]any)["id"].(string)

	code, rot, raw := b.do(http.MethodPost, "/api/agents/"+agentID+"/triggers/"+id+"/secret", b.tokens["eve"], "")
	if code != http.StatusOK {
		t.Fatalf("rotate: %d %s", code, raw)
	}
	fresh := rot["secret"].(string)
	if fresh == old || !strings.HasPrefix(fresh, triggerSecretPrefix) {
		t.Fatalf("rotated secret = %q (old %q)", fresh, old)
	}
	ts := b.triggerStore()
	if got, _ := ts.SigningSecrets(id, time.Now()); len(got) != 2 || got[0] != fresh || got[1] != old {
		t.Errorf("during the overlap = %v, want the new secret then the old one", got)
	}
	if got, _ := ts.SigningSecrets(id, time.Now().Add(previousSecretOverlap+time.Minute)); len(got) != 1 || got[0] != fresh {
		t.Errorf("after the overlap = %v, want only the new secret", got)
	}
	if _, _, raw := b.do(http.MethodGet, "/api/agents/"+agentID+"/triggers/"+id, b.tokens["eve"], ""); !strings.Contains(raw, "secret_rotated_at") {
		t.Errorf("the trigger does not say when its secret was rotated: %s", raw)
	}
}

func TestATriggerIsUpdatedAndRemoved(t *testing.T) {
	b := newTriggerBed(t)
	agentID, _ := b.agent("triage-bot", AgentRole, "eve", "eve", true)
	_, res, _ := b.do(http.MethodPost, "/api/agents/"+agentID+"/triggers", b.tokens["eve"], `{"events":["run.failed"],"url":"`+hookURL+`"}`)
	id := res["trigger"].(map[string]any)["id"].(string)
	path := "/api/agents/" + agentID + "/triggers/" + id

	code, upd, raw := b.do(http.MethodPatch, path, b.tokens["eve"], `{"events":["run.blocked"],"enabled":false,"cooldown_seconds":60}`)
	if code != http.StatusOK || upd["enabled"] != false || upd["cooldown_seconds"] != float64(60) || upd["url"] != hookURL {
		t.Fatalf("update: %d %s", code, raw)
	}
	if code, _, raw := b.do(http.MethodPatch, path, b.tokens["eve"], `{"url":"http://127.0.0.1/x"}`); code != http.StatusBadRequest {
		t.Errorf("an update to a refused URL: %d %s", code, raw)
	}
	if code, _, _ := b.do(http.MethodDelete, path, b.tokens["eve"], ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	if code, _, _ := b.do(http.MethodGet, path, b.tokens["eve"], ""); code != http.StatusNotFound {
		t.Errorf("after delete = %d, want 404", code)
	}
}

func TestTriggerStoreOnBothDialects(t *testing.T) {
	run := func(t *testing.T, db *sql.DB) {
		t.Helper()
		ts, err := NewTriggerStore(db, testKey())
		if err != nil {
			t.Fatal(err)
		}
		agent := "agent-" + generateID()[:8]
		tr := &AgentTrigger{AgentID: agent, WorkspaceID: "default", Events: []string{TriggerEventRunFailed}, URL: hookURL,
			CooldownSeconds: 1800, Enabled: true, CreatedBy: "ada"}
		secret, err := ts.Create(tr)
		if err != nil || tr.ID == "" {
			t.Fatalf("create: %v", err)
		}
		got, err := ts.Get(agent, tr.ID)
		if err != nil || !got.Watches(TriggerEventRunFailed, "any") || got.Watches(TriggerEventRunBlocked, "any") {
			t.Fatalf("get = %+v, %v", got, err)
		}
		got.PipelineID, got.Enabled = "p1", true
		if err := ts.Update(got); err != nil {
			t.Fatal(err)
		}
		if got, _ := ts.Get(agent, tr.ID); !got.Watches(TriggerEventRunFailed, "p1") || got.Watches(TriggerEventRunFailed, "p2") {
			t.Errorf("a one-pipeline trigger watches %+v", got)
		}
		all, err := ts.List("", "")
		found := false
		for _, a := range all {
			found = found || a.ID == tr.ID
		}
		if err != nil || !found {
			t.Errorf("the dispatcher's list misses it: %v", err)
		}
		if s, _ := ts.SigningSecrets(tr.ID, time.Now()); len(s) != 1 || s[0] != secret {
			t.Errorf("signing secrets = %v", s)
		}
		if _, err := ts.Get("someone-else", tr.ID); err != ErrTriggerNotFound {
			t.Errorf("another agent's trigger: %v", err)
		}
		if err := ts.Delete(agent, tr.ID); err != nil {
			t.Fatal(err)
		}
		if err := ts.Delete(agent, tr.ID); err != ErrTriggerNotFound {
			t.Errorf("deleting twice: %v", err)
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		run(t, db)
	})
	t.Run("postgres", func(t *testing.T) {
		uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
		if uri == "" {
			t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
		}
		st, err := store.NewPostgresStore(uri)
		if err != nil {
			t.Fatal(err)
		}
		run(t, st.RawDB().(*sql.DB))
	})
}
