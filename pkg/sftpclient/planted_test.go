package sftpclient

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/sftpclient/sftptest"
)

// On a server shared with other accounts, somebody who could predict the
// temporary name could put a symlink there and have the upload write
// through it. The name is random, and even when it is known, as here, the
// exclusive open refuses rather than follows.
func TestAnUploadRefusesAPlantedTemporary(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	c, err := Dial(context.Background(), Config{
		Host: srv.Host, Port: srv.Port, User: srv.User, Password: srv.Password,
		HostKey: srv.Fingerprint(), BaseDir: srv.Root,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.nonce = func() string { return "known" }

	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(srv.Root, ".orders.csv.run-1-known.part")); err != nil {
		t.Fatal(err)
	}
	_, err = c.Upload("orders.csv", "run-1", func(w io.Writer) error {
		_, werr := io.WriteString(w, "written through the link")
		return werr
	})
	if err == nil {
		t.Fatal("the upload wrote through a planted temporary")
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep me" {
		t.Fatalf("the planted link's target was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(srv.Root, "orders.csv")); !os.IsNotExist(err) {
		t.Fatalf("a destination appeared: %v", err)
	}
}
