package engine

// Reading a task's dataset output (ADR-033 rollout phase 5a). Phase 2b
// mapped only the "scalar" output kind, which kept `task` nodes out of
// the data plane entirely: a task could return one value, never rows,
// so nothing downstream could consume real data from one.
//
// A dataset output is a file the TASK wrote, named by the candidate
// result manifest, so reading it is the one place in this rollout where
// the worker consumes attacker-shaped input from the sandbox. ADR-033
// section 7 rule 6 says exactly how: open relative to a trusted
// staging-directory descriptor with no-follow/beneath semantics, accept
// regular files only, enforce per-file and aggregate limits, and hash
// from the same held descriptor so a symlink or replacement race cannot
// swap the bytes between the check and the read.
//
// os.Root is that trusted descriptor: it resolves every path component
// beneath the root and refuses symlinks that escape it, which is the
// no-follow/beneath requirement without hand-rolling openat2. The file
// is opened ONCE and everything -- stat, hash, decode -- happens through
// that single handle.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/taskinterface"
)

// CodecNDJSON is ADR-033 section 8's baseline interoperable dataset
// format ("NDJSON remains the baseline interoperable format"). Arrow IPC
// is named there as the preferred optional transport when both sides
// advertise it; this phase implements the baseline only, and an
// unrecognized codec is refused by name rather than guessed at.
const CodecNDJSON = "ndjson/v1"

// maxTaskDatasetBytes caps a single dataset output file. Sized like
// task-bundle/v2's own archive cap (pkg/taskbundlev2.MaxArchiveBytes)
// rather than invented: both bound "one file a task's own tooling
// produced", and picking a different number for each would be two
// unexplained limits instead of one.
const maxTaskDatasetBytes = 64 << 20 // 64 MiB

// maxTaskDatasetRows bounds decode work independently of byte size,
// because a file of tiny rows can hold far more of them than the byte
// cap suggests -- the same reasoning pkg/taskbundlev2 gives for capping
// archive entries alongside archive bytes.
const maxTaskDatasetRows = 1_000_000

// readTaskDatasetOutput reads and verifies one dataset-kind output port,
// returning it as the row-shaped DataSet every other node type produces.
//
// stagingDir is the attempt's own output staging directory; rel is the
// path the task declared, interpreted strictly beneath it. wantSize and
// wantChecksum are the manifest's own claims, both verified against the
// bytes actually read -- a task that misreports either is a contract
// violation, not something to accept because the file happened to open.
func readTaskDatasetOutput(stagingDir, rel, codec string, wantSize int64, wantChecksum string) (*common.DataSet, error) {
	if codec != CodecNDJSON {
		return nil, fmt.Errorf("task dataset output declares codec %q, which this server cannot read (supported: %s)", codec, CodecNDJSON)
	}
	if rel == "" {
		return nil, fmt.Errorf("task dataset output declares no path")
	}

	root, err := os.OpenRoot(stagingDir)
	if err != nil {
		return nil, fmt.Errorf("open task output staging dir: %w", err)
	}
	defer root.Close()

	// Beneath-semantics open: a path escaping the staging dir, or
	// reached through a symlink out of it, fails here rather than
	// reading something the task was never allowed to name.
	f, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open task dataset output %q: %w", rel, err)
	}
	defer f.Close()

	// Everything below runs against this one handle -- never a second
	// path lookup -- so the bytes hashed are provably the bytes decoded
	// even if the task replaces the name concurrently.
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat task dataset output %q: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("task dataset output %q is not a regular file", rel)
	}
	if info.Size() > maxTaskDatasetBytes {
		return nil, fmt.Errorf("task dataset output %q is %d bytes, over the %d-byte cap", rel, info.Size(), int64(maxTaskDatasetBytes))
	}
	if wantSize != info.Size() {
		return nil, fmt.Errorf("task dataset output %q is %d bytes, but the result manifest declares %d", rel, info.Size(), wantSize)
	}

	// Hash and decode in one pass over the same handle: TeeReader feeds
	// every byte the decoder consumes into the digest, so the checksum
	// covers exactly what was parsed.
	sum := sha256.New()
	rows, err := decodeNDJSONRows(io.TeeReader(io.LimitReader(f, maxTaskDatasetBytes), sum))
	if err != nil {
		return nil, fmt.Errorf("task dataset output %q: %w", rel, err)
	}
	if got := "sha256:" + hex.EncodeToString(sum.Sum(nil)); !strings.EqualFold(got, wantChecksum) {
		return nil, fmt.Errorf("task dataset output %q failed integrity verification: manifest declares %s, content hashes to %s", rel, wantChecksum, got)
	}

	return &common.DataSet{Columns: datasetColumns(rows), Rows: rows}, nil
}

// decodeNDJSONRows reads newline-delimited JSON objects. Blank lines are
// skipped (a trailing newline is normal); anything that is not a JSON
// object is refused by line number, since "row 40000 is a string" is the
// only form of that error a task author can act on.
func decodeNDJSONRows(r io.Reader) ([]common.DataRow, error) {
	var rows []common.DataRow
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxTaskDatasetBytes)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		if len(rows) >= maxTaskDatasetRows {
			return nil, fmt.Errorf("more than %d rows, over this server's cap", maxTaskDatasetRows)
		}
		var row common.DataRow
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, fmt.Errorf("line %d is not a JSON object: %w", line, err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return rows, nil
}

// datasetColumns derives a stable column list from the rows' union of
// keys. Sorted for the same reason columnsOf (engine/expansion.go) sorts
// -- map iteration order is not stable, and a column list that reorders
// between runs of the same task would make artifacts differ for no
// semantic reason. The union, not the first row's keys, because NDJSON
// rows are independent objects and a later row may carry a key an
// earlier one omitted.
func datasetColumns(rows []common.DataRow) []string {
	seen := map[string]bool{}
	for _, row := range rows {
		for k := range row {
			seen[k] = true
		}
	}
	cols := make([]string, 0, len(seen))
	for k := range seen {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return cols
}

// inputDatasetFilename is where executeTaskBundle stages a task's input
// rows for the harness to read. Written by the trusted worker into the
// attempt directory (not the output staging dir, which is the sandbox's
// to write), so the harness only ever reads it.
const inputDatasetFilename = "input.ndjson"

// writeTaskInputDataset serializes a task's input rows to NDJSON in dir
// and returns the file's path, or "" when there is nothing to write.
//
// The same codec the output side reads (ADR-033 section 8's baseline),
// deliberately: one wire format in both directions means a task's output
// feeding another task's input is the same bytes decoded the same way,
// which is exactly what the cross-language gate exercises.
//
// Column order is not encoded -- NDJSON rows are self-describing
// objects, and a task reading them gets the row's own keys. A column
// present on some rows and absent on others stays absent rather than
// being filled with a null the producer never emitted.
func writeTaskInputDataset(dir string, input *common.DataSet) (string, error) {
	if input == nil || len(input.Rows) == 0 {
		return "", nil
	}
	path := filepath.Join(dir, inputDatasetFilename)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- dir is this attempt's own worker-created scratch directory and the filename is a constant; O_EXCL additionally refuses a pre-existing path rather than writing through one
	if err != nil {
		return "", fmt.Errorf("stage task input dataset: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for i, row := range input.Rows {
		if err := enc.Encode(row); err != nil { // Encode writes its own newline
			return "", fmt.Errorf("stage task input dataset: row %d: %w", i, err)
		}
	}
	if err := w.Flush(); err != nil {
		return "", fmt.Errorf("stage task input dataset: %w", err)
	}
	return path, nil
}

// maxFullValidationRows is where input validation switches from
// checking every row to checking a deterministic sample (ADR-032
// section 10's `full` vs `sample` modes). Section 10 makes `sample` the
// default for datasets precisely because validating every row of a
// large one costs more than the guarantee is worth, while `full` stays
// the default for scalars and parameters.
//
// Phase 3 deferred sampling as having "no reachable target" -- true
// then, because a task produced one scalar and consumed nothing. Rows
// exist now, so the mode is real.
const maxFullValidationRows = 1000

// sampleValidationRate is how many rows the sample mode checks out of
// every N once a dataset is over maxFullValidationRows: every 10th row,
// deterministically by index rather than randomly, so a retry validates
// exactly the same rows as the attempt before it (section 10: "retries
// validate the same rows").
const sampleValidationRate = 10

// validateTaskInputDataset checks rows crossing INTO a task against the
// row type its input port declares.
//
// Skipped entirely when the port declares no row shape -- ADR-032
// section 6's "absence is honest": a dataset port with no declared row
// accepts any row, and inventing a constraint the contract never stated
// would reject data the author meant to allow.
//
// The report never claims more than was checked (section 10: "Sampling
// never permits the system to claim ... that an entire dataset was
// valid"), which is why CheckedRows and Mode are carried on the failure
// rather than implied.
func validateTaskInputDataset(port taskinterface.PortValue, input *common.DataSet) error {
	return validateDatasetRows(port, input, taskinterface.DirectionInput, "input", ErrTaskInputContractViolation)
}

// validateTaskDatasetOutput is the output boundary's half of the same
// rule (section 10: "output validation occurs before the trusted worker
// commits it"). Same sampling, same honesty about what was checked --
// only the direction, port name and sentinel differ, because an output
// violation is the task's own contract error while an input violation
// is the upstream's.
func validateTaskDatasetOutput(port taskinterface.PortValue, out *common.DataSet) error {
	return validateDatasetRows(port, out, taskinterface.DirectionOutput, "result", ErrTaskOutputContractViolation)
}

func validateDatasetRows(port taskinterface.PortValue, ds *common.DataSet, direction taskinterface.Direction, portName string, sentinel error) error {
	if ds == nil || len(ds.Rows) == 0 {
		return nil
	}
	if port.Kind != taskinterface.ValueDataset || port.Row == nil {
		return nil
	}

	mode := taskinterface.ModeFull
	stride := 1
	if len(ds.Rows) > maxFullValidationRows {
		mode = taskinterface.ModeSample
		stride = sampleValidationRate
	}

	checked := 0
	for i := 0; i < len(ds.Rows); i += stride {
		checked++
		if err := taskinterface.ValidateValue(map[string]interface{}(ds.Rows[i]), *port.Row, fmt.Sprintf("$[%d]", i)); err != nil {
			failure := taskinterface.NewValidationFailure(
				direction, portName, *port.Row,
				map[string]interface{}(ds.Rows[i]), err, fmt.Sprintf("$[%d]", i), false,
			)
			failure.CheckedRows = checked
			failure.Mode = mode
			// Both verbs are %w on purpose: callers match the sentinel
			// with errors.Is to classify the fault, and reach the
			// structured ValidationFailure with errors.As to read what
			// section 10 requires a report to carry (mode, checked row
			// count, contract path). A %v here would render those fields
			// into a string and put them out of reach, which defeats
			// having built them.
			return fmt.Errorf("%w: %w", sentinel, failure)
		}
	}
	return nil
}
