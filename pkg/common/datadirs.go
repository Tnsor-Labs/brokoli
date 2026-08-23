package common

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Where file nodes are allowed to read and write.
//
// There used to be two answers to this. The engine kept a configurable
// list (BROKOLI_DATA_DIRS, defaulting to /data, /tmp and the working
// directory) and checked a path against it before handing it to a loader;
// the loader then checked again against a hardcoded pair — the working
// directory and os.TempDir() — and refused anything else. So the two
// disagreed on their own defaults: /data was in the engine's list and
// unreachable in practice, and setting BROKOLI_DATA_DIRS to allow a new
// directory passed the first check and failed the second, with an error
// that named no directory and no setting.
//
// That mattered as soon as anyone mounted shared storage: /data is the
// obvious mount point and the one the engine already advertised, and it
// was exactly the one that could not work.
//
// One list now, defined here because this is the lower-level package and
// the loaders cannot import the engine.

// defaultDataDirs is where file nodes may read and write when
// BROKOLI_DATA_DIRS is unset. "." is the working directory, which is
// where a local `brokoli run` reads its files from.
var defaultDataDirs = []string{"/data", "/tmp", "."}

// DataDirs returns the directories file nodes may use, from
// BROKOLI_DATA_DIRS (colon-separated) or the default.
//
// Read per call rather than cached at init: tests set the variable, and
// the cost is a getenv against a file operation.
//
// An empty value falls back to the defaults rather than allowing nothing.
// Unset and empty are the same thing to most tooling, and a chart that
// renders `value: "{{ .Values.dataDirs }}"` with nothing configured
// produces an empty string — which under the other reading would disable
// every file node in the deployment with no error saying so. To restrict
// access, name the directories that are allowed.
func DataDirs() []string {
	if dirs := os.Getenv("BROKOLI_DATA_DIRS"); dirs != "" {
		var out []string
		for _, d := range strings.Split(dirs, ":") {
			if d = strings.TrimSpace(d); d != "" {
				out = append(out, d)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	// The system temp directory is included even when it is not /tmp, so
	// the default keeps working on platforms that put it elsewhere.
	if tmp := os.TempDir(); tmp != "" && tmp != "/tmp" {
		return append(append([]string{}, defaultDataDirs...), tmp)
	}
	return defaultDataDirs
}

// PathAllowed reports whether path is inside one of the data directories.
// The error names the directories and the setting that changes them,
// because "not permitted" on its own leaves an operator nothing to do —
// which is exactly what the loaders' refusal used to say.
func PathAllowed(path string) error {
	if strings.Contains(path, "..") {
		return fmt.Errorf("path traversal not allowed")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path %q", path)
	}
	dirs := DataDirs()
	for _, dir := range dirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if abs == absDir || strings.HasPrefix(abs, absDir+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("file path %q outside allowed directories (%s); set BROKOLI_DATA_DIRS to allow additional paths",
		path, strings.Join(dirs, ", "))
}
