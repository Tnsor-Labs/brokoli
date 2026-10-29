package common

import (
	"io"
	"os"
)

// File operations confined to the data directories (DataDirs). Each goes
// through OpenInDataDir, so a path that leads out of them, by ".." or by
// a symlink, is refused when the file is opened.

// SafeReadFile is os.ReadFile confined to the data directories.
func SafeReadFile(filePath string) ([]byte, error) {
	f, err := SafeOpenFile(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	return io.ReadAll(f)
}

// SafeOpenFile is os.Open confined to the data directories.
func SafeOpenFile(filePath string) (*os.File, error) {
	return OpenInDataDir(filePath, os.O_RDONLY, 0)
}

// SafeCreateFile is os.Create confined to the data directories.
func SafeCreateFile(filePath string) (*os.File, error) {
	return OpenInDataDir(filePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

// SafeWriteFile is os.WriteFile confined to the data directories.
func SafeWriteFile(filePath string, data []byte, perm os.FileMode) error {
	f, err := OpenInDataDir(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close() // the write error is the one to report
		return err
	}
	return f.Close()
}
