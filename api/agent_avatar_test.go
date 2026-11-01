package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// An agent has an avatar from the start, drawn from its id, so it is the
// same on every read and every client.
func TestAnAgentHasAnAvatarFromItsId(t *testing.T) {
	b := newAgentBed(t)
	id, _ := b.agent("triage-bot", AgentRole, "eve", "eve", true)
	_, a, raw := b.do(http.MethodGet, "/api/agents/"+id, b.tokens["eve"], "")
	want := defaultAgentAvatar(id)
	if a["avatar"] != want || !validAgentAvatar(want) {
		t.Fatalf("avatar = %v, want %s from the id: %s", a["avatar"], want, raw)
	}
	if got := defaultAgentAvatar(id); got != want {
		t.Fatal("the default is not stable")
	}
	// The hash spreads: 64 ids land on every avatar.
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		seen[defaultAgentAvatar(generateID())] = true
	}
	if len(seen) != len(AgentAvatars) {
		t.Errorf("64 ids gave %d of %d avatars", len(seen), len(AgentAvatars))
	}
}

// Its owner picks another; anything not in the family is refused, and an
// empty choice goes back to the default.
func TestAnAgentsOwnerPicksItsAvatar(t *testing.T) {
	b := newAgentBed(t)
	id, _ := b.agent("triage-bot", AgentRole, "eve", "eve", true) // eve owns it and is not an admin
	patch := func(token, body string) (int, map[string]any, string) {
		return b.do(http.MethodPatch, "/api/agents/"+id, token, body)
	}

	if code, a, raw := patch(b.tokens["eve"], `{"avatar":"romanesco"}`); code != http.StatusOK || a["avatar"] != "romanesco" {
		t.Fatalf("owner picks romanesco: %d %s", code, raw)
	}
	if _, a, _ := b.do(http.MethodGet, "/api/agents/"+id, b.tokens["ada"], ""); a["avatar"] != "romanesco" {
		t.Errorf("after the pick, GET says %v", a["avatar"])
	}
	if code, _, raw := patch(b.tokens["eve"], `{"avatar":"carrot"}`); code != http.StatusBadRequest || !strings.Contains(raw, "seedling") {
		t.Errorf("an avatar outside the family: %d %s, want 400 naming the family", code, raw)
	}
	if code, a, raw := patch(b.tokens["eve"], `{"avatar":""}`); code != http.StatusOK || a["avatar"] != defaultAgentAvatar(id) {
		t.Errorf("empty goes back to the default: %d %s", code, raw)
	}
	// The owner still may not change what the agent may do.
	if code, _, raw := patch(b.tokens["eve"], `{"role":"editor","avatar":"kohlrabi"}`); code != http.StatusForbidden {
		t.Errorf("owner changes the role: %d %s, want 403", code, raw)
	}
	if _, a, _ := b.do(http.MethodGet, "/api/agents/"+id, b.tokens["ada"], ""); a["role"] != AgentRole || a["avatar"] != defaultAgentAvatar(id) {
		t.Errorf("a refused request changed the agent: %v", a)
	}
	// Someone else's agent is not theirs to dress.
	if code, _, _ := patch(b.tokens["vic"], `{"avatar":"seedling"}`); code != http.StatusNotFound {
		t.Errorf("another person picks: %d, want 404", code)
	}
	// Whoever manages users may pick too, alongside other fields.
	if code, a, raw := patch(b.tokens["ada"], `{"avatar":"seedling","description":"triages"}`); code != http.StatusOK || a["avatar"] != "seedling" || a["description"] != "triages" {
		t.Errorf("admin picks with a description: %d %s", code, raw)
	}
}

// avatar_url is where a person's picture lives. An agent whose row holds
// anything but an avatar name (a URL, say) has the default.
func TestAnAgentAvatarThatIsNotANameIsTheDefault(t *testing.T) {
	b := newAgentBed(t)
	id, _ := b.agent("triage-bot", AgentRole, "eve", "eve", true)
	if _, err := b.users.db.Exec(b.users.q(`UPDATE users SET avatar_url = ? WHERE id = ?`), "https://example.com/me.png", id); err != nil {
		t.Fatal(err)
	}
	if _, a, _ := b.do(http.MethodGet, "/api/agents/"+id, b.tokens["eve"], ""); a["avatar"] != defaultAgentAvatar(id) {
		t.Errorf("avatar = %v, want the default", a["avatar"])
	}
}

// The run page and the pipeline settings draw the agent's avatar.
func TestTriggerViewsCarryTheAgentsAvatar(t *testing.T) {
	b := newDispatchBed(t)
	rc := newReceiver(t)
	b.pipelineWithNodes("orders", models.DefaultWorkspaceID)
	b.trigger(`{"events":["run.failed"],"url":"` + rc.srv.URL + `","cooldown_seconds":0}`)
	if code, _, raw := b.do(http.MethodPatch, "/api/agents/"+b.agentID, b.tokens["eve"], `{"avatar":"cauliflower"}`); code != http.StatusOK {
		t.Fatalf("pick: %d %s", code, raw)
	}
	failed := b.runEvent("orders", models.RunEventTerminal, models.RunStatusFailed, "boom")
	b.pass()
	for _, path := range []string{"/api/runs/" + failed + "/agents", "/api/pipelines/orders/triggers"} {
		rec := serve(b.srv, http.MethodGet, path, b.tokens["vic"], "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"agent_avatar":"cauliflower"`) {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// The UI draws these by name and lists them in this order
// (ui/packages/ui/src/agentAvatar.tsx, pinned by its test too). Defaults
// come from the position, so the order is a contract.
func TestTheAvatarFamilyIsInItsContractOrder(t *testing.T) {
	want := []string{"broccoli", "romanesco", "cauliflower", "purple", "broccolini", "sprout", "kohlrabi", "seedling"}
	if strings.Join(AgentAvatars[:], ",") != strings.Join(want, ",") {
		t.Fatalf("AgentAvatars = %v, want %v", AgentAvatars, want)
	}
}
