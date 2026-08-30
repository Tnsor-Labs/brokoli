//go:build unix

package codeexec

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
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
	raw, err := os.ReadFile(second.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"a": 4`) && !strings.Contains(string(raw), `"a":4`) {
		t.Fatalf("output not transformed: %s", raw)
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
	rows := []map[string]interface{}{{"a": float64(1)}, {"a": float64(2)}}
	_, err := p.Exec(context.Background(), inlineReq(script, rows, filepath.Join(dir, "m.ndjson")))
	var scriptErr *ErrScript
	if !errors.As(err, &scriptErr) || scriptErr.Kind != ErrKindMutationAfterPass {
		t.Fatalf("want mutation_after_pass, got %v", err)
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
