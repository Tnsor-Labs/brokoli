package common

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// safeFileOperation is a generic helper function for secure file operations
func safeFileOperation[T any](filePath string, operation func(string) (T, error)) (T, error) {
	var zero T

	// Get the current working directory as the base directory
	baseDir, err := os.Getwd()
	if err != nil {
		return zero, err
	}

	// Convert the base directory to an absolute path
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return zero, err
	}

	// Convert the file path to an absolute path
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return zero, err
	}

	// Get the system temp directory
	tempDir := os.TempDir()
	absTempDir, err := filepath.Abs(tempDir)
	if err != nil {
		return zero, err
	}

	// Check if the file path is within the base directory or temp directory
	if !strings.HasPrefix(absFilePath, absBaseDir) && !strings.HasPrefix(absFilePath, absTempDir) {
		return zero, errors.New("access to file outside of the allowed directory is not permitted")
	}

	// Perform the file operation
	return operation(absFilePath)
}
