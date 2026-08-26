package engine

import (
	"fmt"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
	"github.com/Tnsor-Labs/brokoli/pkg/dbtmanifest"
)

// A built dbt model as an ADR-023 reference (Phase 3 of #353).
//
// A model dbt has just built is a named relation on a known server, which is
// exactly what a TableRef is. Handing one downstream means a sink on the
// same connection composes through the pushdown compiler that already
// exists: the rows never leave the database, and no part of that machinery
// had to learn what dbt is.
//
// This is the payoff for doing the dbt work after ADR-024 rather than
// before. The dialect layer, the same-server rule and the pushdown compiler
// were all built for source_db, and a dbt model reaches them by being
// describable in the same terms rather than by adding a path of its own.
//
// # What it needs, and what it refuses without
//
// A reference needs a server, and the only way this node knows one is a
// conn_id: a project pointed at its own profiles.yml is authenticating
// somewhere Brokoli cannot name. So composition requires conn_id, and a
// project using its own profile still runs -- it simply hands downstream the
// per-model results table it always did.
//
// An ephemeral model has no relation at all; dbt inlines it into whatever
// selects from it. Asking for one as an output is a configuration mistake
// worth naming rather than a case to paper over.

// dbtOutputTableRef builds the reference for a dbt node's named output
// model, or explains why it cannot.
//
// Returns (nil, nil) when the node did not ask for one, which is the common
// case and not a problem.
func (r *Runner) dbtOutputTableRef(
	node models.Node,
	projectDir string,
	project *dbtmanifest.Project,
	results *dbtmanifest.RunResults,
) (*TableRef, error) {
	wanted, _ := node.Config["output_model"].(string)
	if wanted == "" {
		return nil, nil
	}
	connID, _ := node.Config["conn_id"].(string)
	if connID == "" {
		return nil, fmt.Errorf(
			"output_model %q needs conn_id: a reference names a server, and a project using its own "+
				"profiles.yml authenticates somewhere Brokoli cannot name", wanted)
	}
	if project == nil || results == nil {
		return nil, fmt.Errorf(
			"output_model %q was requested but dbt's artifacts could not be read, so there is nothing "+
				"to name the relation with", wanted)
	}

	dbtNode, ok := findDBTNode(project, wanted)
	if !ok {
		return nil, fmt.Errorf("output_model %q is not in this dbt project", wanted)
	}
	if dbtNode.Materialization == "ephemeral" {
		return nil, fmt.Errorf(
			"output_model %q is ephemeral, so dbt builds no relation for it -- it is inlined into "+
				"whatever selects from it, and there is nothing downstream could read",
			wanted)
	}

	res, reported := results.ByUniqueID()[dbtNode.UniqueID]
	if !reported {
		return nil, fmt.Errorf(
			"output_model %q was not built by this invocation; select it, or point output_model at "+
				"something this command runs", wanted)
	}
	if !res.Status.Succeeded() {
		// Not an error to return: the run is already failing for the
		// model's own reason, and a second message about the reference
		// would bury it.
		return nil, nil
	}
	relation := res.RelationName
	if relation == "" {
		relation = dbtNode.RelationName
	}
	if relation == "" {
		return nil, fmt.Errorf("dbt reported no relation for %q, so it cannot be referenced", wanted)
	}

	conn, err := r.connResolver.ResolveConnection(connID)
	if err != nil {
		return nil, err
	}
	if !conn.BuildsURI() {
		return nil, fmt.Errorf(
			"connection %q is type %q, which has no database driver in this build, so a downstream node "+
				"could not read the relation dbt built", connID, conn.Type)
	}
	uri := conn.BuildURI()

	// The relation name is dbt's own, already quoted and qualified for the
	// backend it built on, so it is used as written rather than re-quoted.
	query := "SELECT * FROM " + relation

	// Column order comes from the server rather than the manifest: a
	// project documents only the columns it chooses to, and a consumer
	// planning an INSERT needs all of them in the order the query
	// produces.
	dialect := dialectForURI(uri)
	d, ok := dbdialect.For(dialect)
	if !ok {
		return nil, fmt.Errorf("no dialect registered for %q", dialect)
	}
	columns, err := queryColumnOrder(r.ctx, uri, query, d)
	if err != nil {
		return nil, fmt.Errorf("read the columns of %s: %w", relation, err)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("%s reports no columns", relation)
	}

	return &TableRef{
		ConnURI: uri,
		Query:   query,
		Columns: columns,
		Dialect: dialect,
		// The dbt node itself is absorbed: its rows never entered the
		// engine, and the consumer's execution is what moves them.
		Absorbed: []string{node.ID},
	}, nil
}

// findDBTNode resolves what a user wrote in output_model. A dbt unique_id is
// exact; a bare name is what someone actually types, and is matched against
// models only -- a test and a model can share a name, and a test has no
// relation to hand downstream.
func findDBTNode(project *dbtmanifest.Project, wanted string) (dbtmanifest.Node, bool) {
	if n, ok := project.Nodes[wanted]; ok {
		return n, true
	}
	for _, n := range project.Models() {
		if n.Name == wanted {
			return n, true
		}
	}
	return dbtmanifest.Node{}, false
}
