package common

import (
	"os"
	"path/filepath"
)

// SafeFileAccess provides secure file operations to prevent path traversal attacks
// by validating that the file path is within the allowed directory

// SafeReadFile reads a file securely, ensuring the path doesn't escape the base directory
func SafeReadFile(filePath string) ([]byte, error) {
	return safeFileOperation(filePath, func(path string) ([]byte, error) {
		// #nosec G304
		return os.ReadFile(path)
	})
}

// SafeOpenFile opens a file securely, ensuring the path doesn't escape the base directory
func SafeOpenFile(filePath string) (*os.File, error) {
	file, err := safeFileOperation(filePath, func(path string) (*os.File, error) {
		// #nosec G304
		return os.Open(path)
	})
	return file, err
}

// SafeCreateFile creates a file securely, ensuring the path doesn't escape the base directory
func SafeCreateFile(filePath string) (*os.File, error) {
	file, err := safeFileOperation(filePath, func(path string) (*os.File, error) {
		// #nosec G304
		return os.Create(path)
	})
	return file, err
}

// SafeWriteFile writes to a file securely, ensuring the path doesn't escape the base directory
func SafeWriteFile(filePath string, data []byte, perm os.FileMode) error {
	_, err := safeFileOperation(filePath, func(path string) (interface{}, error) {
		return nil, os.WriteFile(path, data, perm)
	})
	return err
}

// safeFileOperation is a generic helper function for secure file operations.
// The path must be inside one of the configured data directories — see
// DataDirs, which is the single answer to that question for both this
// package and the engine.
func safeFileOperation[T any](filePath string, operation func(string) (T, error)) (T, error) {
	var zero T

	if err := PathAllowed(filePath); err != nil {
		return zero, err
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return zero, err
	}
	return operation(abs)
}
