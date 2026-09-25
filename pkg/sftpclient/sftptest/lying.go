package sftptest

import (
	"io"
	"os"

	"github.com/pkg/sftp"
)

// lyingHandlers serve real files read-only while reporting every file as
// empty: a server whose declared sizes cannot be trusted.
type lyingHandlers struct{}

func (lyingHandlers) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	// #nosec G304 -- test server, serving the test's own temp directory.
	return os.Open(r.Filepath)
}

func (lyingHandlers) Filewrite(*sftp.Request) (io.WriterAt, error) { return nil, os.ErrPermission }

func (lyingHandlers) Filecmd(*sftp.Request) error { return os.ErrPermission }

func (lyingHandlers) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	fi, err := os.Stat(r.Filepath)
	if err != nil {
		return nil, err
	}
	return listerAt{zeroSize{fi}}, nil
}

type zeroSize struct{ os.FileInfo }

func (zeroSize) Size() int64 { return 0 }

type listerAt []os.FileInfo

func (l listerAt) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if n < len(out) {
		return n, io.EOF
	}
	return n, nil
}
