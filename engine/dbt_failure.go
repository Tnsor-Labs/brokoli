package engine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/dbtmanifest"
)

// Telling dbt's failure modes apart (ADR-025 Phase 2, #353).
//
// "dbt failed" is not one thing, and a run that reports it as one leaves the
// person reading it to open the log and work out which. The modes below were
// each produced against real dbt 1.8.9 and are distinguishable from what the
// invocation leaves behind:
//
//	mode              exit    run_results.json
//	compile error     2       absent
//	failing model     1       present, node status "error"
//	failing test      1       present, node status "fail" with a count
//	killed process    signal  absent
//	dbt not installed exec    absent
//
// The exit code alone cannot separate a failing model from a failing test --
// both are 1 -- and the absence of run_results.json cannot separate a
// compile error from a kill. So neither signal is sufficient alone, and the
// classifier uses both plus the context's own state.
//
// Why bother: a failing test means the models built and the data is wrong,
// while a compile error means nothing ran at all. Those call for different
// actions from whoever is on call, and collapsing them into "dbt failed"
// throws away the distinction the artifacts already make.

// DBTFailureKind names what went wrong, so a caller can act on it rather
// than parse a message.
type DBTFailureKind string

const (
	// DBTFailureCompile means dbt could not build the project's DAG:
	// unresolvable ref, bad Jinja, a missing dependency. Nothing ran, so
	// nothing was written.
	DBTFailureCompile DBTFailureKind = "compile_error"
	// DBTFailureModel means one or more models failed while executing.
	// Models that do not depend on them still ran and are committed.
	DBTFailureModel DBTFailureKind = "model_error"
	// DBTFailureTest means the models built and a data test failed. The
	// distinction from a model error is the one most worth keeping: the
	// pipeline worked and the data is wrong.
	DBTFailureTest DBTFailureKind = "test_failure"
	// DBTFailureCancelled means the run was cancelled or the node timed
	// out, and dbt was stopped part-way. What it had already committed
	// stays committed.
	DBTFailureCancelled DBTFailureKind = "cancelled"
	// DBTFailureNotInstalled means dbt could not be executed at all.
	// ADR-026 requires this to be named rather than discovered, since it
	// is an operator's environment problem and not a pipeline's.
	DBTFailureNotInstalled DBTFailureKind = "dbt_not_available"
	// DBTFailureUnknown is the honest answer when the artifacts do not
	// support a more specific one. It is not a synonym for any of the
	// above: guessing between them would be worse than saying so.
	DBTFailureUnknown DBTFailureKind = "unknown"
)

// DBTFailure is a classified dbt failure.
type DBTFailure struct {
	Kind    DBTFailureKind
	Command string
	// Nodes names the dbt nodes responsible, when the artifacts identify
	// them. Empty for failures that happened before anything ran.
	Nodes []string
	// Detail is dbt's own explanation where there is one.
	Detail string
	err    error
}

func (f *DBTFailure) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "dbt %s: %s", f.Command, f.headline())
	if len(f.Nodes) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(f.Nodes, ", "))
	}
	if f.Detail != "" {
		fmt.Fprintf(&b, ": %s", f.Detail)
	}
	return b.String()
}

func (f *DBTFailure) Unwrap() error { return f.err }

func (f *DBTFailure) headline() string {
	switch f.Kind {
	case DBTFailureCompile:
		return "the project did not compile, so no model ran"
	case DBTFailureModel:
		return "a model failed"
	case DBTFailureTest:
		return "the models built but a data test failed"
	case DBTFailureCancelled:
		return "cancelled before it finished"
	case DBTFailureNotInstalled:
		return "dbt could not be run on this host"
	default:
		return "failed for a reason the artifacts do not identify"
	}
}

// classifyDBTFailure works out which mode a failed invocation is in.
//
// results is what the project's run_results.json parsed to, or nil when it
// was absent or unreadable -- which is itself a signal, since dbt writes it
// for anything that got as far as executing.
func classifyDBTFailure(
	command string,
	execErr error,
	ctxErr error,
	results *dbtmanifest.RunResults,
) *DBTFailure {
	f := &DBTFailure{Kind: DBTFailureUnknown, Command: command, err: execErr}

	// Cancellation first: a killed process leaves no results, which looks
	// exactly like a compile error from the artifacts alone. The context
	// is the only thing that can tell them apart, so it is asked first.
	if ctxErr != nil {
		f.Kind = DBTFailureCancelled
		f.Detail = ctxErr.Error()
		return f
	}

	// Could not execute dbt at all. Distinguished from anything dbt itself
	// reported, because it is the operator's environment rather than the
	// project (ADR-026).
	var execNotFound *exec.Error
	if errors.As(execErr, &execNotFound) || errors.Is(execErr, exec.ErrNotFound) {
		f.Kind = DBTFailureNotInstalled
		f.Detail = "install dbt-core and the adapter for this connection, and make dbt available on PATH"
		return f
	}

	// With results, the node statuses say what happened. A test failure and
	// a model error are both exit 1 and only the statuses separate them.
	if results != nil {
		var modelErrors, testFailures []string
		for _, n := range results.Results {
			switch n.Status {
			case dbtmanifest.StatusError, dbtmanifest.StatusRuntime:
				modelErrors = append(modelErrors, shortDBTNode(n.UniqueID))
			case dbtmanifest.StatusFail:
				testFailures = append(testFailures, shortDBTNode(n.UniqueID))
			}
		}
		switch {
		case len(modelErrors) > 0:
			// A model error outranks a test failure when both occur: the
			// build is broken, which is the more urgent fact, and the
			// failing test may simply be downstream of it.
			f.Kind = DBTFailureModel
			f.Nodes = modelErrors
			f.Detail = firstMessageFor(results, dbtmanifest.StatusError, dbtmanifest.StatusRuntime)
			return f
		case len(testFailures) > 0:
			f.Kind = DBTFailureTest
			f.Nodes = testFailures
			f.Detail = firstMessageFor(results, dbtmanifest.StatusFail)
			return f
		}
	}

	// No results and not cancelled: dbt exited before executing anything,
	// which is what a compile error looks like. Exit 2 corroborates it, but
	// the absent artifact is the stronger signal -- dbt writes the file for
	// anything that got as far as running.
	if results == nil {
		f.Kind = DBTFailureCompile
		f.Detail = "dbt reported no run results, so it stopped before executing any node"
		return f
	}

	return f
}

// shortDBTNode trims a dbt unique_id to its name for a message. The full id
// stays in the per-model records; a sentence wants the name.
//
// dbt ids are <resource>.<project>.<name>, and a test appends a hash:
//
//	model.my_project.city_totals
//	test.my_project.not_null_orders_id.8cf5724805
//
// So the name is the third segment, not the last. Taking the last one --
// which is what this did first -- names a model correctly and reports a
// failing test as "8cf5724805", which tells a reader nothing.
func shortDBTNode(uniqueID string) string {
	parts := strings.Split(uniqueID, ".")
	if len(parts) >= 3 && parts[2] != "" {
		return parts[2]
	}
	return uniqueID
}

func firstMessageFor(results *dbtmanifest.RunResults, statuses ...dbtmanifest.NodeStatus) string {
	want := map[dbtmanifest.NodeStatus]bool{}
	for _, s := range statuses {
		want[s] = true
	}
	for _, n := range results.Results {
		if want[n.Status] && n.Message != "" {
			// dbt's messages are multi-line; the first line is the one a
			// person reads.
			if i := strings.IndexByte(n.Message, '\n'); i > 0 {
				return strings.TrimSpace(n.Message[:i])
			}
			return strings.TrimSpace(n.Message)
		}
	}
	return ""
}

// contextErrOf reports a context's error without panicking on a nil one,
// which a directly-constructed Runner in a test can have.
func contextErrOf(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
