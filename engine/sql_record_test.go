package engine

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// sqlRecordSourceDB creates a sqlite database with one small table and
// returns the URI a source_db/sink_db node reaches it by.
func sqlRecordSourceDB(t *testing.T, rows int) string {
	t.Helper()
	uri := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		t.Fatalf("open data db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER, name TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := 0; i < rows; i++ {
		if _, err := db.Exec(`INSERT INTO t (id, name) VALUES (?, ?)`,
			i, fmt.Sprintf("row-%d-%s", i, strings.Repeat("x", 60))); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	return uri
}

// recordedStatements is every statement recorded for a node, in event order.
func recordedStatements(t *testing.T, s store.Store, runID, nodeID string) []string {
	t.Helper()
	events, err := s.ListEventsByRun(runID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var out []string
	for _, e := range events {
		if e.EventType == models.AttemptQuery && e.NodeID == nodeID {
			out = append(out, e.Payload.Statement)
		}
	}
	return out
}

// runSQLRecordPipeline runs a one-node or two-node pipeline and returns the
// run plus the store, whether or not the run succeeded -- several of these
// tests are specifically about what a FAILED node records.
func runSQLRecordPipeline(t *testing.T, nodes []models.Node, edges []models.Edge, tweaks ...func(*Engine)) (*models.Run, store.Store) {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := NewEngine(st)
	for _, tweak := range tweaks {
		tweak(e)
	}
	eng := drainEngineOnCleanup(t, e)
	p := &models.Pipeline{
		ID: "sqlrec", Name: "SQL Record", Enabled: true,
		Nodes: nodes, Edges: edges,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	run, err := eng.RunPipeline(p.ID)
	if run == nil {
		t.Fatalf("run pipeline returned no run: %v", err)
	}
	return run, st
}

// The feature itself: the recorded statement is what reached the server,
// with ${...} already substituted, not the template the pipeline holds.
//
// ${run.id} is used as the substituted value because its rendered form is
// knowable exactly -- it is the run's own ID -- so this asserts the real
// text rather than merely that something changed.
func TestRecordedSQL_RecordsTheRenderedQueryNotTheTemplate(t *testing.T) {
	uri := sqlRecordSourceDB(t, 3)
	template := "SELECT id, name FROM t WHERE name != '${run.id}'"

	run, st := runSQLRecordPipeline(t, []models.Node{
		{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src",
			Config: map[string]interface{}{"uri": uri, "query": template}},
	}, nil)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %q, want success (error: %s)", run.Status, run.Error)
	}

	got := recordedStatements(t, st, run.ID, "src")
	if len(got) != 1 {
		t.Fatalf("recorded %d statement(s) %q, want exactly 1", len(got), got)
	}
	want := "SELECT id, name FROM t WHERE name != '" + run.ID + "'"
	if got[0] != want {
		t.Errorf("recorded statement =\n  %q\nwant\n  %q", got[0], want)
	}
	if strings.Contains(got[0], "${") {
		t.Errorf("recorded statement still holds an unrendered reference: %q", got[0])
	}
}

// The statement worth reading is usually the one that failed, so recording
// has to happen before execution rather than after it.
//
// This doubles as the credential test: the node's URI carries a password,
// the connection is refused, and no payload anywhere in the run may contain
// that password.
func TestRecordedSQL_RecordsAFailedStatementAndNeverTheCredential(t *testing.T) {
	// Both source_db paths. A default engine takes the streamed one; the
	// materialising one is only reached with reference passing off. Each
	// has its own copy of the read, and a mutation that moved the record
	// call after the query SURVIVED a version of this test that covered
	// only the streamed path -- so covering one is not covering the other.
	for _, tc := range []struct {
		name   string
		tweaks []func(*Engine)
	}{
		{"streamed", nil},
		{"batch", []func(*Engine){forceBatch}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertFailedStatementRecordedWithoutCredential(t, tc.tweaks...)
		})
	}
}

func assertFailedStatementRecordedWithoutCredential(t *testing.T, tweaks ...func(*Engine)) {
	t.Helper()
	const password = "canary-password-must-not-be-recorded"
	// Port 1 is not listening, so this fails at connect. The statement was
	// still the statement this node was about to run.
	uri := "postgres://brokoli:" + password + "@127.0.0.1:1/brokoli"

	run, st := runSQLRecordPipeline(t, []models.Node{
		{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src",
			Config: map[string]interface{}{"uri": uri, "query": "SELECT 1 AS one"}},
	}, nil, tweaks...)
	if run.Status != models.RunStatusFailed {
		t.Fatalf("run status = %q, want failed (this test needs the connection to be refused)", run.Status)
	}

	got := recordedStatements(t, st, run.ID, "src")
	if len(got) != 1 || got[0] != "SELECT 1 AS one" {
		t.Fatalf("recorded %q, want exactly [\"SELECT 1 AS one\"] -- a failed statement must still be recorded", got)
	}

	events, err := st.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range events {
		// The whole payload, not just Statement: Error carries wrapped
		// driver messages, which is the other way a URI could reach here.
		if strings.Contains(e.Payload.Statement, password) {
			t.Errorf("event %s recorded the connection password in its statement", e.EventType)
		}
		if strings.Contains(e.Payload.Error, password) {
			t.Errorf("event %s recorded the connection password in its error", e.EventType)
		}
	}
}

// One event per attempt, each stamped with its own attempt number -- the
// point being that a retried node shows what it ran each time, not once.
func TestRecordedSQL_OneEventPerAttempt(t *testing.T) {
	run, st := runSQLRecordPipeline(t, []models.Node{
		{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src", Config: map[string]interface{}{
			"uri": "postgres://brokoli:pw@127.0.0.1:1/brokoli", "query": "SELECT 1 AS one",
			"max_retries": float64(2), "retry_delay": float64(10), "retry_backoff": "fixed",
		}},
	}, nil)
	if run.Status != models.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}

	events, err := st.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var attempts []int
	for _, e := range events {
		if e.EventType == models.AttemptQuery && e.NodeID == "src" {
			if e.Attempt == nil {
				t.Fatal("an attempt.query event carried no attempt number")
			}
			attempts = append(attempts, *e.Attempt)
		}
	}
	want := []int{0, 1, 2}
	if len(attempts) != len(want) {
		t.Fatalf("recorded attempts %v, want %v (one statement per attempt)", attempts, want)
	}
	for i, a := range attempts {
		if a != want[i] {
			t.Errorf("attempt[%d] = %d, want %d", i, a, want[i])
		}
	}
}

// An author-written statement can exceed the bound (a long IN list, a large
// literal), so the recorded copy is bounded and says so where it was cut.
//
// This used to be proven with a sink's generated INSERT. #667 stopped
// recording those, so the vehicle is now an author-written source query --
// the kind of statement that is still recorded and can still be large.
func TestRecordedSQL_TruncatesALargeStatementVisibly(t *testing.T) {
	uri := sqlRecordSourceDB(t, 3)
	query := "SELECT id FROM t WHERE name <> '" + strings.Repeat("x", maxRecordedSQLBytes+4096) + "'"

	run, st := runSQLRecordPipeline(t, []models.Node{
		{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src",
			Config: map[string]interface{}{"uri": uri, "query": query}},
	}, nil)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %q, want success (error: %s)", run.Status, run.Error)
	}

	got := recordedStatements(t, st, run.ID, "src")
	if len(got) != 1 {
		t.Fatalf("recorded %d statement(s) for the source, want 1", len(got))
	}
	stmt := got[0]
	if !strings.Contains(stmt, "statement truncated") {
		t.Fatalf("a %d-byte statement was recorded without a truncation marker; "+
			"the bound is %d bytes", len(stmt), maxRecordedSQLBytes)
	}
	// The recorded text is the bound plus the marker, and nothing like the
	// megabytes the node actually sent.
	if len(stmt) > maxRecordedSQLBytes+200 {
		t.Errorf("recorded statement is %d bytes, want at most the %d-byte bound plus a short marker",
			len(stmt), maxRecordedSQLBytes)
	}
	if len(stmt) < maxRecordedSQLBytes {
		t.Errorf("recorded statement is %d bytes, want the full %d-byte bound before the marker",
			len(stmt), maxRecordedSQLBytes)
	}
}

// A secret substituted into a query must not be written into the event log.
func TestRecordedSQL_MasksASubstitutedSecret(t *testing.T) {
	const secret = "super-secret-token-value"
	t.Setenv("BROKED_SECRET_TOKEN", secret)
	uri := sqlRecordSourceDB(t, 3)

	run, st := runSQLRecordPipeline(t, []models.Node{
		{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src", Config: map[string]interface{}{
			"uri": uri, "query": "SELECT id FROM t WHERE name = '${secret.token}'"}},
	}, nil)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %q, want success (error: %s)", run.Status, run.Error)
	}

	got := recordedStatements(t, st, run.ID, "src")
	if len(got) != 1 {
		t.Fatalf("recorded %d statement(s), want 1", len(got))
	}
	if strings.Contains(got[0], secret) {
		t.Error("the recorded statement contains the secret value the resolver substituted")
	}
	if !strings.Contains(got[0], recordedSecretMask) {
		t.Errorf("recorded statement = %q, want the secret replaced by %q", got[0], recordedSecretMask)
	}
}

// Masking a very short secret would shred the statement while still not
// showing the secret was removed, so the statement is refused instead.
func TestRecordedSQL_RefusesWhenASecretIsTooShortToMask(t *testing.T) {
	t.Setenv("BROKED_SECRET_TOKEN", "abc")
	uri := sqlRecordSourceDB(t, 3)

	run, st := runSQLRecordPipeline(t, []models.Node{
		{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src", Config: map[string]interface{}{
			"uri": uri, "query": "SELECT id FROM t WHERE name = '${secret.token}'"}},
	}, nil)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %q, want success (error: %s)", run.Status, run.Error)
	}

	got := recordedStatements(t, st, run.ID, "src")
	if len(got) != 1 {
		t.Fatalf("recorded %d statement(s), want 1", len(got))
	}
	if got[0] != recordedSQLShortSecretRefusal {
		t.Errorf("recorded statement = %q, want the refusal text", got[0])
	}
}

// Replaying the event log must reconstruct the same run it always did. The
// new events carry no run state, so a projection that has never heard of
// them is still correct -- this is what lets ProjectRun have no case for
// AttemptQuery.
func TestRecordedSQL_ProjectionIsUnaffectedByQueryEvents(t *testing.T) {
	uri := sqlRecordSourceDB(t, 5)

	run, st := runSQLRecordPipeline(t, []models.Node{
		{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src",
			Config: map[string]interface{}{"uri": uri, "query": "SELECT id, name FROM t"}},
	}, nil)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %q, want success (error: %s)", run.Status, run.Error)
	}

	real, err := st.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	events, err := st.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatalf("ListEventsByRun: %v", err)
	}
	queryEvents := 0
	for _, e := range events {
		if e.EventType == models.AttemptQuery {
			queryEvents++
		}
	}
	if queryEvents == 0 {
		t.Fatal("no attempt.query events in the log, so this proves nothing about replaying with them")
	}

	assertRunsEqual(t, real, ProjectRun(run.ID, events))
}

// A dry run executes no statement, so it records none.
func TestRecordedSQL_DryRunRecordsNothing(t *testing.T) {
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "dry.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// runs.pipeline_id is a foreign key, so the run needs a pipeline to
	// belong to. Draft skips executable validation for this stub.
	p := &models.Pipeline{
		ID: "p-dry", Name: "Dry", Enabled: true, Draft: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	run := &models.Run{ID: "run-dry", PipelineID: p.ID, Status: models.RunStatusRunning, StartedAt: ptrTime(time.Now().UTC())}
	if err := st.CreateRun(run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	r := &Runner{store: st, run: run, dryRun: true}
	r.recordExecutedSQL("src", 0, "SELECT 1")
	if got := recordedStatements(t, st, run.ID, "src"); len(got) != 0 {
		t.Errorf("a dry run recorded %q, want nothing", got)
	}

	// The same call on a real run does record, so the assertion above is
	// about the dry-run guard and not about the recorder being inert.
	r.dryRun = false
	r.recordExecutedSQL("src", 0, "SELECT 1")
	if got := recordedStatements(t, st, run.ID, "src"); len(got) != 1 {
		t.Errorf("a real run recorded %q, want one statement", got)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// forceBatch disables ADR-019 reference passing, so a source_db takes the
// materialising path (runSourceDB) instead of the streamed one
// (runSourceDBStreamed).
//
// The value must be NEGATIVE, not zero: nodeOutputs rewrites a zero
// threshold back to the default, so zero would quietly leave streaming on
// and the "batch" case would test the streamed path twice.
func forceBatch(e *Engine) { e.StreamThresholdBytes = -1 }

// A bulk load has no SQL statement, and saying nothing would leave an
// operator unable to tell a broken feature from a path that genuinely has
// none. The absence is recorded as an explicit, factual note instead.
func TestRecordedSQL_BulkWriteRecordsAnExplicitNoStatementNote(t *testing.T) {
	// Both sink paths reach a bulk writer and each carries its own record
	// call. Deleting the materialising path's call SURVIVED a version of
	// this test that exercised only the streamed path, so both are driven
	// here -- covering one sink path is not covering the other.
	for _, tc := range []struct {
		name   string
		table  string
		tweaks []func(*Engine)
	}{
		{"streamed", "brokoli_sqlrec_bulk_s", nil},
		{"batch", "brokoli_sqlrec_bulk_b", []func(*Engine){forceBatch}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertBulkWriteRecordsNote(t, tc.table, tc.tweaks...)
		})
	}
}

func assertBulkWriteRecordsNote(t *testing.T, table string, tweaks ...func(*Engine)) {
	t.Helper()
	pg := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if pg == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", pg)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()
	for _, s := range []string{
		`DROP TABLE IF EXISTS ` + table,
		`CREATE TABLE ` + table + ` (id bigint, name text)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + table) })

	// A sqlite source and a Postgres sink are different servers, so this
	// cannot push down; the sink takes the COPY bulk path, which is the
	// path under test.
	src := sqlRecordSourceDB(t, 10)
	run, st := runSQLRecordPipeline(t, []models.Node{
		{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src",
			Config: map[string]interface{}{"uri": src, "query": "SELECT id, name FROM t"}},
		{ID: "sink", Type: models.NodeTypeSinkDB, Name: "Sink",
			Config: map[string]interface{}{"uri": pg, "table": table, "mode": "append"}},
	}, []models.Edge{{From: "src", To: "sink"}}, tweaks...)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %q, want success (error: %s)", run.Status, run.Error)
	}

	got := recordedStatements(t, st, run.ID, "sink")
	if len(got) != 1 {
		t.Fatalf("recorded %d note(s) for the bulk sink %q, want exactly 1 -- "+
			"a bulk write must record an explicit note, not nothing", len(got), got)
	}
	if !strings.Contains(got[0], "no SQL statement") {
		t.Errorf("recorded %q, want an explicit no-statement note", got[0])
	}
	if !strings.Contains(got[0], "COPY") {
		t.Errorf("recorded %q, want the mechanism named", got[0])
	}
	// It must not invent a statement that never ran.
	if strings.Contains(got[0], "INSERT INTO") {
		t.Errorf("recorded %q, which fabricates a statement the node never sent", got[0])
	}
}

// Masking runs over the whole statement BEFORE it is bounded.
//
// The two orders are distinguishable precisely here: with a short secret
// sitting past the 64 KiB cut, masking first sees it and refuses the whole
// statement, while truncating first would drop it and hand back an ordinary
// truncated statement. Pinning this is the difference between the guard and
// a coincidence, since truncation would otherwise "remove" far-away secrets
// by luck while the mask handled near ones.
func TestRenderRecordedSQL_MasksBeforeTruncating(t *testing.T) {
	const short = "abc"
	past := "SELECT " + strings.Repeat("x", maxRecordedSQLBytes) + " '" + short + "'"
	if got := renderRecordedSQL(past, []string{short}); got != recordedSQLShortSecretRefusal {
		t.Errorf("a secret past the cut was not seen by the mask: got %.90q", got)
	}

	// And a maskable secret before the cut is masked rather than merely
	// surviving into the kept prefix.
	const long = "super-secret-token-value"
	before := "SELECT '" + long + "' " + strings.Repeat("x", maxRecordedSQLBytes)
	got := renderRecordedSQL(before, []string{long})
	if strings.Contains(got, long) {
		t.Error("a secret before the cut survived into the recorded statement")
	}
	if !strings.Contains(got, recordedSecretMask) {
		t.Errorf("recorded %.90q, want the mask", got)
	}
	if !strings.Contains(got, "statement truncated") {
		t.Error("the bound was not applied after masking")
	}
}

// ── Unit tests for the pieces the end-to-end tests exercise indirectly ──

func TestTruncateRecordedSQL(t *testing.T) {
	short := "SELECT 1"
	if got := truncateRecordedSQL(short); got != short {
		t.Errorf("a short statement was altered: %q", got)
	}

	long := strings.Repeat("a", maxRecordedSQLBytes+5000)
	got := truncateRecordedSQL(long)
	if !strings.Contains(got, "statement truncated") {
		t.Error("a long statement was not marked as truncated")
	}
	if !strings.HasPrefix(got, strings.Repeat("a", maxRecordedSQLBytes)) {
		t.Error("truncation did not keep the first maxRecordedSQLBytes bytes")
	}

	// A multi-byte rune straddling the bound must not be cut in half: the
	// payload is JSON, and half a character makes the whole event
	// unreadable rather than just this field.
	runeStraddling := strings.Repeat("a", maxRecordedSQLBytes-1) + "é" + "tail"
	if got := truncateRecordedSQL(runeStraddling); !strings.HasSuffix(
		strings.SplitN(got, "\n-- [brokoli]", 2)[0], "a") {
		t.Errorf("truncation split a rune: kept %q", strings.SplitN(got, "\n-- [brokoli]", 2)[0])
	}
}

func TestMaskRecordedSQL(t *testing.T) {
	stmt := "SELECT * FROM t WHERE token = 'super-secret-token' AND x = 1"

	if got := maskRecordedSQL(stmt, nil); got != stmt {
		t.Errorf("no secrets should leave the statement alone, got %q", got)
	}
	got := maskRecordedSQL(stmt, []string{"super-secret-token"})
	if strings.Contains(got, "super-secret-token") {
		t.Errorf("secret survived masking: %q", got)
	}
	if !strings.Contains(got, recordedSecretMask) {
		t.Errorf("masked statement = %q, want the mask placeholder", got)
	}
	// A secret that is not in the statement changes nothing.
	if got := maskRecordedSQL(stmt, []string{"a-different-long-secret"}); got != stmt {
		t.Errorf("an absent secret altered the statement: %q", got)
	}
	// A short secret present in the statement refuses the whole thing.
	if got := maskRecordedSQL(stmt, []string{"tok"}); got != recordedSQLShortSecretRefusal {
		t.Errorf("a short secret should refuse the statement, got %q", got)
	}
	// A short secret NOT in the statement must not refuse it.
	if got := maskRecordedSQL(stmt, []string{"zzz"}); got != stmt {
		t.Errorf("an absent short secret refused the statement: %q", got)
	}
}

func TestTemplateVarNames(t *testing.T) {
	tmpl := "SELECT ${var.a}, ${param.b}, ${secret.c}, ${var.d|date:YYYY}, ${var.a} FROM t"
	got := templateVarNames(tmpl)
	want := map[string]bool{"a": true, "d": true}
	if len(got) != len(want) {
		t.Fatalf("templateVarNames(%q) = %v, want the two var references", tmpl, got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected variable name %q", n)
		}
	}
}

// fakeVarStore reports one encrypted and one plain variable.
type fakeVarStore struct{}

func (fakeVarStore) GetVariableValue(workspaceID, key string) (string, bool, error) {
	switch key {
	case "secret_one":
		return "an-encrypted-variable-value", true, nil
	case "plain_one":
		return "a-plain-variable-value", false, nil
	}
	return "", false, fmt.Errorf("no such variable")
}

// An encrypted stored variable is a secret; an unencrypted one is not,
// because not encrypting it was a choice.
func TestRecordedSQLSecrets_EncryptedVariablesOnly(t *testing.T) {
	r := &Runner{
		varCtx: &VariableContext{
			Env:  map[string]string{"BROKED_SECRET_TOKEN": "an-env-secret-value", "PATH": "/usr/bin"},
			Vars: fakeVarStore{},
		},
		pipe: &models.Pipeline{Nodes: []models.Node{{
			ID: "src",
			Config: map[string]interface{}{
				"query": "SELECT ${var.secret_one}, ${var.plain_one}, ${secret.token} FROM t",
			},
		}}},
	}

	got := r.recordedSQLSecrets("src")
	has := func(v string) bool {
		for _, g := range got {
			if g == v {
				return true
			}
		}
		return false
	}
	if !has("an-env-secret-value") {
		t.Error("a BROKED_SECRET_* value was not collected")
	}
	if !has("an-encrypted-variable-value") {
		t.Error("an encrypted stored variable was not collected")
	}
	if has("a-plain-variable-value") {
		t.Error("an unencrypted stored variable was collected as a secret")
	}
	if has("/usr/bin") {
		t.Error("an ordinary environment variable was collected as a secret")
	}
}

// The pushdown path composes SQL the engine never showed anyone: the whole
// segment becomes one INSERT ... SELECT, and until now nothing recorded it.
// Needs a real Postgres because pushdown only engages for a dialect with a
// same-server rule.
func TestRecordedSQL_PushdownRecordsItsComposedStatement(t *testing.T) {
	pg := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if pg == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", pg)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()
	for _, s := range []string{
		`DROP TABLE IF EXISTS brokoli_sqlrec_src`,
		`DROP TABLE IF EXISTS brokoli_sqlrec_dst`,
		`CREATE TABLE brokoli_sqlrec_src (id bigint, city text)`,
		`CREATE TABLE brokoli_sqlrec_dst (id bigint, city text)`,
		`INSERT INTO brokoli_sqlrec_src SELECT g, 'city' || g FROM generate_series(1, 50) g`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TABLE IF EXISTS brokoli_sqlrec_src`)
		_, _ = db.Exec(`DROP TABLE IF EXISTS brokoli_sqlrec_dst`)
	})
	t.Setenv("BROKOLI_DATA_PLANE", "")

	// #667: every mode records the composed INSERT ... SELECT, which carries
	// the author's query, and never the DELETE / TRUNCATE an overwrite runs
	// first, which is engine boilerplate. Overwrite is the case that matters:
	// before #667 it recorded two statements.
	for _, tc := range []struct {
		name   string
		config map[string]interface{}
	}{
		{"append", map[string]interface{}{"mode": "append"}},
		{"overwrite-delete", map[string]interface{}{"mode": "overwrite"}},
		{"overwrite-truncate", map[string]interface{}{"mode": "overwrite", "truncate": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sinkCfg := map[string]interface{}{"uri": pg, "table": "brokoli_sqlrec_dst"}
			for k, v := range tc.config {
				sinkCfg[k] = v
			}
			run, st := runSQLRecordPipeline(t, []models.Node{
				{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src", Config: map[string]interface{}{
					"uri": pg, "query": "SELECT id, city FROM brokoli_sqlrec_src"}},
				{ID: "sink", Type: models.NodeTypeSinkDB, Name: "Sink", Config: sinkCfg},
			}, []models.Edge{{From: "src", To: "sink"}})
			if run.Status != models.RunStatusSuccess {
				t.Fatalf("run status = %q, want success (error: %s)", run.Status, run.Error)
			}

			got := recordedStatements(t, st, run.ID, "sink")
			if len(got) != 1 {
				t.Fatalf("recorded %d statement(s) for the pushed-down sink %q, want exactly the composed INSERT", len(got), got)
			}
			if !strings.Contains(got[0], "INSERT INTO") || !strings.Contains(got[0], "brokoli_pushdown") {
				t.Errorf("recorded statement = %q, want the composed INSERT ... SELECT", got[0])
			}
			if !strings.Contains(got[0], "SELECT id, city FROM brokoli_sqlrec_src") {
				t.Errorf("recorded statement = %q, want the author's query embedded in it", got[0])
			}
			for _, clear := range []string{"DELETE FROM", "TRUNCATE"} {
				if strings.Contains(got[0], clear) {
					t.Errorf("recorded %q, which includes the engine-generated clear", got[0])
				}
			}
			if strings.Contains(got[0], pg) {
				t.Error("the composed statement recorded the connection URI")
			}
		})
	}
}

// #667, the issue's own scenario: a source_db feeding a sink_db records the
// author's SELECT, and the sink's engine-generated INSERT ... VALUES is not
// recorded -- an explicit note is, so the sink's panel is not silently empty.
//
// create_table forces the statement path: bulkWriterFor declines it and so
// does pushdown, so the sink really does build and run an INSERT here. The
// row-count check below proves the write happened rather than the test
// passing because nothing ran.
func TestRecordedSQL_GeneratedSinkWriteRecordsANoteNotTheStatement(t *testing.T) {
	uri := sqlRecordSourceDB(t, 25)

	run, st := runSQLRecordPipeline(t, []models.Node{
		{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src",
			Config: map[string]interface{}{"uri": uri, "query": "SELECT id, name FROM t"}},
		{ID: "sink", Type: models.NodeTypeSinkDB, Name: "Sink",
			Config: map[string]interface{}{"uri": uri, "table": "dest", "create_table": true}},
	}, []models.Edge{{From: "src", To: "sink"}}, forceBatch)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %q, want success (error: %s)", run.Status, run.Error)
	}

	db, err := sql.Open("sqlite", uri)
	if err != nil {
		t.Fatalf("open data db: %v", err)
	}
	defer db.Close()
	var written int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dest`).Scan(&written); err != nil || written != 25 {
		t.Fatalf("dest holds %d rows (err %v), want 25: the sink must really have run its generated INSERT", written, err)
	}

	if src := recordedStatements(t, st, run.ID, "src"); len(src) != 1 || src[0] != "SELECT id, name FROM t" {
		t.Errorf("source recorded %q, want exactly the author's SELECT", src)
	}

	sink := recordedStatements(t, st, run.ID, "sink")
	if len(sink) != 1 {
		t.Fatalf("sink recorded %d entr(ies) %q, want exactly one note", len(sink), sink)
	}
	if !strings.HasPrefix(sink[0], recordGeneratedWriteNote) {
		t.Errorf("sink recorded %.120q, want the generated-write note", sink[0])
	}
	for _, generated := range []string{"INSERT INTO", "VALUES", "CREATE TABLE"} {
		if strings.Contains(sink[0], generated) {
			t.Errorf("sink recorded %.120q, which contains engine-generated SQL (%s)", sink[0], generated)
		}
	}
	if !strings.Contains(sink[0], `"dest"`) {
		t.Errorf("sink note %q does not name the table it wrote", sink[0])
	}
}

// SQL a person wrote in a sql_generate node and forwarded to a sink IS
// author-written, so the sink records it. This is the other execSinkSQL
// caller, and the reason the author/generated split has to sit at the call
// sites: both callers hand execSinkSQL a string of SQL, and only the caller
// knows which kind it is.
func TestRecordedSQL_SQLGenerateOutputIsRecordedAtTheSink(t *testing.T) {
	uri := sqlRecordSourceDB(t, 4)

	run, st := runSQLRecordPipeline(t, []models.Node{
		{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src",
			Config: map[string]interface{}{"uri": uri, "query": "SELECT id, name FROM t"}},
		{ID: "gen", Type: models.NodeTypeSQLGenerate, Name: "Gen",
			Config: map[string]interface{}{"dialect": "sqlite", "table": "gen_dest", "create_table": true}},
		{ID: "sink", Type: models.NodeTypeSinkDB, Name: "Sink",
			Config: map[string]interface{}{"uri": uri, "table": "gen_dest"}},
	}, []models.Edge{{From: "src", To: "gen"}, {From: "gen", To: "sink"}}, forceBatch)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %q, want success (error: %s)", run.Status, run.Error)
	}

	sink := recordedStatements(t, st, run.ID, "sink")
	if len(sink) != 1 {
		t.Fatalf("sink recorded %d entr(ies) %q, want the forwarded statement", len(sink), sink)
	}
	if strings.HasPrefix(sink[0], "-- [brokoli]") {
		t.Errorf("sink recorded a note %q, want the sql_generate statement itself", sink[0])
	}
	if !strings.Contains(sink[0], "gen_dest") {
		t.Errorf("sink recorded %.120q, want the statement sql_generate forwarded", sink[0])
	}
}

// The generated-write note must not be classifiable as either existing
// note. A reader that sorts notes by prefix -- the run UI does exactly this
// -- would otherwise label an engine-generated write as a statement withheld
// over a short secret, or as a bulk load with no SQL at all.
func TestRecordGeneratedWriteNote_IsDistinctFromTheOtherNotes(t *testing.T) {
	for _, other := range []string{
		"-- [brokoli] statement not recorded",
		"-- [brokoli] no SQL statement",
		"-- [brokoli] statement truncated",
	} {
		if strings.HasPrefix(recordGeneratedWriteNote, other) || strings.HasPrefix(other, recordGeneratedWriteNote) {
			t.Errorf("generated-write note %q collides with the %q prefix", recordGeneratedWriteNote, other)
		}
	}
	if !strings.HasPrefix(recordGeneratedWriteNote, "-- [brokoli] ") {
		t.Errorf("generated-write note %q is not a -- [brokoli] SQL comment", recordGeneratedWriteNote)
	}
	if strings.HasPrefix(recordedSQLShortSecretRefusal, recordGeneratedWriteNote) {
		t.Error("the short-secret refusal would be classified as a generated-write note")
	}
}
