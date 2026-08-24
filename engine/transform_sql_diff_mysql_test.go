package engine

import (
	"testing"
)

// The same corpus, the same runners, against MySQL. This file existing is
// what ADR-024 means by a capability being earned: the mysql entry in
// pkg/dbdialect's registry is allowed because these runs pass, and the
// per-dialect overrides in the corpus (the boolean case) are the documented
// divergences between the backends rather than separate corpora.
//
// Needs a live MySQL for the same reason the Postgres run needs a live
// Postgres: the divergences at issue are collation, NULL handling, and
// numeric coercion, all of which a fake would define away.

func TestTransformSQLPrefixDifferentialMySQL(t *testing.T) {
	runPrefixDifferentialCorpus(t, mysqlTestURI(t), "mysql")
}

func TestTransformSQLAggregateDifferentialMySQL(t *testing.T) {
	runAggregateDifferentialCorpus(t, mysqlTestURI(t), "mysql")
}

func TestRefusedRulesReallyDivergeMySQL(t *testing.T) {
	runRefusedRulesReallyDiverge(t, mysqlTestURI(t), "mysql")
}
