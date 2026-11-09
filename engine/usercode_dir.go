package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
	"github.com/Tnsor-Labs/brokoli/pkg/runas"
)

// userCodeDir is one execution's private folder: what a code child reads
// (its script, its input) and writes (its output), and its HOME and
// TMPDIR. With BROKOLI_USER_CODE_UIDS set the child runs as its tenant's
// own user (pkg/runas) and the folder is handed to that user before the
// child starts; otherwise it is an ordinary temp folder and the child runs
// as the server, as before.
//
// The order matters: write everything in, handOver, start the child, then
// read results only through claim. Once the folder is the child's, a name
// in it may be a symlink the child planted.
type userCodeDir struct {
	id   runas.Identity
	path string
}

func newUserCodeDir(ctx context.Context, pattern string) (*userCodeDir, error) {
	id, err := runas.ForContext(ctx)
	if err != nil {
		return nil, err
	}
	dir, err := id.MkdirTemp(pattern)
	if err != nil {
		return nil, fmt.Errorf("user code folder: %w", err)
	}
	return &userCodeDir{id: id, path: dir}, nil
}

func (d *userCodeDir) join(name string) string { return filepath.Join(d.path, name) }

func (d *userCodeDir) remove() { _ = os.RemoveAll(d.path) }

// wrapper is the code wrapper the child runs: the server's shared copy,
// or, when the child is another user, a copy inside this folder.
func (d *userCodeDir) wrapper() (string, error) {
	if !d.id.Enabled() {
		return codeexec.WrapperPath()
	}
	return codeexec.WrapperPathIn(d.path)
}

// moveIn moves src, a temp file the caller staged for this execution and
// no longer needs, into this folder as name, for a child that cannot reach
// it where it is. Not enabled, it is src itself. Moved rather than linked:
// a link would leave the same file, about to be handed to the child, under
// its old name in the shared temp directory, where its new owner could
// swap that name for a symlink the caller later follows.
func (d *userCodeDir) moveIn(src, name string) (string, error) {
	if !d.id.Enabled() {
		return src, nil
	}
	dst := d.join(name)
	if err := os.Rename(src, dst); err == nil {
		return dst, nil
	}
	in, err := os.Open(src) // #nosec G304 -- src is a file the engine staged for this execution
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- dst is a fixed name in this execution's own folder, created exclusively before the child has it
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return "", err
	}
	return dst, out.Close()
}

// handOver gives the folder to the child's user. Call it once everything
// the child needs has been written in.
func (d *userCodeDir) handOver() error { return d.id.Own(d.path) }

// prepare sets cmd to run as the child's user with HOME and TMPDIR here.
func (d *userCodeDir) prepare(cmd *exec.Cmd) {
	cmd.Env = d.id.Env(cmd.Env, d.path)
	d.id.Apply(cmd)
}

// claim is the path to read the child's output file name from (see
// runas.Identity.Claim). The caller removes it.
func (d *userCodeDir) claim(name string) (string, error) { return d.id.Claim(d.path, name) }

// workOrderTenant marks ctx with the work order's tenant, as
// Runner.Execute does for a run, so its user code runs as that tenant's
// user on a remote worker too, and in that tenant's warm code workers.
func workOrderTenant(ctx context.Context, wo *extensions.InstanceWorkOrder) context.Context {
	if wo.OrgID == "" && wo.WorkspaceID == "" {
		return ctx
	}
	return runas.WithTenant(ctx, wo.OrgID+"/"+wo.WorkspaceID)
}

// copyRegularTree copies the folders and regular files under src into a
// new dst; anything else (a symlink) is skipped. src is the server's own.
func copyRegularTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case e.IsDir():
			return os.Mkdir(target, 0o700)
		case e.Type().IsRegular():
			raw, err := os.ReadFile(p) // #nosec G304 G122 -- p is under the server's own harness cache, which no user code can write
			if err != nil {
				return err
			}
			return os.WriteFile(target, raw, 0o600) // #nosec G703 -- target is under a folder this process just made; the child gets it only afterwards
		}
		return nil
	})
}
