package engine

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/pkg/sftpclient/sftptest"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ADR-040 end to end: real runs through the engine, a stored sftp
// connection resolved the way serve.go resolves it, and a real SSH server
// with the SFTP subsystem on loopback.

const ordersCSV = "id,total\n1,10\n2,25\n3,7\n"

var ordersJSON = `[{"id":1,"total":10},{"id":2,"total":25},{"id":3,"total":7}]`

// allowLoopback is the operator's opt-in to reach private and loopback
// targets, which the test server needs.
func allowLoopback(t *testing.T) {
	t.Helper()
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
}

type sftpFixture struct {
	srv *sftptest.Server
	st  *store.SQLiteStore
	eng *Engine
	dir string
}

// newSFTPFixture stores the connection "partner" for srv. extra nil means
// the correct host key.
func newSFTPFixture(t *testing.T, streamed bool, extra map[string]interface{}) *sftpFixture {
	t.Helper()
	srv := sftptest.Start(t, sftptest.Options{})
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if extra == nil {
		extra = map[string]interface{}{"host_key": srv.Fingerprint()}
	}
	b, _ := json.Marshal(extra)
	if err := st.CreateConnection(&models.Connection{
		ID: "c-partner", ConnID: "partner", Type: models.ConnTypeSFTP,
		Host: srv.Host, Port: srv.Port, Schema: srv.Root,
		Login: srv.User, Password: srv.Password, Extra: string(b),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, nil)
	if streamed {
		eng.SpillThresholdBytes = 1
		eng.StreamThresholdBytes = 1
	} else {
		// Streaming off, so the batch path is the one that runs. Zero
		// would mean "the default"; only a negative threshold means off.
		eng.StreamThresholdBytes = -1
	}
	return &sftpFixture{srv: srv, st: st, eng: eng, dir: dir}
}

func (f *sftpFixture) localFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(f.dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *sftpFixture) remoteFile(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(f.srv.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run executes p and returns the run and everything it logged.
func (f *sftpFixture) run(t *testing.T, p *models.Pipeline) (*models.Run, string) {
	t.Helper()
	if err := f.st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := f.eng.RunPipeline(p.ID)
	if run == nil {
		t.Fatalf("no run: %v", err)
	}
	logs, lerr := f.st.GetLogs(run.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	var b strings.Builder
	for _, l := range logs {
		b.WriteString(l.Message)
		b.WriteByte('\n')
	}
	b.WriteString("run error: " + run.Error + "\n")
	if err != nil {
		b.WriteString("RunPipeline: " + err.Error() + "\n")
	}
	return run, b.String()
}

func chain(id string, nodes ...models.Node) *models.Pipeline {
	p := &models.Pipeline{ID: id, Name: id, Enabled: true, Nodes: nodes}
	for i := 1; i < len(nodes); i++ {
		p.Edges = append(p.Edges, models.Edge{From: nodes[i-1].ID, To: nodes[i].ID})
	}
	return p
}

func fileNode(id string, typ models.NodeType, cfg map[string]interface{}) models.Node {
	return models.Node{ID: id, Type: typ, Name: id, Config: cfg}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSinkFileDeliversOverSFTP(t *testing.T) {
	allowLoopback(t)
	for _, tc := range []struct {
		name     string
		streamed bool
		input    string
		content  string
		// The line only that path logs: proof of which one ran.
		pathLog string
	}{
		{"batch", false, "in.csv", ordersCSV, "Wrote csv to orders.csv"},
		{"streamed", true, "in.csv", ordersCSV, "Streamed csv to orders.csv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSFTPFixture(t, tc.streamed, nil)
			// Only this test's own directory is a data directory, so a
			// remote path mishandled as a local one is refused, not quietly
			// written next to the test binary (the default includes ".").
			t.Setenv("BROKOLI_DATA_DIRS", f.dir)
			in := f.localFile(t, tc.input, tc.content)
			run, logs := f.run(t, chain("deliver-"+tc.name,
				fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": in}),
				fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{
					"path": "outbound/2026/orders.csv", "conn_id": "partner",
				}),
			))
			if run.Status != models.RunStatusSuccess {
				t.Fatalf("status %s\n%s", run.Status, logs)
			}
			if !strings.Contains(logs, tc.pathLog) {
				t.Fatalf("the %s sink did not run (no %q):\n%s", tc.name, tc.pathLog, logs)
			}
			got, err := os.ReadFile(filepath.Join(f.srv.Root, "outbound", "2026", "orders.csv"))
			if err != nil || string(got) != ordersCSV {
				t.Fatalf("delivered %q, %v\n%s", got, err, logs)
			}
			if files := dirEntries(t, f.srv.Root); len(files) != 1 {
				t.Fatalf("the server holds %v, want only the delivered file", files)
			}
			want := "Full path: partner:" + filepath.Join(f.srv.Root, "outbound", "2026", "orders.csv")
			if !strings.Contains(logs, want) {
				t.Fatalf("the log does not say where the file went (%q):\n%s", want, logs)
			}
		})
	}
}

func TestSourceFileFetchesOverSFTP(t *testing.T) {
	allowLoopback(t)
	for _, tc := range []struct {
		name     string
		streamed bool
		remote   string
		content  string
		pathLog  string
	}{
		{"batch csv", false, "inbound/rates.csv", ordersCSV, "Loaded 3 rows, 2 columns from rates.csv"},
		{"streamed csv", true, "inbound/rates.csv", ordersCSV, "Streamed 3 rows, 2 columns from rates.csv"},
		// JSON is written back as JSON and compared as rows below.
		{"batch json", false, "inbound/rates.json", ordersJSON, "Loaded 3 rows, 2 columns from rates.json"},
		// ".." inside a name is an ordinary file, not a traversal.
		{"dots in the name", false, "inbound/q3..final.csv", ordersCSV, "Loaded 3 rows, 2 columns from q3..final.csv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSFTPFixture(t, tc.streamed, nil)
			f.remoteFile(t, tc.remote, tc.content)
			before := downloadLeftovers()
			out := filepath.Join(f.dir, "out"+filepath.Ext(tc.remote))
			run, logs := f.run(t, chain("fetch-"+strings.ReplaceAll(tc.name, " ", "-"),
				fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{
					"path": tc.remote, "conn_id": "partner",
				}),
				fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{"path": out}),
			))
			if run.Status != models.RunStatusSuccess {
				t.Fatalf("status %s\n%s", run.Status, logs)
			}
			for _, want := range []string{tc.pathLog, "Fetched " + filepath.Join(f.srv.Root, filepath.FromSlash(tc.remote)) + " from partner"} {
				if !strings.Contains(logs, want) {
					t.Fatalf("no %q in:\n%s", want, logs)
				}
			}
			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Ext(out) == ".json" {
				var gotRows, wantRows []map[string]interface{}
				if err := json.Unmarshal(got, &gotRows); err != nil {
					t.Fatalf("read back %q: %v", got, err)
				}
				_ = json.Unmarshal([]byte(ordersJSON), &wantRows)
				if !reflect.DeepEqual(gotRows, wantRows) {
					t.Fatalf("read back %v, want %v", gotRows, wantRows)
				}
			} else if string(got) != ordersCSV {
				t.Fatalf("read back %q", got)
			}
			if after := downloadLeftovers(); len(after) != len(before) {
				t.Fatalf("the downloaded copy was left behind: before %v, after %v", before, after)
			}
		})
	}
}

// downloadLeftovers lists downloaded copies still on disk, in every place
// one could be kept.
func downloadLeftovers() []string {
	var out []string
	for _, d := range common.DataDirs() {
		m, _ := filepath.Glob(filepath.Join(d, ".brokoli-sftp-*"))
		out = append(out, m...)
	}
	return out
}

func TestAMissingRemoteFileIsNamed(t *testing.T) {
	allowLoopback(t)
	f := newSFTPFixture(t, false, nil)
	run, logs := f.run(t, chain("missing",
		fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": "inbound/nope.csv", "conn_id": "partner"}),
	))
	if run.Status != models.RunStatusFailed || !strings.Contains(logs, "sftp://partner/inbound/nope.csv") {
		t.Fatalf("status %s, want failed naming sftp://partner/inbound/nope.csv:\n%s", run.Status, logs)
	}
}

// A delivery goes nowhere unless the server proves it is the one
// configured; a refused delivery leaves nothing on the server.
func TestDeliveryRefusesAnUnverifiedServer(t *testing.T) {
	allowLoopback(t)
	other := sftptest.Start(t, sftptest.Options{})
	for _, tc := range []struct {
		name  string
		extra map[string]interface{}
		want  func(f *sftpFixture) string
	}{
		{"mismatched", map[string]interface{}{"host_key": other.Fingerprint()},
			func(*sftpFixture) string { return "does not match the configured one" }},
		{"missing", map[string]interface{}{},
			func(f *sftpFixture) string { return f.srv.Fingerprint() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSFTPFixture(t, false, tc.extra)
			in := f.localFile(t, "in.csv", ordersCSV)
			run, logs := f.run(t, chain("hostkey-"+tc.name,
				fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": in}),
				fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{"path": "orders.csv", "conn_id": "partner"}),
			))
			if run.Status != models.RunStatusFailed {
				t.Fatalf("status %s, want failed:\n%s", run.Status, logs)
			}
			if want := tc.want(f); !strings.Contains(logs, want) {
				t.Fatalf("no %q in:\n%s", want, logs)
			}
			if files := dirEntries(t, f.srv.Root); len(files) != 0 {
				t.Fatalf("an unverified server received %v", files)
			}
		})
	}
}

// The editor's preview runs the pipeline; it must never hand a partner a
// truncated file, and it does not even connect.
func TestADryRunDeliversNothing(t *testing.T) {
	allowLoopback(t)
	f := newSFTPFixture(t, false, nil)
	in := f.localFile(t, "in.csv", ordersCSV)
	p := chain("dry",
		fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": in}),
		fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{"path": "orders.csv", "conn_id": "partner"}),
	)
	if err := f.st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	results, err := f.eng.DryRun(p, 10)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	for id, r := range results {
		if r.Error != "" {
			t.Fatalf("dry-run node %s failed: %s", id, r.Error)
		}
	}
	if n := f.srv.Accepted(); n != 0 {
		t.Fatalf("a dry run connected to the partner's server %d times", n)
	}
	if files := dirEntries(t, f.srv.Root); len(files) != 0 {
		t.Fatalf("a dry run delivered %v", files)
	}
}

// SSH goes through the same outbound policy as HTTP: without the
// operator's opt-in a loopback server is refused before any connection.
func TestFileNodesDialThroughTheNetworkPolicy(t *testing.T) {
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{}))
	f := newSFTPFixture(t, false, nil)
	in := f.localFile(t, "in.csv", ordersCSV)
	run, logs := f.run(t, chain("policy",
		fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": in}),
		fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{"path": "orders.csv", "conn_id": "partner"}),
	))
	if run.Status != models.RunStatusFailed || !strings.Contains(logs, netguard.ErrBlockedTarget.Error()) {
		t.Fatalf("status %s, want failed by the network policy:\n%s", run.Status, logs)
	}
	if n := f.srv.Accepted(); n != 0 {
		t.Fatalf("the server was reached %d times despite the policy", n)
	}
}

func TestAFileNodeRefusesANonSFTPConnection(t *testing.T) {
	allowLoopback(t)
	f := newSFTPFixture(t, false, nil)
	if err := f.st.CreateConnection(&models.Connection{
		ID: "c-wh", ConnID: "wh", Type: models.ConnTypePostgres, Host: "db.internal",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	in := f.localFile(t, "in.csv", ordersCSV)
	run, logs := f.run(t, chain("wrongtype",
		fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": in}),
		fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{"path": "orders.csv", "conn_id": "wh"}),
	))
	if run.Status != models.RunStatusFailed || !strings.Contains(logs, `conn_id "wh" is a postgres connection`) {
		t.Fatalf("status %s:\n%s", run.Status, logs)
	}
}

// A remote file does not live on any worker's disk, so the per-worker
// storage hazard says nothing about it, at deploy time or at run time.
func TestRemoteFilesAreNotWorkerDiskFiles(t *testing.T) {
	withDeployment(t, true, false)
	if !unsharedFileStorage() {
		t.Fatal("setup: the hazard is not in effect")
	}

	var local, remote NodeValidationResult
	validateFileStorage(fileNode("a", models.NodeTypeSourceFile, map[string]interface{}{"path": "/data/in.csv"}), map[string]bool{}, &local)
	validateFileStorage(fileNode("b", models.NodeTypeSourceFile, map[string]interface{}{"path": "/data/in.csv", "conn_id": "partner"}), map[string]bool{}, &remote)
	if len(local.Warnings) == 0 {
		t.Fatal("a local source lost its worker-disk warning")
	}
	if len(remote.Warnings) != 0 {
		t.Fatalf("a remote source was warned about worker disks: %v", remote.Warnings)
	}
	// A conn_id of spaces is no connection, at run time and here alike.
	var blank NodeValidationResult
	validateFileStorage(fileNode("c", models.NodeTypeSourceFile, map[string]interface{}{"path": "/data/in.csv", "conn_id": "   "}), map[string]bool{}, &blank)
	if len(blank.Warnings) == 0 {
		t.Fatal("a whitespace conn_id hid the worker-disk warning for a local file")
	}

	allowLoopback(t)
	// Both paths, because each has its own copy of the warning.
	for _, tc := range []struct {
		name     string
		streamed bool
		pathLogs []string
	}{
		{"batch", false, []string{"Loaded 3 rows", "Wrote csv to orders.csv"}},
		{"streamed", true, []string{"Streamed 3 rows", "Streamed csv to orders.csv"}},
	} {
		f := newSFTPFixture(t, tc.streamed, nil)
		f.remoteFile(t, "inbound/rates.csv", ordersCSV)
		run, logs := f.run(t, chain("nodisk-"+tc.name,
			fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": "inbound/rates.csv", "conn_id": "partner"}),
			fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{"path": "outbound/orders.csv", "conn_id": "partner"}),
		))
		if run.Status != models.RunStatusSuccess {
			t.Fatalf("%s: status %s\n%s", tc.name, run.Status, logs)
		}
		for _, want := range tc.pathLogs {
			if !strings.Contains(logs, want) {
				t.Fatalf("%s: setup: the %s path did not run (no %q):\n%s", tc.name, tc.name, want, logs)
			}
		}
		if strings.Contains(logs, "own filesystem") {
			t.Fatalf("%s: a remote file was reported as on this worker's disk:\n%s", tc.name, logs)
		}
	}
}

func TestARemoteFileIsItsOwnLineageAsset(t *testing.T) {
	nodes := map[string]LineageNode{}
	local := extractFileAsset(map[string]interface{}{"path": "outbound/orders.csv"}, nodes)
	remote := extractFileAsset(map[string]interface{}{"path": "outbound/orders.csv", "conn_id": "partner"}, nodes)
	if local != "file:outbound/orders.csv" {
		t.Errorf("local asset %q", local)
	}
	if remote != "sftp://partner/outbound/orders.csv" {
		t.Errorf("remote asset %q", remote)
	}
	if n := nodes[remote]; n.Name != "orders.csv" || n.Type != "file" {
		t.Errorf("remote asset node %+v", n)
	}
}

// Where replacing a file cannot be atomic, the run log says so: there was
// a moment with no file, and whoever polls the directory may have seen it.
func TestANonAtomicReplacementIsLogged(t *testing.T) {
	if err := sftp.SetSFTPExtensions("statvfs@openssh.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sftp.SetSFTPExtensions("hardlink@openssh.com", "posix-rename@openssh.com", "statvfs@openssh.com")
	})
	allowLoopback(t)
	f := newSFTPFixture(t, false, nil)
	f.remoteFile(t, "orders.csv", "yesterday")
	in := f.localFile(t, "in.csv", ordersCSV)
	run, logs := f.run(t, chain("nonatomic",
		fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": in}),
		fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{"path": "orders.csv", "conn_id": "partner"}),
	))
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("status %s\n%s", run.Status, logs)
	}
	if !strings.Contains(logs, "does not support posix-rename") {
		t.Fatalf("the non-atomic replacement was not logged:\n%s", logs)
	}
	got, _ := os.ReadFile(filepath.Join(f.srv.Root, "orders.csv"))
	if string(got) != ordersCSV {
		t.Fatalf("got %q", got)
	}
}

// An operator who narrows the data directories must still be able to read
// remote files: the downloaded copy is the engine's own scratch, not a
// path a pipeline chose, and it must load wherever it is kept.
func TestARemoteSourceLoadsWithNarrowedDataDirectories(t *testing.T) {
	allowLoopback(t)
	for _, tc := range []struct {
		name, remote, content string
		streamed              bool
	}{
		{"csv batch", "inbound/rates.csv", ordersCSV, false},
		{"csv streamed", "inbound/rates.csv", ordersCSV, true},
		{"json", "inbound/rates.json", ordersJSON, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSFTPFixture(t, tc.streamed, nil)
			f.remoteFile(t, tc.remote, tc.content)
			// Only this test's own directory: the system temp directory,
			// where a naive download would land, is not allowed.
			t.Setenv("BROKOLI_DATA_DIRS", f.dir)
			out := filepath.Join(f.dir, "out.json")
			run, logs := f.run(t, chain("narrow-"+strings.ReplaceAll(tc.name, " ", "-"),
				fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": tc.remote, "conn_id": "partner"}),
				fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{"path": out}),
			))
			if run.Status != models.RunStatusSuccess {
				t.Fatalf("status %s:\n%s", run.Status, logs)
			}
			var rows []map[string]interface{}
			got, _ := os.ReadFile(out)
			if err := json.Unmarshal(got, &rows); err != nil || len(rows) != 3 {
				t.Fatalf("read back %q: %v", got, err)
			}
			if left := downloadLeftovers(); len(left) != 0 {
				t.Fatalf("the downloaded copy was left behind: %v", left)
			}
		})
	}
}

// A node's timeout or a cancelled run reaches the transfer itself: the
// connection is closed and the node returns, rather than staying blocked
// on a server that stopped responding until the much longer idle timeout.
func TestCancellingTheNodeContextEndsATransfer(t *testing.T) {
	allowLoopback(t)
	f := newSFTPFixture(t, false, nil)
	proxy := sftptest.StartProxy(t, net.JoinHostPort(f.srv.Host, strconv.Itoa(f.srv.Port)))
	_, portStr, _ := net.SplitHostPort(proxy.Addr)
	port, _ := strconv.Atoi(portStr)
	if err := f.st.CreateConnection(&models.Connection{
		ID: "c-slow", ConnID: "slow", Type: models.ConnTypeSFTP,
		Host: "127.0.0.1", Port: port, Schema: f.srv.Root,
		Login: f.srv.User, Password: f.srv.Password,
		Extra:     `{"host_key": "` + f.srv.Fingerprint() + `"}`,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{connResolver: NewConnectionResolver(f.st, nil)}
	node := fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{"path": "orders.csv", "conn_id": "slow"})

	t.Run("delivery", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := r.writeFileOutput(ctx, node, "orders.csv", func(w io.Writer) error {
				proxy.Freeze()
				time.AfterFunc(300*time.Millisecond, cancel)
				_, err := io.Copy(w, io.LimitReader(zeroBytes{}, 16<<20))
				return err
			})
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("a delivery whose node context was cancelled reported success")
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the delivery was still blocked 15s after its node context was cancelled")
		}
	})
}

type zeroBytes struct{}

func (zeroBytes) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func TestSFTPConfigReadsTheDownloadLimit(t *testing.T) {
	for extra, want := range map[string]int64{
		`{"max_download_bytes": 1048576}`: 1048576,
		`{"max_download_bytes": "2048"}`:  2048,
		`{}`:                              0,
	} {
		cfg, err := SFTPConfig(&models.Connection{Type: models.ConnTypeSFTP, Extra: extra})
		if err != nil || cfg.MaxDownloadBytes != want {
			t.Errorf("%s: %d, %v; want %d", extra, cfg.MaxDownloadBytes, err, want)
		}
	}
	for _, extra := range []string{`{"max_download_bytes": -5}`, `{"max_download_bytes": 1.5}`, `{"max_download_bytes": "lots"}`, `{"max_download_bytes": true}`} {
		if _, err := SFTPConfig(&models.Connection{Type: models.ConnTypeSFTP, Extra: extra}); err == nil {
			t.Errorf("%s was accepted", extra)
		}
	}
}

// A fetch over the connection's limit fails the node and leaves no copy.
func TestARemoteFileOverTheLimitIsRefused(t *testing.T) {
	allowLoopback(t)
	f := newSFTPFixture(t, false, nil)
	_ = f.st.DeleteConnection("partner")
	if err := f.st.CreateConnection(&models.Connection{
		ID: "c-small", ConnID: "partner", Type: models.ConnTypeSFTP,
		Host: f.srv.Host, Port: f.srv.Port, Schema: f.srv.Root,
		Login: f.srv.User, Password: f.srv.Password,
		Extra:     `{"host_key": "` + f.srv.Fingerprint() + `", "max_download_bytes": 10}`,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	f.remoteFile(t, "inbound/rates.csv", ordersCSV)
	before := downloadLeftovers()
	run, logs := f.run(t, chain("toolarge",
		fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": "inbound/rates.csv", "conn_id": "partner"}),
	))
	if run.Status != models.RunStatusFailed || !strings.Contains(logs, "larger than the download limit") {
		t.Fatalf("status %s:\n%s", run.Status, logs)
	}
	if after := downloadLeftovers(); len(after) != len(before) {
		t.Fatalf("a refused download left %v", after)
	}
}
