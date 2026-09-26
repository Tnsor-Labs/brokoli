package engine

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Column lineage had no tests at all before this file. Not weak ones --
// none: no test in the repository referenced ColumnEdges, Confidence or
// MappingReason. That is how a hardcoded 0.7 on every edge survived to
// be rendered in the UI as "70% inferred".

// --- the coverage gate (ADR-039 part 3) ---

// Every node type declares its column mapping or declares itself opaque.
//
// Neither is a failure. Silence is: a node type with no declaration
// produces a graph that quietly omits it, and nobody finds out until
// somebody asks the graph a question it answered wrongly.
func TestEveryNodeTypeDeclaresItsColumnLineage(t *testing.T) {
	if len(models.AllNodeTypes) == 0 {
		t.Fatal("AllNodeTypes is empty; this gate would pass for anything")
	}

	var missing []string
	for _, nt := range models.AllNodeTypes {
		if _, ok := columnLineageByType[nt]; !ok {
			missing = append(missing, string(nt))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d node type(s) declare no column lineage: %v\n\n"+
			"Add an entry to columnLineageByType. If the node cannot trace columns -- it runs "+
			"user code, or calls out to another tool -- declare it opaque with the reason, which "+
			"is a real answer and is rendered as one. What is not allowed is saying nothing.\n"+
			"Currently declared: %v",
			len(missing), missing, declaredNodeTypes())
	}

	// The other direction: a declaration for a type that no longer
	// exists is a stale entry whose reason nobody will revisit.
	for declared := range columnLineageByType {
		if !models.IsKnownNodeType(declared) {
			t.Errorf("columnLineageByType declares %q, which is not a known node type", declared)
		}
	}
}

// Every opaque declaration states a reason, because the graph shows it
// to a person. "This node cannot say" with no explanation is the gap the
// declaration was supposed to close.
func TestEveryOpaqueDeclarationGivesAReason(t *testing.T) {
	for _, nt := range models.AllNodeTypes {
		declare, ok := columnLineageByType[nt]
		if !ok {
			continue // reported by the gate above
		}
		got := declare(ColumnLineageRequest{Node: models.Node{ID: "n", Type: nt}})
		if got.Opaque && strings.TrimSpace(got.Reason) == "" {
			t.Errorf("%s declares itself opaque with no reason", nt)
		}
		if !got.Opaque && got.Reason != "" {
			t.Errorf("%s is not opaque but carries an opacity reason %q", nt, got.Reason)
		}
		if got.Opaque && len(got.Derivations) > 0 {
			t.Errorf("%s declares itself opaque and also returns %d derivation(s); "+
				"a node that cannot trace columns must not also describe them",
				nt, len(got.Derivations))
		}
	}
}

// Opacity is enforced by the graph, not only by each declarer's good
// behaviour.
//
// Every declarer today returns an empty derivation list alongside
// Opaque, so the graph's skip is currently unreachable -- a mutation
// removing it changes nothing and no test notices. That makes the
// guarantee a convention rather than a rule, and the next declarer to
// return both would publish exactly the confident-looking edges through
// a black box that ADR-039 exists to stop.
//
// So: a declarer that returns both, and the graph must still draw
// nothing.
func TestAnOpaqueNodeDrawsNoEdgesEvenIfItReturnsSome(t *testing.T) {
	const victim = models.NodeTypeCode
	original := columnLineageByType[victim]
	t.Cleanup(func() { columnLineageByType[victim] = original })

	columnLineageByType[victim] = func(req ColumnLineageRequest) ColumnLineage {
		var d []ColumnDerivation
		for _, in := range req.Inputs {
			for _, c := range in.Columns {
				d = append(d, ColumnDerivation{
					Output: c, From: []ColumnRef{{Node: in.Node, Column: c}},
					Evidence: EvidenceDeclared, Rule: "a declarer that contradicts itself",
				})
			}
		}
		return ColumnLineage{Opaque: true, Reason: "cannot say", Derivations: d}
	}

	graph := BuildLineageGraphWithProfiles([]models.Pipeline{{
		ID: "p1", Name: "p1",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Config: map[string]interface{}{"path": "/in.csv"}},
			{ID: "code", Type: victim, Config: map[string]interface{}{"code": "..."}},
		},
		Edges: []models.Edge{{From: "src", To: "code"}},
	}}, map[string]LineageProfile{
		profileKey("p1", "src"): profileWith("a", "b"),
	})

	for _, e := range graph.ColumnEdges {
		if strings.HasSuffix(e.To, ":code") {
			t.Errorf("an opaque node's derivations reached the graph: %+v", e)
		}
	}
}

// An unregistered type is opaque rather than silently edge-free, so a
// build that does not know a node still renders a graph that says so.
func TestAnUnknownNodeTypeIsOpaqueNotSilent(t *testing.T) {
	got := ColumnLineageFor(ColumnLineageRequest{
		Node:   models.Node{ID: "n", Type: models.NodeType("invented_later")},
		Inputs: []NodeInput{{Node: "up", Columns: []string{"a", "b"}}},
	})
	if !got.Opaque {
		t.Fatal("an unregistered node type was not opaque")
	}
	if !strings.Contains(got.Reason, "invented_later") {
		t.Errorf("the reason does not name the type: %q", got.Reason)
	}
	if len(got.Derivations) != 0 {
		t.Errorf("an opaque node produced %d derivation(s)", len(got.Derivations))
	}
}

// --- the headline fix ---

// The case ADR-039 opens with. "total = price * qty" produces a column
// that exists on neither input, so the old name match emitted no edge
// for it: the strongest fact in the pipeline was the one thing the graph
// could not say.
func TestADerivedColumnNamesWhatItDerivesFrom(t *testing.T) {
	got := transformColumns(ColumnLineageRequest{
		Node: transformNode(map[string]interface{}{
			"type": "add_column", "name": "total", "expression": "price * qty",
		}),
		Inputs: []NodeInput{{Node: "up", Columns: []string{"price", "qty", "sku"}}},
	})
	if got.Opaque {
		t.Fatalf("a structured transform came back opaque: %s", got.Reason)
	}

	d := derivationFor(t, got, "total")
	if d.Evidence != EvidenceDeclared {
		t.Errorf("evidence = %q, want declared: the expression states this exactly", d.Evidence)
	}
	if d.Rule != "price * qty" {
		t.Errorf("rule = %q, want the expression itself", d.Rule)
	}
	if cols := refColumns(d.From); !equalStrings(cols, []string{"price", "qty"}) {
		t.Errorf("total derives from %v, want [price qty]", cols)
	}

	// The untouched columns still pass through.
	for _, c := range []string{"price", "qty", "sku"} {
		if _, ok := findDerivation(got, c); !ok {
			t.Errorf("%s disappeared from the output", c)
		}
	}
}

// The second defect: two unrelated datasets that both have "id" were
// joined by an edge asserting a derivation nobody performed. A code node
// between them now draws nothing at all.
func TestACoincidentalNameMatchNoLongerBecomesAnEdge(t *testing.T) {
	got := ColumnLineageFor(ColumnLineageRequest{
		Node:   models.Node{ID: "c", Type: models.NodeTypeCode},
		Inputs: []NodeInput{{Node: "up", Columns: []string{"id", "name"}}},
	})
	if !got.Opaque {
		t.Fatal("a code node claimed to know where its columns came from")
	}
	if len(got.Derivations) != 0 {
		t.Errorf("a code node produced %d column derivation(s)", len(got.Derivations))
	}
	if !strings.Contains(got.Reason, "user code") {
		t.Errorf("reason = %q, want it to say why", got.Reason)
	}
}

// --- transform rules ---

func TestTransformRuleMappings(t *testing.T) {
	in := []NodeInput{{Node: "up", Columns: []string{"qty", "price", "sku"}}}

	t.Run("rename", func(t *testing.T) {
		got := transformColumns(ColumnLineageRequest{
			Node: transformNode(map[string]interface{}{
				"type": "rename", "mapping": map[string]interface{}{"qty": "quantity"},
			}),
			Inputs: in,
		})
		d := derivationFor(t, got, "quantity")
		if !equalStrings(refColumns(d.From), []string{"qty"}) {
			t.Errorf("quantity derives from %v, want [qty]", refColumns(d.From))
		}
		if d.Rule != "renamed from qty" {
			t.Errorf("rule = %q", d.Rule)
		}
		if _, ok := findDerivation(got, "qty"); ok {
			t.Error("the old name survived the rename")
		}
	})

	t.Run("drop", func(t *testing.T) {
		got := transformColumns(ColumnLineageRequest{
			Node: transformNode(map[string]interface{}{
				"type": "drop_columns", "columns": []interface{}{"sku"},
			}),
			Inputs: in,
		})
		if _, ok := findDerivation(got, "sku"); ok {
			t.Error("a dropped column is still in the output")
		}
		if _, ok := findDerivation(got, "qty"); !ok {
			t.Error("dropping sku also removed qty")
		}
	})

	t.Run("aggregate replaces the column set", func(t *testing.T) {
		got := transformColumns(ColumnLineageRequest{
			Node: transformNode(map[string]interface{}{
				"type": "aggregate", "group_by": []interface{}{"sku"},
				"agg_fields": []interface{}{
					map[string]interface{}{"column": "qty", "function": "sum", "alias": "total_qty"},
				},
			}),
			Inputs: in,
		})
		if _, ok := findDerivation(got, "price"); ok {
			t.Error("a column that survives no aggregation is still in the output")
		}
		d := derivationFor(t, got, "total_qty")
		if !equalStrings(refColumns(d.From), []string{"qty"}) {
			t.Errorf("total_qty derives from %v, want [qty]", refColumns(d.From))
		}
		if d.Rule != "sum(qty)" {
			t.Errorf("rule = %q, want sum(qty)", d.Rule)
		}
		if _, ok := findDerivation(got, "sku"); !ok {
			t.Error("the group-by key is not in the output")
		}
	})

	t.Run("rules apply in order", func(t *testing.T) {
		// A rename followed by an add_column referencing the NEW name has
		// to resolve through the rename, or the derived column loses its
		// origin.
		got := transformColumns(ColumnLineageRequest{
			Node: transformNode(
				map[string]interface{}{"type": "rename", "mapping": map[string]interface{}{"qty": "quantity"}},
				map[string]interface{}{"type": "add_column", "name": "total", "expression": "price * quantity"},
			),
			Inputs: in,
		})
		d := derivationFor(t, got, "total")
		if !equalStrings(refColumns(d.From), []string{"price", "qty"}) {
			t.Errorf("total derives from %v, want [price qty]: the rename must be followed through", refColumns(d.From))
		}
	})

	t.Run("row-level rules leave columns alone", func(t *testing.T) {
		for _, rule := range []string{"filter_rows", "sort", "deduplicate"} {
			got := transformColumns(ColumnLineageRequest{
				Node:   transformNode(map[string]interface{}{"type": rule, "column": "qty"}),
				Inputs: in,
			})
			if got.Opaque {
				t.Errorf("%s came back opaque: %s", rule, got.Reason)
				continue
			}
			if len(got.Derivations) != 3 {
				t.Errorf("%s changed the column count to %d", rule, len(got.Derivations))
			}
		}
	})

	t.Run("an unrecognised rule makes the whole node opaque", func(t *testing.T) {
		got := transformColumns(ColumnLineageRequest{
			Node: transformNode(
				map[string]interface{}{"type": "rename", "mapping": map[string]interface{}{"qty": "quantity"}},
				map[string]interface{}{"type": "some_future_rule"},
			),
			Inputs: in,
		})
		if !got.Opaque {
			t.Fatal("a rule this build cannot read did not make the node opaque")
		}
		// Not per-rule: stopping at the unknown rule would leave the
		// rename's mapping standing as though the unknown rule had not run.
		if len(got.Derivations) != 0 {
			t.Errorf("an opaque transform still reported %d derivation(s)", len(got.Derivations))
		}
		if !strings.Contains(got.Reason, "some_future_rule") {
			t.Errorf("the reason does not name the rule: %q", got.Reason)
		}
	})
}

// normaliseTransformType must recognise every rule name applyRule
// accepts. A name this misses is a rule whose effect on columns is
// silently ignored -- the transform keeps reporting the pre-rule column
// set as though the rule were a no-op.
//
// Read out of transform.go rather than listed here, because a list here
// is the same rot the ADR is about.
func TestTransformAliasesMatchTheExecutor(t *testing.T) {
	src, err := os.ReadFile("transform.go")
	if err != nil {
		t.Fatalf("reading transform.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func applyRule(")
	if start < 0 {
		t.Fatal("applyRule not found in transform.go; this test can no longer verify anything")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of applyRule")
	}
	body = body[start : start+end]

	caseLine := regexp.MustCompile(`case ((?:"[a-z_]+"(?:, )?)+):`)
	literal := regexp.MustCompile(`"([a-z_]+)"`)

	var names []string
	for _, m := range caseLine.FindAllStringSubmatch(body, -1) {
		for _, l := range literal.FindAllStringSubmatch(m[1], -1) {
			names = append(names, l[1])
		}
	}
	if len(names) == 0 {
		t.Fatal("no rule names extracted from applyRule; this test would pass for anything")
	}
	t.Logf("applyRule accepts %d rule name(s): %v", len(names), names)

	// Every executor rule name must normalise to something the lineage
	// switch handles. Normalising to itself means it fell through the
	// default, which is the silent case.
	handled := map[string]bool{
		"rename": true, "add_column": true, "filter": true, "function": true,
		"replace": true, "drop": true, "sort": true, "dedup": true, "aggregate": true,
		"project": true,
	}
	for _, name := range names {
		if !handled[normaliseTransformType(name)] {
			t.Errorf("applyRule accepts %q but the lineage declarer does not handle it "+
				"(normalises to %q). Its effect on columns would be silently ignored.",
				name, normaliseTransformType(name))
		}
	}
}

// --- join and union ---

// The declaration must agree with what JoinDatasets actually produces.
// Two hand-maintained descriptions of the same rule drift, and the
// symptom is a lineage graph naming columns the join never emitted, so
// this runs the real join and compares.
func TestJoinColumnLineageMatchesTheJoin(t *testing.T) {
	for _, tc := range []struct {
		name                string
		leftKey, rightKey   string
		leftCols, rightCols []string
	}{
		{"same key name", "id", "id", []string{"id", "name"}, []string{"id", "total"}},
		{"different key names", "id", "cust_id", []string{"id", "name"}, []string{"cust_id", "total"}},
		{"colliding non-key column", "id", "cust_id", []string{"id", "name"}, []string{"cust_id", "name"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left := datasetWith(tc.leftCols)
			right := datasetWith(tc.rightCols)

			joined, err := JoinDatasets(left, right, tc.leftKey, tc.rightKey, ParseJoinType("inner"))
			if err != nil {
				t.Fatalf("JoinDatasets: %v", err)
			}

			got := joinColumns(ColumnLineageRequest{
				Node: models.Node{ID: "j", Type: models.NodeTypeJoin, Config: map[string]interface{}{
					"left_key": tc.leftKey, "right_key": tc.rightKey,
				}},
				Inputs: []NodeInput{
					{Node: "L", Columns: tc.leftCols},
					{Node: "R", Columns: tc.rightCols},
				},
			})
			if got.Opaque {
				t.Fatalf("the join came back opaque: %s", got.Reason)
			}

			var declared []string
			for _, d := range got.Derivations {
				declared = append(declared, d.Output)
			}
			if !equalStrings(declared, joined.Columns) {
				t.Errorf("declared columns %v, but the join produced %v", declared, joined.Columns)
			}
		})
	}
}

func TestJoinColumnLineageUsesAliasOutputSchema(t *testing.T) {
	got := joinColumns(ColumnLineageRequest{
		Node: models.Node{ID: "j", Type: models.NodeTypeJoin, Config: map[string]interface{}{
			"left_key": "id", "right_key": "id",
			"collision_policy": "alias", "right_alias": "customer",
		}},
		Inputs: []NodeInput{
			{Node: "L", Columns: []string{"id", "name"}},
			{Node: "R", Columns: []string{"id", "name"}},
		},
	})
	if got.Opaque {
		t.Fatalf("the alias join came back opaque: %s", got.Reason)
	}
	var columns []string
	for _, derivation := range got.Derivations {
		columns = append(columns, derivation.Output)
	}
	if want := []string{"id", "name", "customer_name"}; !equalStrings(columns, want) {
		t.Fatalf("lineage columns = %v, want %v", columns, want)
	}
}

func TestJoinColumnLineageRejectsInvalidCollisionPolicy(t *testing.T) {
	got := joinColumns(ColumnLineageRequest{
		Node: models.Node{ID: "j", Type: models.NodeTypeJoin, Config: map[string]interface{}{
			"left_key": "id", "right_key": "id", "collision_policy": "rename",
		}},
		Inputs: []NodeInput{
			{Node: "L", Columns: []string{"id"}},
			{Node: "R", Columns: []string{"id"}},
		},
	})
	if !got.Opaque || !strings.Contains(got.Reason, "collision_policy") {
		t.Fatalf("expected opaque invalid-policy lineage, got %#v", got)
	}
}

// The key column carries values matched against the other side, so it
// derives from both inputs rather than only the left.
func TestAJoinKeyDerivesFromBothSides(t *testing.T) {
	got := joinColumns(ColumnLineageRequest{
		Node: models.Node{ID: "j", Type: models.NodeTypeJoin, Config: map[string]interface{}{
			"left_key": "id", "right_key": "cust_id",
		}},
		Inputs: []NodeInput{
			{Node: "L", Columns: []string{"id", "name"}},
			{Node: "R", Columns: []string{"cust_id", "total"}},
		},
	})
	d := derivationFor(t, got, "id")
	if len(d.From) != 2 {
		t.Fatalf("the join key derives from %d input(s), want 2: %+v", len(d.From), d.From)
	}
	if !strings.Contains(d.Rule, "cust_id") {
		t.Errorf("rule = %q, want it to name the matched column", d.Rule)
	}
}

// A join with one input cannot be described, and says so rather than
// describing the half it has.
func TestAJoinMissingAnInputIsOpaque(t *testing.T) {
	got := joinColumns(ColumnLineageRequest{
		Node:   models.Node{ID: "j", Type: models.NodeTypeJoin, Config: map[string]interface{}{"left_key": "id"}},
		Inputs: []NodeInput{{Node: "L", Columns: []string{"id"}}},
	})
	if !got.Opaque {
		t.Error("a one-input join described itself anyway")
	}
}

func TestUnionMergesSameNamedColumns(t *testing.T) {
	got := unionColumns(ColumnLineageRequest{
		Node: models.Node{ID: "u", Type: models.NodeTypeUnion},
		Inputs: []NodeInput{
			{Node: "A", Columns: []string{"id", "amount"}},
			{Node: "B", Columns: []string{"id", "region"}},
		},
	})
	if got.Opaque {
		t.Fatalf("union came back opaque: %s", got.Reason)
	}

	d := derivationFor(t, got, "id")
	if len(d.From) != 2 {
		t.Errorf("id derives from %d input(s), want both: %+v", len(d.From), d.From)
	}
	amount := derivationFor(t, got, "amount")
	if len(amount.From) != 1 || amount.From[0].Node != "A" {
		t.Errorf("amount derives from %+v, want only A", amount.From)
	}
	if _, ok := findDerivation(got, "region"); !ok {
		t.Error("a column present on only one input was dropped")
	}
}

// --- pass-through ---

func TestPassThroughNodesKeepTheirColumns(t *testing.T) {
	for _, nt := range []models.NodeType{
		models.NodeTypeQualityCheck, models.NodeTypeContractGate, models.NodeTypeCondition,
		models.NodeTypeSinkFile, models.NodeTypeSinkDB, models.NodeTypeSinkAPI,
	} {
		got := ColumnLineageFor(ColumnLineageRequest{
			Node:   models.Node{ID: "n", Type: nt},
			Inputs: []NodeInput{{Node: "up", Columns: []string{"a", "b"}}},
		})
		if got.Opaque {
			t.Errorf("%s came back opaque: %s", nt, got.Reason)
			continue
		}
		if len(got.Derivations) != 2 {
			t.Errorf("%s produced %d derivation(s), want 2", nt, len(got.Derivations))
			continue
		}
		for _, d := range got.Derivations {
			if d.Evidence != EvidenceDeclared {
				t.Errorf("%s: evidence = %q, want declared", nt, d.Evidence)
			}
			if len(d.From) != 1 || d.From[0].Column != d.Output {
				t.Errorf("%s: %s does not trace to itself: %+v", nt, d.Output, d.From)
			}
		}
	}
}

// A pipeline that has never run has no observed columns, and must
// produce no column edges rather than inventing them.
func TestNoObservedColumnsMeansNoColumnEdges(t *testing.T) {
	for _, nt := range models.AllNodeTypes {
		got := ColumnLineageFor(ColumnLineageRequest{
			Node:   models.Node{ID: "n", Type: nt, Config: map[string]interface{}{"left_key": "id"}},
			Inputs: []NodeInput{{Node: "up"}, {Node: "up2"}},
		})
		for _, d := range got.Derivations {
			if d.Output == "" {
				t.Errorf("%s produced a derivation with an empty output column", nt)
			}
		}
	}
}

// --- the graph ---

// End to end through the graph builder, which is where the response a
// user sees is actually assembled.
func TestTheGraphCarriesEvidenceAndOpacity(t *testing.T) {
	pipe := models.Pipeline{
		ID: "p1", Name: "p1",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Config: map[string]interface{}{"path": "/in.csv"}},
			{ID: "tf", Type: models.NodeTypeTransform, Config: map[string]interface{}{
				"rules": []interface{}{map[string]interface{}{
					"type": "add_column", "name": "total", "expression": "price * qty",
				}},
			}},
			{ID: "code", Type: models.NodeTypeCode, Config: map[string]interface{}{"code": "..."}},
			{ID: "dst", Type: models.NodeTypeSinkFile, Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "src", To: "tf"}, {From: "tf", To: "code"}, {From: "code", To: "dst"}},
	}
	profiles := map[string]LineageProfile{
		profileKey("p1", "src"):  profileWith("price", "qty"),
		profileKey("p1", "tf"):   profileWith("price", "qty", "total"),
		profileKey("p1", "code"): profileWith("price", "qty", "total"),
	}

	graph := BuildLineageGraphWithProfiles([]models.Pipeline{pipe}, profiles)

	// The derived column has edges from both its operands, and they say
	// how they were established.
	var totalSources []string
	for _, e := range graph.ColumnEdges {
		// Scoped to the transform. The sink downstream of the code node
		// also has an edge into a column called "total", and correctly
		// so: the sink declares that it writes what it is given, and the
		// code node did produce a "total". The chain BREAKS at the code
		// node rather than vanishing after it, which is the intended
		// shape -- dst.total cannot be traced back to src.price, and
		// that is the honest answer.
		if e.ToColumn != "total" || !strings.HasSuffix(e.To, ":tf") {
			continue
		}
		if e.Evidence != EvidenceDeclared {
			t.Errorf("edge to total has evidence %q, want declared", e.Evidence)
		}
		if e.MappingReason == "observed column name match" {
			t.Error("the old constant reason is still being emitted")
		}
		totalSources = append(totalSources, e.FromColumn)
	}
	sort.Strings(totalSources)
	if !equalStrings(totalSources, []string{"price", "qty"}) {
		t.Errorf("total's edges come from %v, want [price qty]", totalSources)
	}

	// Nothing is drawn through the code node.
	for _, e := range graph.ColumnEdges {
		if strings.Contains(e.To, ":code") {
			t.Errorf("a column edge was drawn into the code node: %+v", e)
		}
	}

	// And the code node says why, rather than just having no edges.
	var found bool
	for _, n := range graph.Nodes {
		if !strings.HasSuffix(n.ID, ":code") {
			continue
		}
		found = true
		if !n.ColumnsOpaque {
			t.Error("the code node is not marked opaque")
		}
		if n.OpaqueReason == "" {
			t.Error("the code node is opaque with no reason, which is the gap this replaces")
		}
	}
	if !found {
		t.Fatal("the code node is not in the graph at all; node lineage must stay complete")
	}
}

// --- helpers ---

func transformNode(rules ...map[string]interface{}) models.Node {
	raw := make([]interface{}, 0, len(rules))
	for _, r := range rules {
		raw = append(raw, r)
	}
	return models.Node{ID: "tf", Type: models.NodeTypeTransform,
		Config: map[string]interface{}{"rules": raw}}
}

func datasetWith(cols []string) *common.DataSet {
	row := common.DataRow{}
	for _, c := range cols {
		row[c] = "1"
	}
	return &common.DataSet{Columns: cols, Rows: []common.DataRow{row}}
}

func profileWith(cols ...string) LineageProfile {
	p := &DataProfile{ColumnCount: len(cols)}
	for _, c := range cols {
		p.Columns = append(p.Columns, ColumnProfile{Name: c})
	}
	return LineageProfile{Profile: p}
}

func findDerivation(l ColumnLineage, output string) (ColumnDerivation, bool) {
	for _, d := range l.Derivations {
		if d.Output == output {
			return d, true
		}
	}
	return ColumnDerivation{}, false
}

func derivationFor(t *testing.T, l ColumnLineage, output string) ColumnDerivation {
	t.Helper()
	d, ok := findDerivation(l, output)
	if !ok {
		var have []string
		for _, x := range l.Derivations {
			have = append(have, x.Output)
		}
		t.Fatalf("no derivation for %q; the node declares %v", output, have)
	}
	return d
}

func refColumns(refs []ColumnRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Column)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
