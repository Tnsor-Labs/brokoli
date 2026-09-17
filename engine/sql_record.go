package engine

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Recording the statement a node actually ran.
//
// A templated pipeline is opaque after the fact: the pipeline holds
// ${interval.start}, the server saw a date, and reconstructing which date
// means re-deriving the run's interval by hand. This records the rendered
// text as a models.AttemptQuery event, per node attempt, so the question
// "what did this actually execute?" is answered by reading the run instead.
//
// Two properties are load-bearing and both are tested:
//
//   - It records BEFORE the statement executes. The statement worth reading
//     is usually the one that failed, and a recorder that fires on success
//     cannot show it.
//   - It never records a credential. The connection URI is not part of a
//     statement and is never copied into one; secret-derived values that the
//     resolver substituted INTO the statement are masked (see
//     maskRecordedSQL).

// maxRecordedSQLBytes bounds one recorded statement.
//
// sink_db renders every row into SQL text when the dialect has no bulk
// protocol, so a generated INSERT is routinely megabytes and occasionally
// far worse. run_events is an audit log, not a copy of the data: 64 KiB is
// far more than enough to read what a statement does (its shape, its
// predicates, its substituted dates) while keeping one row's payload
// bounded no matter how many rows the node wrote.
const maxRecordedSQLBytes = 64 << 10

// minMaskableSecretBytes is the shortest secret that can be masked without
// mangling the statement. See maskRecordedSQL.
const minMaskableSecretBytes = 8

const recordedSecretMask = "[redacted]"

// recordedSQLShortSecretRefusal replaces the whole statement when masking
// cannot be done safely. It is deliberately a SQL comment: whatever reads
// these payloads renders it as a statement, and this way it reads as the
// explanation it is.
const recordedSQLShortSecretRefusal = "-- [brokoli] statement not recorded: a secret it substitutes is shorter than " +
	"8 characters, and masking a value that short would corrupt the statement without proving the secret was removed"

// recordExecutedSQL appends the statement a node is about to execute to the
// run's durable event log.
//
// Callers pass the statement they are ABOUT to send, not one they have sent.
func (r *Runner) recordExecutedSQL(nodeID string, attempt int, statement string) {
	// A dry run executes nothing, so it has no statement that ran, and it
	// persists no node attempt for one to hang off. Matches every other
	// event the runner appends.
	if r.dryRun || r.run == nil {
		return
	}
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return
	}
	recorded := renderRecordedSQL(statement, r.recordedSQLSecrets(nodeID))
	a := attempt
	r.appendEvent(models.RunEvent{
		RunID:     r.run.ID,
		NodeID:    nodeID,
		Attempt:   &a,
		EventType: models.AttemptQuery,
		Payload:   models.RunEventPayload{Statement: recorded},
	})
}

// renderRecordedSQL turns a statement into the text that gets persisted.
//
// The order is load-bearing and is pinned by a test: masking runs over the
// WHOLE statement, and only then is the result bounded. Truncating first
// would mean a secret sitting past the cut was dropped by luck rather than
// removed by the mask -- the same secret in a shorter statement would be
// handled by a different mechanism, and the short-secret refusal below
// would never fire for a large statement at all.
func renderRecordedSQL(statement string, secrets []string) string {
	return truncateRecordedSQL(maskRecordedSQL(statement, secrets))
}

// recordBulkWrite records that a write happened through a bulk load
// protocol, which has no SQL statement to record.
//
// Recording nothing here would be the wrong kind of silence: an operator
// looking at a sink node with no statement cannot tell whether this feature
// is broken or whether the path genuinely has no SQL. So the absence is
// stated as a fact, naming the mechanism, rather than left to inference.
// The note is deliberately a SQL comment and never pretends to be the
// statement that ran -- no synthetic INSERT is invented for it, because a
// fabricated statement is worse than an honest absence.
func (r *Runner) recordBulkWrite(nodeID string, attempt int, dialect, table string) {
	r.recordExecutedSQL(nodeID, attempt, fmt.Sprintf(
		"-- [brokoli] no SQL statement: this write streamed rows into %q using %s, "+
			"which carries rows as data rather than as SQL text", table, bulkMechanismName(dialect)))
}

// bulkMechanismName names the protocol that moved the rows, so the note
// says which one rather than only that there was one.
func bulkMechanismName(dialect string) string {
	switch dialect {
	case "postgres":
		return "the PostgreSQL COPY protocol"
	case "mysql":
		return "MySQL LOAD DATA LOCAL INFILE"
	case "clickhouse":
		return "the ClickHouse native batch append"
	default:
		return "the dialect's bulk load protocol"
	}
}

// maskRecordedSQL replaces every secret-derived value in a rendered
// statement with a placeholder.
//
// The decision this encodes: recording the rendered text is the point of the
// feature, but "it is the user's own query" does not make its contents safe
// to write down. ${secret.*} and an encrypted ${var.*} exist precisely so a
// value is NOT written into the pipeline; substituting it and then
// persisting the result into run_events would undo that, and would hand the
// value to everyone who can read the run rather than to the server alone.
// So the rendered statement is recorded with those values, and only those
// values, masked. A plain ${param.*}, an operator-allowlisted ${env.*} and
// an unencrypted ${var.*} are not secrets and are recorded as they ran.
//
// A value shorter than minMaskableSecretBytes is refused rather than masked.
// A three-character secret occurs all over ordinary SQL -- in table names,
// in keywords -- so replacing every occurrence would shred the statement
// into something misleading while still not demonstrating the secret was
// removed. Refusing says so, which is the visible kind of wrong.
func maskRecordedSQL(statement string, secrets []string) string {
	for _, s := range secrets {
		if s == "" || !strings.Contains(statement, s) {
			continue
		}
		if len(s) < minMaskableSecretBytes {
			return recordedSQLShortSecretRefusal
		}
	}
	for _, s := range secrets {
		if len(s) < minMaskableSecretBytes {
			continue
		}
		statement = strings.ReplaceAll(statement, s, recordedSecretMask)
	}
	return statement
}

// recordedSQLSecrets is every value this node could have substituted that
// must not be written down.
//
// Two sources, deliberately asymmetric:
//
//   - Every BROKED_SECRET_* value in the run's environment, whether or not
//     this node references it. Enumerating them costs nothing and masks a
//     secret that reached the statement by some route this function did not
//     predict, which is the failure worth defending against.
//   - Each ${var.*} the node's own template names, masked only when the
//     store reports it encrypted. Stored variables cannot be enumerated by
//     value, so these have to be looked up by name; an unencrypted one is
//     not a secret, because not encrypting it was a choice.
func (r *Runner) recordedSQLSecrets(nodeID string) []string {
	vc := r.varCtx
	if vc == nil {
		return nil
	}
	var out []string
	for k, v := range vc.Env {
		if v != "" && strings.HasPrefix(k, "BROKED_SECRET_") {
			out = append(out, v)
		}
	}
	if vc.Vars == nil {
		return out
	}
	for _, name := range templateVarNames(r.nodeConfigTemplate(nodeID)) {
		v, encrypted, err := vc.Vars.GetVariableValue(vc.WorkspaceID, name)
		if err == nil && encrypted && v != "" {
			out = append(out, v)
		}
	}
	return out
}

// templateVarNames is the ${var.NAME} references in a config template.
//
// The reference is cut at the first '|' so that a filtered reference names
// the same variable as a bare one.
func templateVarNames(template string) []string {
	if template == "" {
		return nil
	}
	var names []string
	seen := make(map[string]bool)
	for _, m := range varPattern.FindAllStringSubmatch(template, -1) {
		key, _, _ := strings.Cut(m[1], "|")
		prefix, name, ok := strings.Cut(strings.TrimSpace(key), ".")
		if !ok || prefix != "var" {
			continue
		}
		if name = strings.TrimSpace(name); name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// nodeConfigTemplate is a node's config as authored, before variables were
// resolved.
//
// Runner.executeNode resolves into a NEW map and assigns it to its own copy
// of the node, so the pipeline's own node still holds the ${...} references.
// That is what names the variables whose values have to be masked -- by the
// time a handler runs, its config holds the values themselves and no longer
// says where they came from.
func (r *Runner) nodeConfigTemplate(nodeID string) string {
	if r.pipe == nil {
		return ""
	}
	for _, n := range r.pipe.Nodes {
		if n.ID == nodeID {
			var b strings.Builder
			writeTemplateStrings(n.Config, &b)
			return b.String()
		}
	}
	return ""
}

// writeTemplateStrings collects every string in a config value, walking the
// same nested shapes VariableContext.resolveValue resolves.
func writeTemplateStrings(v interface{}, b *strings.Builder) {
	switch val := v.(type) {
	case string:
		b.WriteString(val)
		b.WriteByte('\n')
	case map[string]interface{}:
		for _, item := range val {
			writeTemplateStrings(item, b)
		}
	case []interface{}:
		for _, item := range val {
			writeTemplateStrings(item, b)
		}
	}
}

// truncateRecordedSQL bounds one recorded statement, visibly.
//
// The marker matters as much as the bound: a statement that simply stops is
// indistinguishable from one the engine built wrong, and someone reading a
// truncated INSERT looking for a bug would have no way to tell which they
// were looking at.
func truncateRecordedSQL(s string) string {
	if len(s) <= maxRecordedSQLBytes {
		return s
	}
	cut := maxRecordedSQLBytes
	// Never split a rune: the payload is JSON, and half a character would
	// make the whole event unreadable rather than just this field.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n-- [brokoli] statement truncated: %d of %d bytes recorded", cut, len(s))
}
