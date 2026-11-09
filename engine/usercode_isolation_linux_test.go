//go:build linux

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/runas"
)

// These need root: only root can start a child as another user. CI runs
// them as root in the engine job's "User code isolation (as root)" step,
// which fails if they skip; elsewhere they skip.

func requireRootForIsolation(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("starting user code as another user needs root")
	}
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		if _, err := os.Stat("/usr/local/bin/python3"); err != nil {
			t.Skip("python3 is not installed")
		}
	}
}

// rootOnlyFile is a file only root may read, in a directory anyone may
// enter: what the server's own secrets look like to user code.
func rootOnlyFile(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "brokoli-root-only-")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("root's secret")
	_ = f.Close()
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	return f.Name()
}

const isolationProbe = `
import os
def can_read(p):
    try:
        open(p).read()
        return "yes"
    except Exception:
        return "no"
def can_signal(pid):
    if not pid:
        return ""
    try:
        os.kill(int(pid), 0)
        return "yes"
    except Exception:
        return "no"
def can_write(p):
    try:
        open(p, "a").close()
        return "yes"
    except Exception:
        return "no"
mine = os.path.join(os.environ.get("TMPDIR", "/tmp"), "mine.txt")
open(mine, "w").write("tenant data")
output_data = {"columns": ["uid", "pid", "secret", "server_binary", "peer_file", "peer_signal", "mine", "home"], "rows": [{
    "uid": os.getuid(), "pid": os.getpid(),
    "secret": can_read(config["secret"]),
    "server_binary": can_write(config["server_binary"]),
    "peer_file": can_read(config.get("peer_file", "")) if config.get("peer_file") else "",
    "peer_signal": can_signal(config.get("peer_pid", "")),
    "mine": mine, "home": os.environ.get("HOME", ""),
}]}
`

func probeUserCode(t *testing.T, tenant string, config map[string]interface{}) common.DataRow {
	t.Helper()
	ctx := runas.WithTenant(context.Background(), tenant)
	ds, stderr, err := ExecuteCodeNodeContext(ctx, isolationProbe, nil, config, nil, nil, 30)
	if err != nil {
		t.Fatalf("code node as %s: %v\nstderr: %s", tenant, err, stderr)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("probe returned %d rows", len(ds.Rows))
	}
	return ds.Rows[0]
}

// serverBinary stands in for the server's own executable: root-owned and
// writable by root only, as /app/brokoli is.
func serverBinary(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "brokoli-binary-")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	_ = os.Chmod(f.Name(), 0o755)
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	return f.Name()
}

func TestUserCodeRunsAsItsTenantsOwnUser(t *testing.T) {
	requireRootForIsolation(t)
	for _, pool := range []string{"1", "0"} {
		t.Run("pool="+pool, func(t *testing.T) {
			t.Setenv("BROKOLI_CODE_POOL", pool)
			secret, binary := rootOnlyFile(t), serverBinary(t)
			config := map[string]interface{}{"secret": secret, "server_binary": binary}

			// The control: without the setting the code is root, and the
			// probe can tell -- otherwise "no" below would prove nothing.
			t.Setenv(runas.EnvUIDs, "")
			asRoot := probeUserCode(t, "orgA/ws1", config)
			if fmt.Sprint(asRoot["uid"]) != "0" || asRoot["secret"] != "yes" || asRoot["server_binary"] != "yes" {
				t.Fatalf("control: as root the probe saw %v", asRoot)
			}

			t.Setenv(runas.EnvUIDs, "1000000000-1999999999")
			want, _ := runas.For("orgA/ws1")
			got := probeUserCode(t, "orgA/ws1", config)
			if fmt.Sprint(got["uid"]) != fmt.Sprint(want.UID()) {
				t.Fatalf("user code ran as uid %v, want its tenant's %d", got["uid"], want.UID())
			}
			if got["secret"] != "no" {
				t.Fatal("user code read a root-only file")
			}
			if got["server_binary"] != "no" {
				t.Fatal("user code could write the server's binary")
			}
			if home := fmt.Sprint(got["home"]); home == "/root" || home == "" {
				t.Fatalf("user code got HOME=%q, want its own folder", home)
			}
		})
	}
}

func TestTenantsCannotReachEachOthersCode(t *testing.T) {
	requireRootForIsolation(t)
	t.Setenv("BROKOLI_CODE_POOL", "1")
	t.Setenv(runas.EnvUIDs, "1000000000-1999999999")

	// Tenant A leaves a file in a folder of its own and a process in a
	// session of its own, so both outlive whichever worker ran it (a pooled
	// one, or a one-shot when the pool is full).
	leave := `
import os, subprocess, tempfile
d = tempfile.mkdtemp(dir="/tmp")
p = os.path.join(d, "mine.txt")
open(p, "w").write("tenant data")
os.chmod(p, 0o644)
proc = subprocess.Popen(["sleep", "60"], start_new_session=True)
output_data = {"columns": ["dir", "file", "pid"], "rows": [{"dir": d, "file": p, "pid": proc.pid}]}
`
	ds, stderr, err := ExecuteCodeNodeContext(runas.WithTenant(context.Background(), "orgA/ws1"), leave, nil, nil, nil, nil, 30)
	if err != nil || len(ds.Rows) != 1 {
		t.Fatalf("tenant A: %v %v\n%s", ds, err, stderr)
	}
	left := ds.Rows[0]
	t.Cleanup(func() {
		var pid int
		_, _ = fmt.Sscan(fmt.Sprint(left["pid"]), &pid)
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		_ = os.RemoveAll(fmt.Sprint(left["dir"]))
	})

	peer := map[string]interface{}{"secret": rootOnlyFile(t), "server_binary": serverBinary(t),
		"peer_file": left["file"], "peer_pid": fmt.Sprint(left["pid"])}
	// The control: tenant A reaches both, so "no" below means isolation.
	same := probeUserCode(t, "orgA/ws1", peer)
	if same["peer_file"] != "yes" || same["peer_signal"] != "yes" {
		t.Fatalf("control: tenant A could not reach its own file and process: %v", same)
	}
	b := probeUserCode(t, "orgB/ws1", peer)
	if b["peer_file"] != "no" {
		t.Fatal("tenant B read a file tenant A's code wrote")
	}
	if b["peer_signal"] != "no" {
		t.Fatal("tenant B could signal tenant A's process")
	}
}

// The server reads back what user code wrote with more privilege than the
// code has. A script that replaces its output with a link to a root-only
// file must get an error, not the file.
func TestPlantedOutputLinkIsNotFollowed(t *testing.T) {
	requireRootForIsolation(t)
	t.Setenv("BROKOLI_CODE_POOL", "0")
	secret := rootOnlyFile(t)
	if err := os.WriteFile(secret, []byte(`{"leak":"root's secret"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := make([]common.DataRow, fileModeThreshold)
	for i := range rows {
		rows[i] = common.DataRow{"id": i}
	}
	input := &common.DataSet{Columns: []string{"id"}, Rows: rows}
	// After the wrapper has written its output, swap it for a link.
	script := `
import atexit, os
out = os.environ["BROKED_OUTPUT_NDJSON"]
def plant():
    os.remove(out)
    os.symlink(config["secret"], out)
atexit.register(plant)
`
	leaked := func() (bool, error) {
		ctx := runas.WithTenant(context.Background(), "orgA/ws1")
		ds, _, err := ExecuteCodeNodeContext(ctx, script, input, map[string]interface{}{"secret": secret}, nil, nil, 30)
		if err != nil {
			return false, err
		}
		for _, row := range ds.Rows {
			if fmt.Sprint(row["leak"]) == "root's secret" {
				return true, nil
			}
		}
		return false, nil
	}

	// The control: the server as it was follows the link.
	t.Setenv(runas.EnvUIDs, "")
	if ok, err := leaked(); !ok {
		t.Fatalf("control: the planted link was not followed (err %v), so this test cannot see the attack", err)
	}
	t.Setenv(runas.EnvUIDs, "1000000000-1999999999")
	ok, err := leaked()
	if ok {
		t.Fatal("the server followed the planted link and returned the root-only file")
	}
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("want a refusal naming the output, got %v", err)
	}
}

func TestBashRunsAsItsTenantsOwnUser(t *testing.T) {
	requireRootForIsolation(t)
	requireBash(t)
	t.Setenv(runas.EnvUIDs, "1000000000-1999999999")
	secret := rootOnlyFile(t)
	want, _ := runas.For("orgA/ws1")
	r := newUnitTestRunner(t)
	ctx := runas.WithTenant(context.Background(), "orgA/ws1")
	node := models.Node{ID: "bash", Config: map[string]interface{}{
		"command": fmt.Sprintf(`echo "uid=$(id -u)"; if cat %q >/dev/null 2>&1; then echo secret=yes; else echo secret=no; fi; echo "home=$HOME"; touch "$HOME/x"`, secret),
	}}
	if _, err := r.runBash(ctx, node, nil); err != nil {
		t.Fatalf("runBash: %v\n%s", err, strings.Join(bashLogMessages(t, r), "\n"))
	}
	logs := strings.Join(bashLogMessages(t, r), "\n")
	if !strings.Contains(logs, fmt.Sprintf("uid=%d", want.UID())) {
		t.Fatalf("bash did not run as the tenant's uid %d:\n%s", want.UID(), logs)
	}
	if !strings.Contains(logs, "secret=no") {
		t.Fatalf("bash read a root-only file:\n%s", logs)
	}
	if strings.Contains(logs, "home=/root") {
		t.Fatalf("bash kept the server's HOME:\n%s", logs)
	}
}

func TestUserCodeFolderIsHandedOverAndRemoved(t *testing.T) {
	requireRootForIsolation(t)
	t.Setenv(runas.EnvUIDs, "1000000000-1999999999")
	ctx := runas.WithTenant(context.Background(), "orgA/ws1")
	d, err := newUserCodeDir(ctx, "probe-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.join("in"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.handOver(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(d.join("in"))
	if uid := info.Sys().(*syscall.Stat_t).Uid; uid != d.id.UID() {
		t.Fatalf("input owned by %d after handOver, want %d", uid, d.id.UID())
	}
	parent, _ := os.Stat(filepath.Dir(d.path))
	if parent.Mode().Perm() != 0o711 {
		t.Fatalf("the folders' parent is %v, want 0711", parent.Mode().Perm())
	}
	d.remove()
	if _, err := os.Stat(d.path); !os.IsNotExist(err) {
		t.Fatalf("folder still there after remove: %v", err)
	}
}
