package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/golang-jwt/jwt/v5"
)

// Who started a run (#241).

func attributionRequest(userID, name, tokenName string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/pipelines/p1/run", nil)
	claims := jwt.MapClaims{}
	if userID != "" {
		claims["sub"] = userID
	}
	if name != "" {
		claims["display_name"] = name
	}
	if tokenName != "" {
		claims["token_name"] = tokenName
	}
	return r.WithContext(context.WithValue(r.Context(), "claims", &claims))
}

// A person pressing run is recorded as a person, with the name copied at
// the time rather than resolved later.
func TestAttributionFromASignedInUser(t *testing.T) {
	got := runAttributionFromRequest(attributionRequest("u1", "Alice A", ""))
	if got == nil {
		t.Fatal("attribution = nil, want a user")
	}
	if got.Kind != models.RunTriggerKindUser {
		t.Errorf("kind = %q, want user", got.Kind)
	}
	if got.UserID != "u1" || got.UserName != "Alice A" {
		t.Errorf("attribution = %+v, want u1/Alice A", got)
	}
}

// A token is a machine even though it carries a subject. Conflating the
// two would name whoever minted the token years ago as the person who
// pressed run.
func TestATokenIsNotAPerson(t *testing.T) {
	got := runAttributionFromRequest(attributionRequest("u1", "Alice A", "deploy-bot"))
	if got == nil {
		t.Fatal("attribution = nil")
	}
	if got.Kind != models.RunTriggerKindAPIToken {
		t.Errorf("kind = %q, want api_token", got.Kind)
	}
	if got.TokenName != "deploy-bot" {
		t.Errorf("token_name = %q, want deploy-bot", got.TokenName)
	}
}

// Nil means not recorded. Inventing a placeholder would put a name on a
// run nobody can be held to.
func TestNoClaimsMeansNotRecorded(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/pipelines/p1/run", nil)
	if got := runAttributionFromRequest(r); got != nil {
		t.Errorf("attribution = %+v, want nil with no claims", got)
	}
	// Claims with no subject: authenticated, but nobody to name.
	if got := runAttributionFromRequest(attributionRequest("", "Alice", "")); got != nil {
		t.Errorf("attribution = %+v, want nil with no subject", got)
	}
}

func attributionStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(t.TempDir() + "/attr.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// The round trip through the store, including that an unrecognised kind
// is refused rather than stored as a value nothing renders.
func TestRunAttributionRoundTrip(t *testing.T) {
	s := attributionStore(t)

	if err := s.SetRunAttribution("r1", &models.RunAttribution{
		Kind: models.RunTriggerKindUser, UserID: "u1", UserName: "Alice",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.SetRunAttribution("r2", &models.RunAttribution{
		Kind: models.RunTriggerKindSchedule,
	}); err != nil {
		t.Fatalf("set schedule: %v", err)
	}

	got, err := s.GetRunAttribution([]string{"r1", "r2", "r3"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2; r3 has none and must be absent", len(got))
	}
	if got["r1"].UserName != "Alice" || got["r1"].Kind != models.RunTriggerKindUser {
		t.Errorf("r1 = %+v", got["r1"])
	}
	if got["r2"].Kind != models.RunTriggerKindSchedule || got["r2"].UserID != "" {
		t.Errorf("r2 = %+v, want a schedule with no user", got["r2"])
	}

	// Absent, not zero: a run created before this existed must not read
	// as "started by nobody".
	if _, present := got["r3"]; present {
		t.Error("a run with no attribution appeared in the map")
	}

	// A kind nothing renders is refused at the door.
	if err := s.SetRunAttribution("r4", &models.RunAttribution{Kind: "telepathy"}); err == nil {
		t.Error("an unknown kind was stored; it would render as a blank that looks like no attribution")
	}

	// Re-writing replaces rather than accumulating.
	if err := s.SetRunAttribution("r1", &models.RunAttribution{
		Kind: models.RunTriggerKindRetry, UserID: "u2", UserName: "Bob",
	}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got, _ = s.GetRunAttribution([]string{"r1"})
	if got["r1"].UserName != "Bob" || got["r1"].Kind != models.RunTriggerKindRetry {
		t.Errorf("after rewrite r1 = %+v, want Bob/retry", got["r1"])
	}
}

func TestListRunIDsStartedByIsNewestFirst(t *testing.T) {
	s := attributionStore(t)
	// Run ids are UUIDv7, so id order is creation order; these are
	// lexicographically ordered stand-ins for that.
	for _, id := range []string{"r-001", "r-002", "r-003"} {
		if err := s.SetRunAttribution(id, &models.RunAttribution{
			Kind: models.RunTriggerKindUser, UserID: "u1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetRunAttribution("r-004", &models.RunAttribution{
		Kind: models.RunTriggerKindUser, UserID: "someone-else",
	}); err != nil {
		t.Fatal(err)
	}

	ids, err := s.ListRunIDsStartedBy("u1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want the three u1 started", ids)
	}
	if ids[0] != "r-003" {
		t.Errorf("ids[0] = %q, want r-003 (newest first)", ids[0])
	}
	if limited, _ := s.ListRunIDsStartedBy("u1", 2); len(limited) != 2 {
		t.Errorf("limit 2 returned %d", len(limited))
	}
	// Nobody's runs are not everybody's runs.
	if none, _ := s.ListRunIDsStartedBy("", 10); len(none) != 0 {
		t.Errorf("an empty user id returned %d ids, want none", len(none))
	}
}

// A purge that deletes runs must be able to take their attribution too,
// or the table grows for ever.
func TestDeleteRunAttribution(t *testing.T) {
	s := attributionStore(t)
	for _, id := range []string{"r1", "r2"} {
		if err := s.SetRunAttribution(id, &models.RunAttribution{
			Kind: models.RunTriggerKindUser, UserID: "u1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteRunAttribution([]string{"r1"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRunAttribution([]string{"r1", "r2"})
	if _, present := got["r1"]; present {
		t.Error("r1 survived the delete")
	}
	if _, present := got["r2"]; !present {
		t.Error("r2 was deleted and should not have been")
	}
}

// The read path: a run carries who started it, and one that has no
// record carries nothing rather than an empty attribution.
func TestRunResponseCarriesWhoStartedIt(t *testing.T) {
	s := attributionStore(t)
	seedAttributionRun(t, s, "p1", "r1")
	seedAttributionRun(t, s, "p1", "r2")
	if err := s.SetRunAttribution("r1", &models.RunAttribution{
		Kind: models.RunTriggerKindUser, UserID: "u1", UserName: "Alice",
	}); err != nil {
		t.Fatal(err)
	}

	h := &RunHandler{store: s}
	runs := h.decorateRuns([]models.Run{{ID: "r1"}, {ID: "r2"}})
	if runs[0].TriggeredBy == nil {
		t.Fatal("r1 has no attribution on the wire")
	}
	if runs[0].TriggeredBy.UserName != "Alice" {
		t.Errorf("r1 started by %+v, want Alice", runs[0].TriggeredBy)
	}
	if runs[1].TriggeredBy != nil {
		t.Errorf("r2 = %+v, want nil; a run with no record must not read as started by nobody",
			runs[1].TriggeredBy)
	}

	// And it survives serialisation as an absent field, not a null one.
	body, _ := json.Marshal(runs[1])
	if strings.Contains(string(body), "triggered_by") {
		t.Errorf("an unattributed run serialised a triggered_by field: %s", body)
	}
}

func seedAttributionRun(t *testing.T, s store.Store, pipelineID, runID string) {
	t.Helper()
	if _, err := s.GetPipeline(pipelineID); err != nil {
		if err := s.CreatePipeline(&models.Pipeline{
			ID: pipelineID, Name: pipelineID, Enabled: true,
			WorkspaceID: models.DefaultWorkspaceID,
			Nodes:       []models.Node{{ID: "s1", Type: models.NodeTypeSourceFile, Name: "src"}},
			CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create pipeline: %v", err)
		}
	}
	started := time.Now().UTC()
	if err := s.CreateRun(&models.Run{
		ID: runID, PipelineID: pipelineID, Status: models.RunStatusSuccess, StartedAt: &started,
	}); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
}
