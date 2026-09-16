package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/templates"
)

// fakeSeedDB stands in for a store's four accessors so the seeding rule
// can be tested directly, including states a real migrate cannot be
// talked into (a marker naming a template no longer in Builtin).
type fakeSeedDB struct {
	settings  map[string]string
	rows      map[string]bool
	created   []string
	setCalls  int
	createErr error
}

func newFakeSeedDB(marker string, rowIDs ...string) *fakeSeedDB {
	f := &fakeSeedDB{settings: map[string]string{}, rows: map[string]bool{}}
	if marker != "" {
		f.settings[templateSeedMarkerKey] = marker
	}
	for _, id := range rowIDs {
		f.rows[id] = true
	}
	return f
}

func (f *fakeSeedDB) get(key string) (string, error) { return f.settings[key], nil }

func (f *fakeSeedDB) set(key, value string) error {
	f.setCalls++
	f.settings[key] = value
	return nil
}

func (f *fakeSeedDB) list() ([]models.PipelineTemplate, error) {
	out := make([]models.PipelineTemplate, 0, len(f.rows))
	for id := range f.rows {
		out = append(out, models.PipelineTemplate{ID: id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeSeedDB) create(t *models.PipelineTemplate) error {
	if f.createErr != nil {
		return f.createErr
	}
	if f.rows[t.ID] {
		return fmt.Errorf("duplicate template %q", t.ID)
	}
	f.rows[t.ID] = true
	f.created = append(f.created, t.ID)
	return nil
}

func (f *fakeSeedDB) seed() error {
	return seedBuiltinTemplates(f.get, f.set, f.list, f.create)
}

func (f *fakeSeedDB) marker() string { return f.settings[templateSeedMarkerKey] }

func markerHas(marker, id string) bool { return parseSeedMarker(marker)[id] }

func builtinIDs() []string {
	ids := make([]string, 0, len(templates.Builtin))
	for _, t := range templates.Builtin {
		ids = append(ids, t.ID)
	}
	return ids
}

func TestSeedBuiltinTemplates_FreshDatabaseSeedsEveryBuiltin(t *testing.T) {
	f := newFakeSeedDB("")
	if err := f.seed(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got, want := strings.Join(f.created, ","), strings.Join(builtinIDs(), ","); got != want {
		t.Errorf("created %q, want every builtin in Builtin order %q", got, want)
	}
	if got, want := f.marker(), strings.Join(builtinIDs(), ","); got != want {
		t.Errorf("marker = %q, want %q", got, want)
	}
}

// The production case: seeded long ago with a smaller Builtin, so the
// table is not empty and the old rule skipped every later addition.
func TestSeedBuiltinTemplates_LegacyInstallGainsBuiltinsAddedSince(t *testing.T) {
	first := templates.Builtin[0].ID
	f := newFakeSeedDB("", first, "blank")
	if err := f.seed(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	want := builtinIDs()[1:]
	if got := strings.Join(f.created, ","); got != strings.Join(want, ",") {
		t.Errorf("created %q, want the builtins added since the first seed %q", got, strings.Join(want, ","))
	}
	for _, id := range f.created {
		if id == first {
			t.Errorf("re-inserted %q, which was already in the table", first)
		}
	}
	for _, id := range append(builtinIDs(), "blank") {
		if !markerHas(f.marker(), id) {
			t.Errorf("marker %q missing %q; a row present at first marking must count as offered", f.marker(), id)
		}
	}
}

func TestSeedBuiltinTemplates_DeletionSurvivesOnceRecorded(t *testing.T) {
	ids := builtinIDs()
	deleted := ids[len(ids)-1]
	f := newFakeSeedDB(strings.Join(ids, ","), ids[:len(ids)-1]...)
	if err := f.seed(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(f.created) != 0 {
		t.Errorf("created %v, want nothing: %q was deleted by an admin after being offered", f.created, deleted)
	}
	if f.setCalls != 0 {
		t.Errorf("wrote the marker %d times, want 0 when nothing changed", f.setCalls)
	}
}

func TestSeedBuiltinTemplates_RecordsExistingRowWithoutInserting(t *testing.T) {
	ids := builtinIDs()
	if len(ids) < 2 {
		t.Skip("needs at least two builtins")
	}
	// The marker knows the first; the second exists but was never recorded.
	f := newFakeSeedDB(ids[0], ids[0], ids[1])
	if err := f.seed(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, id := range f.created {
		if id == ids[1] {
			t.Fatalf("inserted %q over an existing row", ids[1])
		}
	}
	if !markerHas(f.marker(), ids[1]) {
		t.Errorf("marker %q must record %q so a later deletion sticks", f.marker(), ids[1])
	}
}

func TestSeedBuiltinTemplates_MarkerKeepsIDsThatLeftBuiltin(t *testing.T) {
	f := newFakeSeedDB("retired-starter,"+builtinIDs()[0], builtinIDs()[0])
	if err := f.seed(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !markerHas(f.marker(), "retired-starter") {
		t.Errorf("marker = %q, want it to keep 'retired-starter': dropping it would resurrect the template if it returned to Builtin", f.marker())
	}
}

func TestSeedBuiltinTemplates_CreateFailureLeavesMarkerUnwritten(t *testing.T) {
	f := newFakeSeedDB("")
	f.createErr = fmt.Errorf("disk on fire")
	err := f.seed()
	if err == nil {
		t.Fatal("expected the create error to surface")
	}
	if !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("error = %v, want it to wrap the create failure", err)
	}
	if _, ok := f.settings[templateSeedMarkerKey]; ok {
		t.Error("marker was written despite the failed insert; the next start would skip that template forever")
	}
}

func TestFormatSeedMarker_IsStable(t *testing.T) {
	ids := map[string]bool{"zeta": true, "alpha": true}
	for _, tmpl := range templates.Builtin {
		ids[tmpl.ID] = true
	}
	first := formatSeedMarker(ids)
	for i := 0; i < 20; i++ {
		if got := formatSeedMarker(ids); got != first {
			t.Fatalf("run %d produced %q, want the stable %q", i, got, first)
		}
	}
	if !strings.HasPrefix(first, strings.Join(builtinIDs(), ",")) {
		t.Errorf("marker %q should start with Builtin's own order", first)
	}
	if !strings.HasSuffix(first, "alpha,zeta") {
		t.Errorf("marker %q should end with the non-builtin ids sorted", first)
	}
}

// --- store level, through a real migrate ---

func TestSQLiteStore_PipelineTemplates_RecordsSeedMarker(t *testing.T) {
	s := newTemplateSQLiteStore(t)
	marker, err := s.GetSetting(templateSeedMarkerKey)
	if err != nil {
		t.Fatalf("get marker: %v", err)
	}
	if marker == "" {
		t.Fatal("a fresh database must record what it was seeded with")
	}
	for _, id := range builtinIDs() {
		if !markerHas(marker, id) {
			t.Errorf("marker %q missing seeded template %q", marker, id)
		}
	}
}

// A database seeded before the marker existed, which is every install
// created before this change: rows present, no marker, and built-ins
// added since still missing.
func TestSQLiteStore_PipelineTemplates_LegacyDatabaseGainsNewBuiltins(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	keep := templates.Builtin[0].ID
	for _, id := range builtinIDs()[1:] {
		if err := s.DeletePipelineTemplate(id); err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
	}
	if err := s.SetSetting(templateSeedMarkerKey, ""); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	s.Close()

	s2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s2.Close()

	list, err := s2.ListPipelineTemplates()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]bool{}
	for _, tmpl := range list {
		got[tmpl.ID] = true
	}
	for _, id := range builtinIDs() {
		if !got[id] {
			t.Errorf("template %q still missing after restart; a legacy install must gain builtins added since it was seeded", id)
		}
	}
	if !got[keep] {
		t.Errorf("the template already present (%q) was lost", keep)
	}
}

func TestPostgresStore_PipelineTemplates_LegacyDatabaseGainsNewBuiltins(t *testing.T) {
	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	s, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	keep := templates.Builtin[0].ID
	for _, id := range builtinIDs()[1:] {
		_ = s.DeletePipelineTemplate(id)
	}
	if err := s.SetSetting(templateSeedMarkerKey, ""); err != nil {
		t.Fatalf("clear marker: %v", err)
	}

	s2, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s2.Close()

	list, err := s2.ListPipelineTemplates()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]bool{}
	for _, tmpl := range list {
		got[tmpl.ID] = true
	}
	for _, id := range builtinIDs() {
		if !got[id] {
			t.Errorf("template %q still missing after restart on Postgres", id)
		}
	}
	if !got[keep] {
		t.Errorf("the template already present (%q) was lost", keep)
	}
}
