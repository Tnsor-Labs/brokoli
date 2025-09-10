package common

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func SafeReadConfig(configFile string) ([]byte, error) {
	baseDir := "./"
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}

	absConfigPath, err := filepath.Abs(filepath.Join(baseDir, configFile))
	if err != nil {
		return nil, err
	}

	if !strings.HasPrefix(absConfigPath, absBaseDir) {
		return nil, errors.New("access to file outside of the allowed directory is not permitted")
	}

	// Use direct file operation since we've already validated the path
	// #nosec G304
	return os.ReadFile(absConfigPath)
}
