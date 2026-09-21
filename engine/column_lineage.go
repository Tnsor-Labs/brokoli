package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Column lineage that says how it knows (ADR-039).
//
// What this replaces: inferColumnMappings compared the observed column
// names either side of an edge and emitted an edge wherever a name
// appeared on both, with a hardcoded Confidence of 0.7. Three problems,
// all of them visible to a user as confident output:
//
//   - a derived column got no edge at all, so "total = price * qty" --
//     the strongest fact in the pipeline -- was the one thing the graph
//     could not say;
//   - two unrelated datasets that both had "id" got an edge asserting a
//     derivation nobody performed;
//   - 0.7 was the same for every edge, so it carried no information while
//     reading as a calibrated probability.
//
// The replacement asks the node type. A transform node is not opaque
// text: add_column carries an expression, rename carries a pair, join
// carries its keys. Those mappings are not inferred, they are read.
//
// WHERE IT CANNOT BE READ, NOTHING IS DRAWN. A code node runs arbitrary
// Python, JavaScript or JVM bytecode and no analysis available here can
// say which output column came from which input. Such a node declares
// itself opaque and no column edges pass through it. That is a real loss
// of apparent capability, and it is the point: a plausible claim about a
// black box is the worst available answer.
//
// Node-level lineage is unaffected and stays complete. Which node
// produced which dataset is always knowable, code node or not.

// EvidenceLevel says how a column edge was established.
//
// It replaces the Confidence float. A consumer that wants only facts
// filters to EvidenceDeclared and EvidenceAttested, which was not
// possible before at any price.
type EvidenceLevel string

const (
	// EvidenceDeclared is read from the pipeline definition: an
	// expression, a rename pair, a join key, a node type that provably
	// does not alter columns. Exact.
	EvidenceDeclared EvidenceLevel = "declared"

	// EvidenceAttested is proven by the execution record, and checkable
	// after the fact.
	//
	// Produced for one claim today: an identity edge through a node whose
	// run stored its single input and its output with the same digest.
	// The bytes did not change, so the column provably passed through
	// untouched, whatever the node's type says it does. Anything short of
	// that stays declared.
	EvidenceAttested EvidenceLevel = "attested"

	// EvidenceParsed is extracted from user-authored SQL. Table-level
	// only, never column-level.
	//
	// Not produced yet. Reserved for source_db table-reference
	// extraction.
	EvidenceParsed EvidenceLevel = "parsed"

	// EvidenceInferred is a column-name match: a guess, labelled as one.
	//
	// Not produced by any node type today. It is the level the old
	// behaviour would have carried, kept in the vocabulary so that a
	// consumer's filter is written against the whole set rather than
	// against what happens to be emitted this release.
	EvidenceInferred EvidenceLevel = "inferred"
)

// ColumnRef is one upstream column.
type ColumnRef struct {
	// Node is the upstream node's ID.
	Node string `json:"node"`
	// Column is the column name on that node's output.
	Column string `json:"column"`
}

// ColumnDerivation is one output column and where it came from.
type ColumnDerivation struct {
	// Output is the column this node produces.
	Output string
	// From is what it derives from. Empty means the column is a
	// constant, or is produced from nothing the node can name.
	From []ColumnRef
	// Evidence is how this was established.
	Evidence EvidenceLevel
	// Rule states the derivation in the pipeline's own terms:
	// "renamed from qty", "price * qty", "join key". It is shown to a
	// person, so it names what the pipeline says, not how this code
	// works.
	Rule string
}

// ColumnLineage is what a node type declares about its output columns.
//
// A node type must return one or the other: derivations, or opacity with
// a reason. Returning neither is what the coverage gate refuses, because
// silence is indistinguishable from an unfinished declaration.
type ColumnLineage struct {
	// Opaque means the node cannot say which output came from which
	// input. No column edges are drawn through it.
	Opaque bool
	// Reason is why, and it is shown in the graph. A node that declares
	// itself opaque is making a statement, not failing to make one.
	Reason string
	// Derivations is one entry per output column, when not opaque.
	Derivations []ColumnDerivation
}

// NodeInput is one incoming edge's contribution.
type NodeInput struct {
	// Node is the upstream node's ID.
	Node string
	// Columns are the column names on its output, in order.
	//
	// These come from the observed profile of the last run, because a
	// source's columns are not in the pipeline definition: a CSV's
	// header is a fact about the file. Empty when nothing has run yet,
	// which every declarer must handle -- a pipeline that has never run
	// still has a lineage graph, and it must not claim columns it has
	// not seen.
	Columns []string
}

// ColumnLineageRequest is what a declarer is given.
type ColumnLineageRequest struct {
	// Node is the node, with variables already resolved.
	Node models.Node
	// Inputs are the incoming edges in DAG order. Join and union depend
	// on that order: for a join, Inputs[0] is the left side.
	Inputs []NodeInput
}

// ColumnLineageFunc declares a node type's column mapping.
type ColumnLineageFunc func(ColumnLineageRequest) ColumnLineage

// columnLineageByType is the declaration per node type.
//
// A map rather than a switch in the graph builder, and this is the whole
// design point of ADR-039 part 3: a switch's default case silently
// absorbs every node type added after it was written, so the absence of
// a declaration is undetectable. A map's absence is a missing key, and
// TestEveryNodeTypeDeclaresItsColumnLineage fails on it.
var columnLineageByType = map[models.NodeType]ColumnLineageFunc{
	// Sources produce columns from outside the graph. There is no
	// upstream column for an edge to come from, so there is nothing to
	// declare and nothing is opaque about it.
	models.NodeTypeSourceFile: sourceColumns,
	models.NodeTypeSourceAPI:  sourceColumns,

	// source_db carries user-authored SQL. Its columns come from a query
	// this engine does not parse, so at the column level it can say
	// nothing -- but it is a source, so there is no upstream to point at
	// either. Table-reference extraction is a later piece of ADR-039 and
	// is table-level, never column-level.
	models.NodeTypeSourceDB: sourceColumns,

	// The structured transforms: the mappings are in the IR.
	models.NodeTypeTransform: transformColumns,
	models.NodeTypeProject:   dedicatedOperatorColumns,
	models.NodeTypeAggregate: dedicatedOperatorColumns,
	models.NodeTypeFilter:    dedicatedOperatorColumns,
	models.NodeTypeJoin:      joinColumns,
	models.NodeTypeUnion:     unionColumns,

	// Nodes that provably return their input unchanged. quality_check
	// runs its checks and returns the same dataset; condition evaluates
	// a predicate and passes the input through on the branch it takes;
	// the sinks write what they are given. Verified in the handlers, not
	// assumed: engine/node_handlers.go runQualityCheck returns `input`,
	// runCondition returns `input`.
	models.NodeTypeQualityCheck: passThroughColumns("quality_check validates and returns its input unchanged"),
	models.NodeTypeCondition:    passThroughColumns("condition evaluates a predicate and passes its input through"),
	models.NodeTypeSinkFile:     passThroughColumns("sink_file writes the columns it is given"),
	models.NodeTypeSinkDB:       passThroughColumns("sink_db writes the columns it is given"),
	models.NodeTypeSinkAPI:      passThroughColumns("sink_api sends the columns it is given"),
	models.NodeTypeNotify:       passThroughColumns("notify sends a message and passes its input through"),
	models.NodeTypeWait:         passThroughColumns("wait delays and passes its input through"),

	// migrate has no dataset input at all: it reads source_uri and
	// writes dest_uri itself, and what it returns is a summary row, not
	// the migrated data. So its output columns derive from nothing in
	// this graph.
	models.NodeTypeMigrate: migrateColumns,

	// The black boxes. Each states why, because a node declaring itself
	// opaque is making a statement and the graph renders it as one.
	models.NodeTypeCode: opaqueColumns(
		"a code node runs arbitrary user code; which output column derives from which input is not knowable here"),
	models.NodeTypeDatasetMap: opaqueColumns(
		"dataset_map applies a user-supplied Python function, which may produce any columns"),
	models.NodeTypeDatasetFilter: opaqueColumns(
		"dataset_filter applies a user-supplied Python function; it is expected to filter rows, but nothing constrains it to leave columns alone"),
	models.NodeTypeSQLGenerate: opaqueColumns(
		"sql_generate emits SQL text derived from its input, not a dataset whose columns descend from it"),
	models.NodeTypeDBT: opaqueColumns(
		"a dbt node runs a separate tool whose models this engine does not read"),
	models.NodeTypeTask: opaqueColumns(
		"a task node runs a user-authored harness; ADR-033's output contract declares shape, not column provenance"),
}

func dedicatedOperatorColumns(req ColumnLineageRequest) ColumnLineage {
	synthetic := req.Node
	originalType := req.Node.Type
	synthetic.Type = models.NodeTypeTransform
	synthetic.Config = map[string]interface{}{"rules": []interface{}{synthetic.Config}}
	// The original node type selects the rule shape; preserve it before
	// replacing the type for the shared lineage walker.
	if originalType == models.NodeTypeProject {
		synthetic.Config["rules"] = []interface{}{map[string]interface{}{"type": "project", "expression_version": req.Node.Config["expression_version"], "projections": req.Node.Config["projections"]}}
	} else {
		synthetic.Config["rules"] = []interface{}{map[string]interface{}{"type": "aggregate", "group_by": req.Node.Config["group_by"], "agg_fields": req.Node.Config["agg_fields"]}}
	}
	return transformColumns(ColumnLineageRequest{Node: synthetic, Inputs: req.Inputs})
}

// ColumnLineageFor returns a node's declaration.
//
// An unregistered node type is opaque rather than absent: a graph must
// still render, and refusing to draw column edges through something this
// build does not recognise is the safe direction. The coverage gate is
// what stops that becoming the normal case.
func ColumnLineageFor(req ColumnLineageRequest) ColumnLineage {
	declare, ok := columnLineageByType[req.Node.Type]
	if !ok {
		return ColumnLineage{
			Opaque: true,
			Reason: fmt.Sprintf("this build has no column lineage declaration for node type %q", req.Node.Type),
		}
	}
	return declare(req)
}

// sourceColumns: a source has no upstream, so no column edge enters it.
func sourceColumns(ColumnLineageRequest) ColumnLineage {
	return ColumnLineage{}
}

// migrateColumns: migrate's output is a summary of what it did, and its
// data never passes through the graph.
func migrateColumns(ColumnLineageRequest) ColumnLineage {
	return ColumnLineage{}
}

// passThroughColumns declares a node type that returns its input as it
// received it.
//
// The derivation is declared even though the column names come from an
// observed profile: what is declared is the RULE -- this node type does
// not alter columns -- which is exact and comes from the node type, not
// from a name comparison. Observing which columns exist is a separate
// question from knowing that they pass through unchanged.
func passThroughColumns(reason string) ColumnLineageFunc {
	return func(req ColumnLineageRequest) ColumnLineage {
		var out []ColumnDerivation
		for _, in := range req.Inputs {
			for _, col := range in.Columns {
				out = append(out, ColumnDerivation{
					Output:   col,
					From:     []ColumnRef{{Node: in.Node, Column: col}},
					Evidence: EvidenceDeclared,
					Rule:     reason,
				})
			}
		}
		return ColumnLineage{Derivations: out}
	}
}

// opaqueColumns declares a node type that cannot say.
func opaqueColumns(reason string) ColumnLineageFunc {
	return func(ColumnLineageRequest) ColumnLineage {
		return ColumnLineage{Opaque: true, Reason: reason}
	}
}

// transformColumns walks the transform node's rules.
//
// The rules are applied in order and each one rewrites the column set,
// so this simulates that: it tracks the current columns and where each
// currently traces back to, then reports the final state. Applying them
// in order matters -- a rename followed by an add_column referencing the
// new name must resolve through the rename.
func transformColumns(req ColumnLineageRequest) ColumnLineage {
	rules, err := parseNodeTransformRules(req.Node)
	if err != nil {
		// A config this build cannot read is not a licence to guess.
		return ColumnLineage{
			Opaque: true,
			Reason: fmt.Sprintf("the transform's rules could not be read: %v", err),
		}
	}

	// origin maps a currently-live column to the upstream columns it
	// came from, and rule to how.
	origin := map[string][]ColumnRef{}
	rule := map[string]string{}
	var order []string

	for _, in := range req.Inputs {
		for _, col := range in.Columns {
			if _, seen := origin[col]; !seen {
				order = append(order, col)
			}
			origin[col] = append(origin[col], ColumnRef{Node: in.Node, Column: col})
			rule[col] = "passed through"
		}
	}

	drop := func(name string) {
		delete(origin, name)
		delete(rule, name)
		for i, c := range order {
			if c == name {
				order = append(order[:i], order[i+1:]...)
				break
			}
		}
	}
	add := func(name string, from []ColumnRef, how string) {
		if _, exists := origin[name]; !exists {
			order = append(order, name)
		}
		origin[name] = from
		rule[name] = how
	}

	for _, r := range rules {
		switch normaliseTransformType(r.Type) {
		case "rename":
			for from, to := range r.Mapping {
				if from == to {
					continue
				}
				src, known := origin[from]
				if !known {
					// Renaming a column this build has not seen. The
					// mapping is still declared by the config, so the
					// output column is recorded with no traceable
					// origin rather than dropped: the pipeline says it
					// exists.
					src = nil
				}
				add(to, src, "renamed from "+from)
				drop(from)
			}
		case "add_column":
			if r.Name == "" {
				continue
			}
			add(r.Name, resolveExpressionColumns(r.Expression, origin), r.Expression)
		case "project":
			next := map[string][]ColumnRef{}
			nextRule := map[string]string{}
			var nextOrder []string
			for _, projection := range r.Projections {
				next[projection.Name] = resolveNativeExpressionColumns(projection.Expr, origin)
				nextRule[projection.Name] = "native expression"
				nextOrder = append(nextOrder, projection.Name)
			}
			origin, rule, order = next, nextRule, nextOrder
		case "drop":
			for _, c := range r.Columns {
				drop(c)
			}
		case "function":
			// apply_function rewrites a column in place from itself.
			if r.Column == "" {
				continue
			}
			if src, known := origin[r.Column]; known {
				add(r.Column, src, r.Function+"("+r.Column+")")
			}
		case "replace":
			if r.Column == "" {
				continue
			}
			if src, known := origin[r.Column]; known {
				add(r.Column, src, "values replaced in "+r.Column)
			}
		case "aggregate":
			// Aggregation replaces the column set entirely: the group-by
			// keys plus one column per aggregation.
			next := map[string][]ColumnRef{}
			nextRule := map[string]string{}
			var nextOrder []string
			for _, g := range r.GroupBy {
				if src, known := origin[g]; known {
					next[g] = src
				} else {
					next[g] = nil
				}
				nextRule[g] = "grouped by " + g
				nextOrder = append(nextOrder, g)
			}
			for _, f := range aggFieldsOf(r) {
				name := f.Alias
				if name == "" {
					name = f.Function + "_" + f.Column
				}
				next[name] = origin[f.Column]
				nextRule[name] = f.Function + "(" + f.Column + ")"
				nextOrder = append(nextOrder, name)
			}
			origin, rule, order = next, nextRule, nextOrder
		case "filter", "sort", "dedup":
			// Row-level operations. The column set is untouched, which is
			// why they are listed rather than defaulted: a reader can see
			// they were considered.
		default:
			// A rule this build does not recognise may do anything to the
			// column set, so the whole node stops making claims. Refusing
			// per-rule would leave the earlier rules' mappings standing
			// as if the unknown rule had not run.
			return ColumnLineage{
				Opaque: true,
				Reason: fmt.Sprintf("the transform uses a rule this build does not recognise (%q), so its effect on columns is unknown", r.Type),
			}
		}
	}

	out := make([]ColumnDerivation, 0, len(order))
	for _, name := range order {
		out = append(out, ColumnDerivation{
			Output: name, From: origin[name],
			Evidence: EvidenceDeclared, Rule: rule[name],
		})
	}
	return ColumnLineage{Derivations: out}
}

// normaliseTransformType collapses the aliases applyRule accepts.
//
// Kept in step with engine/transform.go's own switch by
// TestTransformAliasesMatchTheExecutor, because a rule name this misses
// is a rule whose column effect is silently ignored.
func normaliseTransformType(t string) string {
	switch t {
	case "rename_columns", "rename":
		return "rename"
	case "add_column":
		return "add_column"
	case "project", "projection":
		return "project"
	case "filter_rows", "filter", "filter_native":
		return "filter"
	case "apply_function", "function":
		return "function"
	case "replace_values", "replace":
		return "replace"
	case "drop_columns", "drop":
		return "drop"
	case "sort":
		return "sort"
	case "deduplicate", "dedup":
		return "dedup"
	case "aggregate", "agg":
		return "aggregate"
	default:
		return t
	}
}

func resolveNativeExpressionColumns(expr map[string]interface{}, origin map[string][]ColumnRef) []ColumnRef {
	var refs []ColumnRef
	seen := map[string]bool{}
	var visit func(map[string]interface{})
	visit = func(node map[string]interface{}) {
		if node["op"] == "column" {
			path, _ := node["path"].([]interface{})
			if len(path) > 0 {
				if name, ok := path[0].(string); ok && !seen[name] {
					seen[name] = true
					refs = append(refs, origin[name]...)
				}
			}
		}
		for _, key := range []string{"left", "right"} {
			if child, ok := node[key].(map[string]interface{}); ok {
				visit(child)
			}
		}
		if args, ok := node["args"].([]interface{}); ok {
			for _, raw := range args {
				if child, ok := raw.(map[string]interface{}); ok {
					visit(child)
				}
			}
		}
	}
	visit(expr)
	return refs
}

// aggFieldsOf returns the aggregation list under either of its names.
func aggFieldsOf(r TransformRule) []AggField {
	if len(r.AggFields) > 0 {
		return r.AggFields
	}
	return r.Aggregations
}

// resolveExpressionColumns finds which known columns an add_column
// expression reads.
//
// Deliberately a reference scan and not an evaluator. The engine's own
// expression handling (evalAddColumnExpression) has three forms with
// operator-dependent semantics, and reimplementing them here would give
// two answers to the same question. What lineage needs is narrower: which
// upstream columns does this expression name. A token that matches a live
// column is a reference; anything else is a literal.
//
// The failure direction is chosen: an expression mentioning a column that
// happens to share a name with a literal over-reports one edge, whereas
// evaluating wrongly would under-report the derivation entirely, which is
// the case ADR-039 exists to fix.
func resolveExpressionColumns(expr string, origin map[string][]ColumnRef) []ColumnRef {
	if strings.TrimSpace(expr) == "" {
		return nil
	}
	var refs []ColumnRef
	seen := map[string]bool{}
	for _, token := range strings.FieldsFunc(expr, func(r rune) bool {
		return r == '+' || r == '-' || r == '*' || r == '/' || r == '(' || r == ')' || r == ' ' || r == ','
	}) {
		token = strings.TrimSpace(token)
		if token == "" || seen[token] {
			continue
		}
		// A quoted token is a literal, whatever it spells.
		if strings.HasPrefix(token, `"`) || strings.HasPrefix(token, "'") {
			continue
		}
		src, known := origin[token]
		if !known {
			continue
		}
		seen[token] = true
		refs = append(refs, src...)
	}
	return refs
}

// joinColumns declares the join's output from its keys.
//
// JoinDatasets builds the output according to its explicit collision policy.
// This declarer delegates output naming to the same planner used by execution;
// TestJoinColumnLineageMatchesTheJoin also compares the resulting schema to a
// real join for the legacy policy.
func joinColumns(req ColumnLineageRequest) ColumnLineage {
	if len(req.Inputs) < 2 {
		return ColumnLineage{
			Opaque: true,
			Reason: fmt.Sprintf("a join needs two inputs and this one has %d", len(req.Inputs)),
		}
	}
	left, right := req.Inputs[0], req.Inputs[1]

	leftKey, _ := req.Node.Config["left_key"].(string)
	rightKey, _ := req.Node.Config["right_key"].(string)
	if leftKey == "" {
		return ColumnLineage{Opaque: true, Reason: "the join declares no left_key"}
	}
	if rightKey == "" {
		rightKey = leftKey
	}

	policy, _ := req.Node.Config["collision_policy"].(string)
	rightAlias, _ := req.Node.Config["right_alias"].(string)
	_, rightOutputNames, err := planJoinColumns(left.Columns, right.Columns, leftKey, rightKey, JoinOptions{
		CollisionPolicy: JoinCollisionPolicy(policy),
		RightAlias:      rightAlias,
	})
	if err != nil {
		return ColumnLineage{
			Opaque: true,
			Reason: fmt.Sprintf("the join output schema could not be planned: %v", err),
		}
	}

	var out []ColumnDerivation
	for _, c := range left.Columns {
		d := ColumnDerivation{
			Output: c, From: []ColumnRef{{Node: left.Node, Column: c}},
			Evidence: EvidenceDeclared, Rule: "passed through from the left input",
		}
		if c == leftKey {
			// The key column carries values matched against the right
			// side, so it derives from both.
			d.From = append(d.From, ColumnRef{Node: right.Node, Column: rightKey})
			d.Rule = fmt.Sprintf("join key: %s matched against %s", leftKey, rightKey)
		}
		out = append(out, d)
	}
	for _, c := range right.Columns {
		if c == rightKey && leftKey == rightKey {
			continue // JoinDatasets drops the duplicate key column
		}
		out = append(out, ColumnDerivation{
			Output: rightOutputNames[c], From: []ColumnRef{{Node: right.Node, Column: c}},
			Evidence: EvidenceDeclared,
			Rule:     "passed through from the right input",
		})
	}
	return ColumnLineage{Derivations: out}
}

// unionColumns declares the union's output.
//
// UnionDatasets takes the union of the inputs' column names in first-seen
// order, so a name present on several inputs is one output column fed by
// all of them.
func unionColumns(req ColumnLineageRequest) ColumnLineage {
	origin := map[string][]ColumnRef{}
	var order []string
	for _, in := range req.Inputs {
		for _, c := range in.Columns {
			if _, seen := origin[c]; !seen {
				order = append(order, c)
			}
			origin[c] = append(origin[c], ColumnRef{Node: in.Node, Column: c})
		}
	}
	out := make([]ColumnDerivation, 0, len(order))
	for _, c := range order {
		rule := "passed through by the union"
		if len(origin[c]) > 1 {
			rule = fmt.Sprintf("union of %s from %d inputs", c, len(origin[c]))
		}
		out = append(out, ColumnDerivation{
			Output: c, From: origin[c], Evidence: EvidenceDeclared, Rule: rule,
		})
	}
	return ColumnLineage{Derivations: out}
}

// declaredNodeTypes is the registered set, sorted, for the gate's message.
func declaredNodeTypes() []string {
	out := make([]string, 0, len(columnLineageByType))
	for t := range columnLineageByType {
		out = append(out, string(t))
	}
	sort.Strings(out)
	return out
}
