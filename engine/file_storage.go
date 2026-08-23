package engine

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// Where file nodes actually put their files, and who can see them.
//
// source_file and sink_file read and write the worker's own filesystem.
// With one worker that is unambiguous. With several it is not: each pod
// has its own copy of the data directories, so a file written by one run
// is visible only to the pod that ran it.
//
// Within a single run this is fine — every node of a run executes on one
// worker. Across runs it is a coin flip, and the two outcomes are not
// equally visible:
//
//   - the later run lands on a pod without the file and fails with
//     "no such file or directory", which is true but says nothing about
//     why the file it wrote an hour ago is gone;
//   - or it lands on a pod that has an *older* copy, reads it, and
//     succeeds against stale data. No error, wrong numbers.
//
// Brokoli cannot make a local filesystem shared; that is an operator
// decision (an RWX volume mounted at the data directories). What it can
// do is stop being quiet about it. The hazard is reported at the three
// points where someone can act on it: at startup, when a pipeline is
// validated, and when a read actually fails.

// distributedWorkers records whether pipeline nodes may run on more than
// one worker process. Set once at startup by the server; false means a
// single process runs everything and file nodes are unambiguous.
var distributedWorkers atomic.Bool

// SetDistributedWorkers declares whether this deployment dispatches work
// to separate worker processes. Called by the server once its job queue
// wiring is known.
func SetDistributedWorkers(v bool) { distributedWorkers.Store(v) }

// fileStorageShared reports whether the data directories are on storage
// every worker can see.
//
// Declared rather than detected. Detection would mean probing the
// filesystem from several pods and inferring — the kind of guess that is
// right until it is silently wrong, which is the failure mode this whole
// file exists to remove. An operator who mounts an RWX volume knows they
// did; the platform does not.
func fileStorageShared() bool {
	v := os.Getenv("BROKOLI_DATA_DIRS_SHARED")
	if v == "" {
		return false
	}
	shared, err := strconv.ParseBool(v)
	return err == nil && shared
}

// unsharedFileStorage reports whether file nodes are unreliable across
// runs in this deployment.
func unsharedFileStorage() bool {
	return distributedWorkers.Load() && !fileStorageShared()
}

// FileStorageWarning returns the operator-facing explanation of the
// hazard, or "" when there is none. Exported for the server to log at
// startup.
func FileStorageWarning() string {
	if !unsharedFileStorage() {
		return ""
	}
	return fmt.Sprintf(
		"file nodes (source_file, sink_file) read and write each worker's own filesystem, and this deployment runs more than one worker. "+
			"A file written by one run is visible only to the pod that ran it, so a later run reading it will fail — or read a stale copy left by an older run and succeed with wrong data. "+
			"Mount shared storage (an RWX volume) at %s on every worker and set BROKOLI_DATA_DIRS_SHARED=1 to confirm it. "+
			"Pipelines that write and read a file within a single run are unaffected.",
		strings.Join(allowedDataDirs, ", "))
}

// describeMissingFile appends the likely cause to a failed read when the
// deployment makes one likely. On a single-worker deployment, or one with
// shared storage, the plain error is the whole story and nothing is
// added.
func describeMissingFile(path string, err error) error {
	if !unsharedFileStorage() || !os.IsNotExist(underlyingErr(err)) {
		return err
	}
	host, _ := os.Hostname()
	return fmt.Errorf("%w — this worker (%s) has its own filesystem and this deployment runs several. "+
		"If %s was written by an earlier run, it is on whichever pod ran that run. "+
		"Mount shared storage at the data directories and set BROKOLI_DATA_DIRS_SHARED=1",
		err, host, path)
}

// underlyingErr unwraps to the deepest error, so os.IsNotExist can see a
// wrapped *PathError.
func underlyingErr(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		next := u.Unwrap()
		if next == nil {
			return err
		}
		err = next
	}
}
