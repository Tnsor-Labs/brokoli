//go:build unix

package codeexec

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Live pool tests: real python3 workers over the real socket protocol,
// like the engine's code-node tests.

func testPool(t *testing.T) *Pool {
	t.Helper()
	p := NewPool()
	t.Cleanup(p.Close)
	return p
}

func inlineReq(script string, rows []map[string]interface{}, out string) Request {
	return Request{
		Script:       script,
		Interpreter:  "python3",
		InlineRows:   rows,
		InputColumns: []string{"a"},
		OutputNDJSON: out,
		Timeout:      30 * time.Second,
	}
}

func inlineTSReq(t *testing.T, script string, rows []map[string]interface{}, out string) Request {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	return Request{
		Language: "typescript", Script: script, Interpreter: node,
		InlineRows: rows, InputColumns: []string{"a"}, OutputNDJSON: out, Timeout: 30 * time.Second,
	}
}

func TestPoolRunsAndReusesTypeScriptWorkers(t *testing.T) {
	p := testPool(t)
	dir := t.TempDir()
	script := `output_data = { columns, rows: rows.map(row => ({ a: row.a * 2 })) };`
	first, err := p.Exec(context.Background(), inlineTSReq(t, script, []map[string]interface{}{{"a": float64(2)}}, filepath.Join(dir, "one.ndjson")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Exec(context.Background(), inlineTSReq(t, script, []map[string]interface{}{{"a": float64(3)}}, filepath.Join(dir, "two.ndjson")))
	if err != nil {
		t.Fatal(err)
	}
	if first.Meta.Language != "typescript" || first.Meta.WrapperVersion != 1 || second.Rows[0]["a"] != float64(6) {
		t.Fatalf("TypeScript result/meta wrong: first=%+v second=%+v", first, second)
	}
	if !second.Meta.Warm || p.WorkerBoots() != 1 {
		t.Fatalf("TypeScript worker was not reused: warm=%v boots=%d", second.Meta.Warm, p.WorkerBoots())
	}
}

func TestPoolSeparatesLanguagesAndMakesCPULimitsOneShot(t *testing.T) {
	if subKey("python", "/runtime", Limits{}) == subKey("typescript", "/runtime", Limits{}) {
		t.Fatal("language missing from sub-pool identity")
	}
	p := testPool(t)
	dir := t.TempDir()
	for i := range 2 {
		req := inlineTSReq(t, `output_data = { columns: [], rows: [] };`, nil, filepath.Join(dir, fmt.Sprintf("%d.ndjson", i)))
		req.Limits.CPUSeconds = 10
		if _, err := p.Exec(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if p.WorkerBoots() != 2 {
		t.Fatalf("CPU-limited executions reused a cumulative-limit process: boots=%d", p.WorkerBoots())
	}
}

func TestTypeScriptHeapAbortRetainsBoundedStderr(t *testing.T) {
	p := testPool(t)
	req := inlineTSReq(t, `
    const values = [];
    while (true) values.push(new Array(100000).fill("memory"));
  `, nil, filepath.Join(t.TempDir(), "oom.ndjson"))
	req.Limits.MemoryMB = 32
	_, err := p.Exec(context.Background(), req)
	var died *ErrWorkerDied
	if !errors.As(err, &died) {
		t.Fatalf("want ErrWorkerDied from fatal V8 OOM, got %v", err)
	}
	if !strings.Contains(strings.ToLower(died.Stderr), "heap") || !strings.Contains(strings.ToLower(died.Stderr), "out of memory") {
		t.Fatalf("V8 fatal diagnostic missing from stderr tail: %q", died.Stderr)
	}
	if len(died.Stderr) > workerStderrTailBytes+100 {
		t.Fatalf("worker stderr was not bounded: %d bytes", len(died.Stderr))
	}
}

func TestCPULimitedRequestAfterCloseIsRejected(t *testing.T) {
	p := NewPool()
	p.Close()
	req := inlineReq(`output_data = {"columns": [], "rows": []}`, nil, filepath.Join(t.TempDir(), "out.ndjson"))
	req.Limits.CPUSeconds = 1
	if _, err := p.Exec(context.Background(), req); err == nil || !strings.Contains(err.Error(), "shut down") {
		t.Fatalf("closed pool accepted CPU-limited request: %v", err)
	}
}

func TestWorkerExitBeforeConnectReturnsStderrImmediately(t *testing.T) {
	interpreter := filepath.Join(t.TempDir(), "fail-worker")
	if err := os.WriteFile(interpreter, []byte("#!/bin/sh\necho startup exploded >&2\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err := spawnWorker(context.Background(), "python", interpreter, Limits{}, t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "startup exploded") {
		t.Fatalf("startup stderr missing: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("pre-connect worker death took %s", elapsed)
	}
}

func TestWorkerHelloRequiresLanguage(t *testing.T) {
	interpreter := filepath.Join(t.TempDir(), "missing-language")
	script := `#!/usr/bin/env python3
import json, socket, struct, sys, time
path = sys.argv[sys.argv.index("--socket") + 1]
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(path)
body = json.dumps({"protocol_version": 1, "wrapper_version": 2, "pid": 1}).encode()
s.sendall(struct.pack(">IBB", len(body), 1, 0x10) + body)
time.sleep(5)
`
	if err := os.WriteFile(interpreter, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := spawnWorker(context.Background(), "python", interpreter, Limits{}, t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "language") {
		t.Fatalf("worker without hello language was accepted: %v", err)
	}
}

func TestPoolReusesWarmWorkers(t *testing.T) {
	p := testPool(t)
	dir := t.TempDir()
	script := `output_data = {"columns": columns, "rows": [{"a": r["a"] * 2} for r in rows]}`

	first, err := p.Exec(context.Background(), inlineReq(script, []map[string]interface{}{{"a": float64(1)}}, filepath.Join(dir, "o1.ndjson")))
	if err != nil {
		t.Fatal(err)
	}
	if first.Meta.Warm {
		t.Fatal("first exec cannot be warm")
	}
	second, err := p.Exec(context.Background(), inlineReq(script, []map[string]interface{}{{"a": float64(2)}}, filepath.Join(dir, "o2.ndjson")))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Meta.Warm {
		t.Fatal("second exec should reuse the warm worker")
	}
	if boots := p.WorkerBoots(); boots != 1 {
		t.Fatalf("expected 1 boot, got %d", boots)
	}
	// Inline input keeps the v1 stdin contract: the result returns
	// inline too, no file involved.
	if second.Path != "" || len(second.Rows) != 1 || second.Rows[0]["a"] != float64(4) {
		t.Fatalf("inline result wrong: %+v", second)
	}
	if second.Meta.WrapperVersion != 2 || second.Meta.ProtocolVersion != 1 {
		t.Fatalf("meta wrong: %+v", second.Meta)
	}
}

func TestPoolSurvivesUserExceptionsAndCrashes(t *testing.T) {
	p := testPool(t)
	dir := t.TempDir()

	// A user exception is normal traffic: typed error, worker stays warm.
	_, err := p.Exec(context.Background(), inlineReq(`raise ValueError("boom")`, nil, filepath.Join(dir, "e.ndjson")))
	var scriptErr *ErrScript
	if !errors.As(err, &scriptErr) || scriptErr.Kind != ErrKindUserException || !strings.Contains(scriptErr.Message, "boom") {
		t.Fatalf("want user_exception 'boom', got %v", err)
	}

	// A hard crash kills the worker; the pool recovers on the next exec.
	_, err = p.Exec(context.Background(), inlineReq("import os\nos._exit(1)\n", nil, filepath.Join(dir, "c.ndjson")))
	if err == nil || !strings.Contains(err.Error(), "worker died") {
		t.Fatalf("want worker-died error, got %v", err)
	}
	ok, err := p.Exec(context.Background(), inlineReq(
		`output_data = {"columns": ["a"], "rows": [{"a": 1}]}`, nil, filepath.Join(dir, "r.ndjson")))
	if err != nil {
		t.Fatal(err)
	}
	if ok.RowsWritten != 1 {
		t.Fatalf("recovery exec wrong: %+v", ok)
	}
	// exception (warm after boot 1) + crash (same worker) + respawn = 2 boots.
	if boots := p.WorkerBoots(); boots != 2 {
		t.Fatalf("expected 2 boots, got %d", boots)
	}
}

func TestPoolMutationAfterPassIsTyped(t *testing.T) {
	p := testPool(t)
	dir := t.TempDir()
	script := `
kept = []
for r in rows:
    kept.append(r)
kept[0]["a"] = 99
`
	// LazyRows strictness belongs to the FILE data plane only (inline
	// input keeps v1's freely-mutable stdin rows), so stage NDJSON.
	inp := filepath.Join(dir, "in.ndjson")
	if err := os.WriteFile(inp, []byte("{\"a\": 1}\n{\"a\": 2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := Request{
		Script: script, Interpreter: "python3",
		InputNDJSON: inp, InputColumns: []string{"a"},
		OutputNDJSON: filepath.Join(dir, "m.ndjson"),
		Timeout:      30 * time.Second,
	}
	_, err := p.Exec(context.Background(), req)
	var scriptErr *ErrScript
	if !errors.As(err, &scriptErr) || scriptErr.Kind != ErrKindMutationAfterPass {
		t.Fatalf("want mutation_after_pass, got %v", err)
	}
	if !strings.Contains(scriptErr.Message, "already moved on") {
		t.Fatalf("mutation message lost its explanation: %q", scriptErr.Message)
	}
}

func TestPoolZeroEmitReturnsInline(t *testing.T) {
	p := testPool(t)
	dir := t.TempDir()
	script := `
begin_emit(["a"])
for r in rows:
    if r["a"] > 100:
        emit(r)
`
	res, err := p.Exec(context.Background(), inlineReq(script, []map[string]interface{}{{"a": float64(1)}}, filepath.Join(dir, "z.ndjson")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "" || len(res.Rows) != 0 || len(res.Columns) != 1 || res.Columns[0] != "a" {
		t.Fatalf("zero-emit contract broken: %+v", res)
	}
}

func TestPoolTimeoutKillsOnlyTheBusyWorker(t *testing.T) {
	p := testPool(t)
	dir := t.TempDir()

	var wg sync.WaitGroup
	wg.Add(2)
	var slowErr, fastErr error
	var fast *Result
	go func() {
		defer wg.Done()
		req := inlineReq("import time\ntime.sleep(60)\n", nil, filepath.Join(dir, "slow.ndjson"))
		req.Timeout = 2 * time.Second
		_, slowErr = p.Exec(context.Background(), req)
	}()
	go func() {
		defer wg.Done()
		time.Sleep(300 * time.Millisecond)
		req := inlineReq("import time\ntime.sleep(3)\noutput_data = {\"columns\": [\"a\"], \"rows\": [{\"a\": 1}]}\n", nil, filepath.Join(dir, "fast.ndjson"))
		fast, fastErr = p.Exec(context.Background(), req)
	}()
	wg.Wait()
	if slowErr == nil || !strings.Contains(slowErr.Error(), "timed out after 2s") {
		t.Fatalf("want timeout, got %v", slowErr)
	}
	if fastErr != nil {
		t.Fatalf("concurrent exec should be untouched by the other's timeout: %v", fastErr)
	}
	if fast.RowsWritten != 1 {
		t.Fatalf("concurrent exec result wrong: %+v", fast)
	}
}

func TestPoolOverflowSpawnsOneShotsInsteadOfDeadlocking(t *testing.T) {
	t.Setenv("BROKOLI_CODE_POOL_MAX_WORKERS", "1")
	p := testPool(t)
	dir := t.TempDir()

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := inlineReq(
				"import time\ntime.sleep(1)\noutput_data = {\"columns\": [\"a\"], \"rows\": [{\"a\": 1}]}\n",
				nil, filepath.Join(dir, string(rune('a'+i))+".ndjson"))
			_, errs[i] = p.Exec(context.Background(), req)
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("overflow execs deadlocked")
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("exec %d failed: %v", i, err)
		}
	}
	if boots := p.WorkerBoots(); boots < 2 {
		t.Fatalf("expected overflow one-shots to spawn, boots=%d", boots)
	}
}

func TestPoolStagedNDJSONInput(t *testing.T) {
	p := testPool(t)
	dir := t.TempDir()
	inp := filepath.Join(dir, "in.ndjson")
	if err := os.WriteFile(inp, []byte("{\"a\": 1}\n{\"a\": 2}\n{\"a\": 3}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := Request{
		Script:       `output_data = {"columns": columns, "rows": ({"a": r["a"] * 10} for r in rows)}`,
		Interpreter:  "python3",
		InputNDJSON:  inp,
		InputColumns: []string{"a"},
		OutputNDJSON: filepath.Join(dir, "out.ndjson"),
		Timeout:      30 * time.Second,
	}
	res, err := p.Exec(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.RowsWritten != 3 || res.Path == "" {
		t.Fatalf("staged result wrong: %+v", res)
	}
}

func TestPoolForwardsPrintsAsLogsWithoutCorruptingResults(t *testing.T) {
	p := testPool(t)
	dir := t.TempDir()
	var logs []string
	req := inlineReq(
		"print(\"hello from user code\")\noutput_data = {\"columns\": [\"a\"], \"rows\": [{\"a\": 7}]}\n",
		nil, filepath.Join(dir, "p.ndjson"))
	req.LogHandler = func(level, message string) { logs = append(logs, level+": "+message) }
	res, err := p.Exec(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.RowsWritten != 1 {
		t.Fatalf("print corrupted the result: %+v", res)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "hello from user code") {
		t.Fatalf("user print did not reach logs: %v", logs)
	}
}

func TestReadFrameRefusesBadVersionAndOversize(t *testing.T) {
	var buf bytes.Buffer
	header := make([]byte, 6)
	binary.BigEndian.PutUint32(header[0:4], 2)
	header[4] = 99 // unsupported version
	header[5] = FrameHello
	buf.Write(header)
	buf.WriteString("{}")
	if _, _, err := ReadFrame(&buf); err == nil || !strings.Contains(err.Error(), "unsupported code protocol version 99") {
		t.Fatalf("want version refusal, got %v", err)
	}

	buf.Reset()
	binary.BigEndian.PutUint32(header[0:4], MaxFramePayload+1)
	header[4] = CodeProtocolVersion
	buf.Write(header)
	if _, _, err := ReadFrame(&buf); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want oversize refusal, got %v", err)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	msg := ExecMsg{ExecID: "x1", Script: "pass", TimeoutMs: 1000,
		Input: ExecInput{Mode: "inline", Rows: []map[string]interface{}{{"a": 1.0}}}}
	if err := WriteFrame(&buf, FrameExec, msg); err != nil {
		t.Fatal(err)
	}
	frameType, payload, err := ReadFrame(&buf)
	if err != nil || frameType != FrameExec {
		t.Fatalf("round trip: %v (type %#x)", err, frameType)
	}
	var got ExecMsg
	if err := unmarshalStrictEnough(payload, &got); err != nil || got.ExecID != "x1" || got.Input.Mode != "inline" {
		t.Fatalf("payload mangled: %+v %v", got, err)
	}
}

func TestPoolDeliversTypedProgress(t *testing.T) {
	p := testPool(t)
	dir := t.TempDir()
	inp := filepath.Join(dir, "in.ndjson")
	if err := os.WriteFile(inp, []byte("{\"a\": 1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var progress []string
	req := Request{
		Script:       `output_data = {"columns": columns, "rows": rows}`,
		Interpreter:  "python3",
		InputNDJSON:  inp,
		InputColumns: []string{"a"},
		OutputNDJSON: filepath.Join(dir, "out.ndjson"),
		Timeout:      30 * time.Second,
		ProgressHandler: func(percent int, message string) {
			progress = append(progress, fmt.Sprintf("%d:%s", percent, message))
		},
	}
	if _, err := p.Exec(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// File mode always announces its lazy attach at 5%; the marker was
	// emitted and DISCARDED for the legacy transport's whole life.
	joined := strings.Join(progress, "\n")
	if !strings.Contains(joined, "5:") {
		t.Fatalf("typed progress not delivered: %v", progress)
	}
}
