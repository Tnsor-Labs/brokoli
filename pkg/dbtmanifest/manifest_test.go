package dbtmanifest

import (
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures are real manifests, written by real dbt, from two versions
// deliberately chosen to differ. ADR-025 requires more than one because a
// parser checked against a single version is correct against the thing in
// front of it and wrong against the thing it ships to -- the same defect
// class as #348.
//
// What makes this pair worth having: dbt 1.8.9 and 1.10.11 both write
// schema v12, and 1.10.11's nodes carry doc_blocks, primary_key and
// time_spine, which 1.8.9's do not. The schema version stayed still while
// the shape moved. A reader that trusted the version number alone would
// have no idea.
var fixtures = map[string]string{
	"dbt-1.8.9":   filepath.Join("testdata", "manifest-v12-dbt-1.8.9.json"),
	"dbt-1.10.11": filepath.Join("testdata", "manifest-v12-dbt-1.10.11.json"),
}

func TestParsesEveryFixtureVersionIdentically(t *testing.T) {
	type shape struct {
		models, seeds, tests int
		cityTotalsDeps       []string
		stgOrdersMaterial    string
	}
	shapes := map[string]shape{}

	for version, path := range fixtures {
		p, err := ParseFile(path)
		if err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		if p.SchemaVersion != 12 {
			t.Errorf("%s: schema version = %d, want 12", version, p.SchemaVersion)
		}
		if !strings.HasPrefix(p.DBTVersion, strings.TrimPrefix(version, "dbt-")[:3]) {
			t.Errorf("%s: manifest reports dbt %q", version, p.DBTVersion)
		}
		if p.ProjectName != "brokoli_fixture" {
			t.Errorf("%s: project = %q", version, p.ProjectName)
		}

		var deps []string
		var material string
		for _, m := range p.Models() {
			switch m.Name {
			case "city_totals":
				deps = m.DependsOn
			case "stg_orders":
				material = m.Materialization
			}
		}
		shapes[version] = shape{
			models: len(p.Models()), seeds: len(p.Seeds()), tests: len(p.Tests()),
			cityTotalsDeps: deps, stgOrdersMaterial: material,
		}
	}

	// The point of two fixtures: the DAG they describe must be the same
	// project, whichever dbt wrote it. A difference here is the parser
	// reading one version's shape and not the other's.
	var first string
	for version, s := range shapes {
		if first == "" {
			first = version
			continue
		}
		want := shapes[first]
		if s.models != want.models || s.seeds != want.seeds || s.tests != want.tests {
			t.Errorf("%s and %s disagree on the project: %+v vs %+v", first, version, want, s)
		}
		if strings.Join(s.cityTotalsDeps, ",") != strings.Join(want.cityTotalsDeps, ",") {
			t.Errorf("%s and %s disagree on city_totals deps: %v vs %v",
				first, version, want.cityTotalsDeps, s.cityTotalsDeps)
		}
		if s.stgOrdersMaterial != want.stgOrdersMaterial {
			t.Errorf("%s and %s disagree on materialization: %q vs %q",
				first, version, want.stgOrdersMaterial, s.stgOrdersMaterial)
		}
	}
}

// The DAG itself: the fixture project is seed -> stg_orders -> city_totals,
// plus a standalone model. Reading the edges is the whole reason this
// package exists, so it is asserted rather than assumed.
func TestReadsTheModelDAG(t *testing.T) {
	p, err := ParseFile(fixtures["dbt-1.8.9"])
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]Node{}
	for _, n := range p.Nodes {
		byName[n.Name] = n
	}

	city, ok := byName["city_totals"]
	if !ok {
		t.Fatal("city_totals missing")
	}
	if len(city.DependsOn) != 1 || !strings.HasSuffix(city.DependsOn[0], "stg_orders") {
		t.Errorf("city_totals depends on %v, want stg_orders", city.DependsOn)
	}
	stg, ok := byName["stg_orders"]
	if !ok {
		t.Fatal("stg_orders missing")
	}
	if len(stg.DependsOn) != 1 || !strings.HasSuffix(stg.DependsOn[0], "raw_orders") {
		t.Errorf("stg_orders depends on %v, want the seed", stg.DependsOn)
	}
	// A node with no dependencies must come back with an empty slice, not
	// a nil that a caller has to guard. A real manifest omits the key.
	af, ok := byName["always_fails"]
	if !ok {
		t.Fatal("always_fails missing")
	}
	if af.DependsOn == nil {
		t.Error("DependsOn must be empty, never nil")
	}
	if len(af.DependsOn) != 0 {
		t.Errorf("always_fails depends on %v, want nothing", af.DependsOn)
	}

	// A built model has to be addressable for ADR-023 composition, which
	// needs the relation dbt itself would write.
	if city.RelationName == "" {
		t.Error("city_totals has no relation name; it could not be referenced as a TableRef")
	}
	if city.Materialization == "" {
		t.Error("city_totals has no materialization")
	}
}

// Build order is what turns a manifest into a schedule: every node must
// follow everything it depends on.
func TestBuildOrderRespectsDependencies(t *testing.T) {
	for version, path := range fixtures {
		p, err := ParseFile(path)
		if err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		order, err := p.BuildOrder()
		if err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		if len(order) != len(p.Nodes) {
			t.Fatalf("%s: build order has %d of %d nodes", version, len(order), len(p.Nodes))
		}
		seen := map[string]bool{}
		for _, n := range order {
			for _, dep := range n.DependsOn {
				if _, inManifest := p.Nodes[dep]; !inManifest {
					continue
				}
				if !seen[dep] {
					t.Errorf("%s: %s is ordered before its dependency %s", version, n.UniqueID, dep)
				}
			}
			seen[n.UniqueID] = true
		}
	}
}

// A cycle is reported rather than looping or silently dropping a node.
func TestBuildOrderReportsCycles(t *testing.T) {
	p := &Project{Nodes: map[string]Node{
		"a": {UniqueID: "a", DependsOn: []string{"b"}},
		"b": {UniqueID: "b", DependsOn: []string{"a"}},
	}}
	_, err := p.BuildOrder()
	if err == nil {
		t.Fatal("a cycle must be reported")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should name the problem: %v", err)
	}
}

// A dependency on something outside the manifest -- a disabled model, or one
// from a package -- must not stop the rest of the DAG being ordered.
func TestBuildOrderIgnoresDependenciesOutsideTheManifest(t *testing.T) {
	p := &Project{Nodes: map[string]Node{
		"model.p.a": {UniqueID: "model.p.a", DependsOn: []string{"model.other_package.x"}},
	}}
	order, err := p.BuildOrder()
	if err != nil {
		t.Fatalf("an external dependency must not fail ordering: %v", err)
	}
	if len(order) != 1 {
		t.Fatalf("order has %d nodes, want 1", len(order))
	}
}

// The refusal that keeps this reader honest about reading someone else's
// format: an unrecognised schema version is named, together with the dbt
// that wrote it, rather than parsed into a shape that may have moved.
func TestRefusesUnknownSchemaVersion(t *testing.T) {
	const future = `{"metadata":{"dbt_schema_version":"https://schemas.getdbt.com/dbt/manifest/v99.json",
		"dbt_version":"2.5.0","project_name":"p"},"nodes":{}}`
	_, err := Parse(strings.NewReader(future))
	if err == nil {
		t.Fatal("an unsupported schema version must be refused")
	}
	for _, want := range []string{"v99", "2.5.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should name %q, got: %v", want, err)
		}
	}
}

func TestRefusesSomethingThatIsNotAManifest(t *testing.T) {
	for name, body := range map[string]string{
		"no schema version": `{"metadata":{"dbt_version":"1.8.9"},"nodes":{}}`,
		"odd schema url":    `{"metadata":{"dbt_schema_version":"not-a-url"},"nodes":{}}`,
		"not json":          `this is not json`,
	} {
		if _, err := Parse(strings.NewReader(body)); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// Forward compatibility, stated as a test rather than trusted: the newer
// fixture carries fields the older does not, and reading it must neither
// fail nor be affected by them.
func TestUnknownFieldsAreIgnored(t *testing.T) {
	withExtras := `{
		"metadata":{"dbt_schema_version":"https://schemas.getdbt.com/dbt/manifest/v12.json",
			"dbt_version":"1.99.0","project_name":"p","some_new_metadata":{"a":1}},
		"nodes":{"model.p.a":{"unique_id":"model.p.a","name":"a","resource_type":"model",
			"depends_on":{"nodes":[]},"config":{"materialized":"table"},
			"time_spine":null,"primary_key":["id"],"doc_blocks":[],"a_field_from_2030":42}},
		"a_new_top_level_section":{}
	}`
	p, err := Parse(strings.NewReader(withExtras))
	if err != nil {
		t.Fatalf("fields this reader does not know must be ignored, not fatal: %v", err)
	}
	if len(p.Models()) != 1 {
		t.Fatalf("models = %d, want 1", len(p.Models()))
	}
	if p.Models()[0].Materialization != "table" {
		t.Errorf("materialization = %q", p.Models()[0].Materialization)
	}
}

// Ordering must not depend on Go's map iteration, or the same project would
// produce a different node list between runs.
func TestOrderIsStable(t *testing.T) {
	p, err := ParseFile(fixtures["dbt-1.8.9"])
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.BuildOrder()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := p.BuildOrder()
		if err != nil {
			t.Fatal(err)
		}
		for j := range first {
			if first[j].UniqueID != again[j].UniqueID {
				t.Fatalf("build order is not stable: position %d was %s, now %s",
					j, first[j].UniqueID, again[j].UniqueID)
			}
		}
	}
	models := p.Models()
	for i := 0; i < 20; i++ {
		again := p.Models()
		for j := range models {
			if models[j].UniqueID != again[j].UniqueID {
				t.Fatal("Models() is not stable")
			}
		}
	}
}

// Parsing a manifest is on the path of every dbt-backed pipeline run, so its
// cost is worth knowing rather than assuming. Real manifests, both versions.
func BenchmarkParseManifest(b *testing.B) {
	for version, path := range fixtures {
		b.Run(version, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				p, err := ParseFile(path)
				if err != nil {
					b.Fatal(err)
				}
				if len(p.Nodes) == 0 {
					b.Fatal("no nodes")
				}
			}
		})
	}
}

func BenchmarkBuildOrder(b *testing.B) {
	p, err := ParseFile(fixtures["dbt-1.8.9"])
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := p.BuildOrder(); err != nil {
			b.Fatal(err)
		}
	}
}
