package engine

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/dbtmanifest"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Recording what each dbt model did, from one invocation (ADR-025, #353).
//
// #353 measured invoking dbt per model at 30x the cost of a single build,
// and found the alternative gives up nothing: one invocation reports every
// node individually. So the dbt node still runs dbt once, and this turns
// that one invocation into a durable record per model.
//
// The records go where ADR-017 already put per-instance execution history:
// an execution_attempts row per (node, instance_key), with the model's dbt
// unique_id as the instance key. That is the same mechanism dynamic
// expansion uses for its per-item instances, and reusing it means per-model
// history is queryable through machinery that already exists rather than a
// second, parallel notion of "part of a node".
//
// Nothing about the pipeline graph changes. A dbt node stays one node; what
// changes is that its run history can answer "which model failed" instead of
// "the dbt node failed".

// recordDBTModelOutcomes writes one durable attempt record and one log line
// per model, and returns a summary.
//
// Best-effort on the durable records: a failure to write history must not
// fail a run whose dbt work actually succeeded. The log lines are the
// fallback, and they carry the same facts.
func (r *Runner) recordDBTModelOutcomes(
	node models.Node,
	nodeAttempt int,
	project *dbtmanifest.Project,
	results *dbtmanifest.RunResults,
) dbtmanifest.Summary {
	outcomes := dbtmanifest.Reconcile(project, results)
	summary := dbtmanifest.Summarize(outcomes)

	attemptStore, canRecord := r.store.(store.ExecutionAttemptStore)
	canRecord = canRecord && !r.dryRun

	for _, o := range outcomes {
		// A node this invocation never covered has no history to write:
		// nothing happened to it, and a record saying so would be
		// indistinguishable from one saying it was skipped.
		if o.Status == dbtmanifest.OutcomeNotSelected {
			continue
		}

		fields := map[string]string{
			"dbt_node":     o.Node.UniqueID,
			"dbt_resource": string(o.Node.Type),
			"dbt_status":   string(o.Status),
		}
		if o.Result != nil && o.Result.RelationName != "" {
			fields["dbt_relation"] = o.Result.RelationName
		}

		switch o.Status {
		case dbtmanifest.OutcomeSucceeded:
			r.logWithTrace(node.ID, models.LogLevelInfo, "", nodeAttempt, fields,
				"%s %s in %s", o.Node.Type, o.Node.Name, outcomeDuration(o))
		case dbtmanifest.OutcomeWarned:
			r.logWithTrace(node.ID, models.LogLevelWarning, "", nodeAttempt, fields,
				"%s %s warned: %s", o.Node.Type, o.Node.Name, outcomeMessage(o))
		case dbtmanifest.OutcomeFailed:
			r.logWithTrace(node.ID, models.LogLevelError, "", nodeAttempt, fields,
				"%s %s failed: %s", o.Node.Type, o.Node.Name, outcomeMessage(o))
		case dbtmanifest.OutcomeSkipped:
			// The blocked-by list is the whole point of reconciling: dbt
			// says "skipped" and stops, and this says which model to go
			// and fix.
			if len(o.BlockedBy) > 0 {
				fields["dbt_blocked_by"] = o.BlockedBy[0]
				r.logWithTrace(node.ID, models.LogLevelWarning, "", nodeAttempt, fields,
					"%s %s was skipped because %s failed", o.Node.Type, o.Node.Name, o.BlockedBy[0])
			} else {
				r.logWithTrace(node.ID, models.LogLevelWarning, "", nodeAttempt, fields,
					"%s %s was skipped", o.Node.Type, o.Node.Name)
			}
		}

		if !canRecord {
			continue
		}
		if err := r.writeDBTAttempt(attemptStore, node, nodeAttempt, o); err != nil {
			// History is worth having and not worth failing a run for.
			r.log(node.ID, models.LogLevelWarning,
				"could not record per-model history for %s: %v", o.Node.UniqueID, err)
		}
	}

	return summary
}

// writeDBTAttempt records one model's outcome as an execution attempt.
//
// Written straight to its terminal state rather than through the
// claim/acknowledge lifecycle: nothing was claimed or leased for a model, so
// going through that dance would misrepresent what happened. This mirrors
// how a reused expansion instance is recorded, and leaves FencingGeneration
// at zero, which accurately says "never claimed".
func (r *Runner) writeDBTAttempt(
	attemptStore store.ExecutionAttemptStore,
	node models.Node,
	nodeAttempt int,
	o dbtmanifest.Outcome,
) error {
	status := models.AttemptStatusCompleted
	errMsg := ""
	switch o.Status {
	case dbtmanifest.OutcomeFailed:
		status = models.AttemptStatusFailed
		errMsg = outcomeMessage(o)
	case dbtmanifest.OutcomeSkipped:
		status = models.AttemptStatusFailed
		if len(o.BlockedBy) > 0 {
			errMsg = fmt.Sprintf("skipped: %s failed", o.BlockedBy[0])
		} else {
			errMsg = "skipped by dbt"
		}
	case dbtmanifest.OutcomeWarned:
		// A warn is not a failure in dbt's severity model, so it is not
		// one here; the message keeps what dbt said.
		errMsg = outcomeMessage(o)
	}

	return r.store.WithTx(func(tx *sql.Tx) error {
		return attemptStore.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID:       r.run.ID,
			NodeID:      node.ID,
			InstanceKey: o.Node.UniqueID,
			Attempt:     nodeAttempt,
			Status:      status,
			Error:       errMsg,
			// Scoped by run, node, model and attempt, so a retry of the
			// same dbt node writes a distinct row rather than colliding
			// with the previous attempt's.
			IdempotencyKey: fmt.Sprintf("%s:%s:%s:%d", r.run.ID, node.ID, o.Node.UniqueID, nodeAttempt),
		})
	})
}

func outcomeDuration(o dbtmanifest.Outcome) time.Duration {
	if o.Result == nil {
		return 0
	}
	return o.Result.ExecutionTime.Round(time.Millisecond)
}

func outcomeMessage(o dbtmanifest.Outcome) string {
	if o.Result == nil || o.Result.Message == "" {
		return "no message from dbt"
	}
	return o.Result.Message
}

// recordDBTModelOutcomesFromProject reads a project's manifest and run
// results and records what each model did. Reports false when the artifacts
// are not both readable, in which case the caller reports what it always
// did -- per-model history is an improvement on the run log, never a
// precondition for it.
//
// Both paths resolve against the project directory, never the process
// working directory. Reading an artifact relative to the engine's cwd is
// the defect #348 shipped with.
func (r *Runner) recordDBTModelOutcomesFromProject(
	node models.Node,
	projectDir string,
) (dbtmanifest.Summary, bool) {
	project, err := dbtmanifest.ParseProject(projectDir)
	if err != nil {
		r.log(node.ID, models.LogLevelInfo,
			"per-model history unavailable (%v); the run log below is dbt's own output", err)
		return dbtmanifest.Summary{}, false
	}
	results, err := dbtmanifest.ParseRunResultsForProject(projectDir)
	if err != nil {
		r.log(node.ID, models.LogLevelInfo,
			"per-model history unavailable (%v); the run log below is dbt's own output", err)
		return dbtmanifest.Summary{}, false
	}
	return r.recordDBTModelOutcomes(node, 0, project, results), true
}
