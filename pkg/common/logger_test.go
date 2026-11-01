package common

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	logger := NewLogger(LogLevelInfo)

	if logger == nil {
		t.Error("NewLogger() returned nil")
	}

	if logger != nil && logger.level != LogLevelInfo {
		t.Errorf("NewLogger() level = %v, want %v", logger.level, LogLevelInfo)
	}
}

func TestNewLoggerWithWriter(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLoggerWithWriter(&buf, LogLevelDebug)

	if logger == nil {
		t.Error("NewLoggerWithWriter() returned nil")
	}

	if logger != nil && logger.level != LogLevelDebug {
		t.Errorf("NewLoggerWithWriter() level = %v, want %v", logger.level, LogLevelDebug)
	}
}

func TestLogger_SetLevel(t *testing.T) {
	logger := NewLogger(LogLevelInfo)
	logger.SetLevel(LogLevelError)

	if logger.level != LogLevelError {
		t.Errorf("SetLevel() level = %v, want %v", logger.level, LogLevelError)
	}
}

func TestLogger_Debug(t *testing.T) {
	tests := []struct {
		name      string
		level     LogLevel
		shouldLog bool
	}{
		{
			name:      "Debug level",
			level:     LogLevelDebug,
			shouldLog: true,
		},
		{
			name:      "Info level",
			level:     LogLevelInfo,
			shouldLog: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := NewLoggerWithWriter(&buf, tt.level)

			logger.Debug("test message")

			if tt.shouldLog && !strings.Contains(buf.String(), "test message") {
				t.Errorf("Debug() should log message at level %v", tt.level)
			}

			if !tt.shouldLog && buf.String() != "" {
				t.Errorf("Debug() should not log message at level %v", tt.level)
			}
		})
	}
}

func TestLogger_Info(t *testing.T) {
	tests := []struct {
		name      string
		level     LogLevel
		shouldLog bool
	}{
		{
			name:      "Debug level",
			level:     LogLevelDebug,
			shouldLog: true,
		},
		{
			name:      "Info level",
			level:     LogLevelInfo,
			shouldLog: true,
		},
		{
			name:      "Warning level",
			level:     LogLevelWarning,
			shouldLog: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := NewLoggerWithWriter(&buf, tt.level)

			logger.Info("test message")

			if tt.shouldLog && !strings.Contains(buf.String(), "test message") {
				t.Errorf("Info() should log message at level %v", tt.level)
			}

			if !tt.shouldLog && buf.String() != "" {
				t.Errorf("Info() should not log message at level %v", tt.level)
			}
		})
	}
}

func TestLogger_Warning(t *testing.T) {
	tests := []struct {
		name      string
		level     LogLevel
		shouldLog bool
	}{
		{
			name:      "Info level",
			level:     LogLevelInfo,
			shouldLog: true,
		},
		{
			name:      "Warning level",
			level:     LogLevelWarning,
			shouldLog: true,
		},
		{
			name:      "Error level",
			level:     LogLevelError,
			shouldLog: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := NewLoggerWithWriter(&buf, tt.level)

			logger.Warning("test message")

			if tt.shouldLog && !strings.Contains(buf.String(), "test message") {
				t.Errorf("Warning() should log message at level %v", tt.level)
			}

			if !tt.shouldLog && buf.String() != "" {
				t.Errorf("Warning() should not log message at level %v", tt.level)
			}
		})
	}
}

func TestLogger_Error(t *testing.T) {
	tests := []struct {
		name      string
		level     LogLevel
		shouldLog bool
	}{
		{
			name:      "Warning level",
			level:     LogLevelWarning,
			shouldLog: true,
		},
		{
			name:      "Error level",
			level:     LogLevelError,
			shouldLog: true,
		},
		{
			name:      "Fatal level",
			level:     LogLevelFatal,
			shouldLog: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := NewLoggerWithWriter(&buf, tt.level)

			logger.Error("test message")

			if tt.shouldLog && !strings.Contains(buf.String(), "test message") {
				t.Errorf("Error() should log message at level %v", tt.level)
			}

			if !tt.shouldLog && buf.String() != "" {
				t.Errorf("Error() should not log message at level %v", tt.level)
			}
		})
	}
}

// Note: We can't easily test Fatal() because it calls os.Exit(1)

func TestLogLevelFromString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  LogLevel
	}{
		{
			name:  "DEBUG",
			input: "DEBUG",
			want:  LogLevelDebug,
		},
		{
			name:  "debug (lowercase)",
			input: "debug",
			want:  LogLevelDebug,
		},
		{
			name:  "INFO",
			input: "INFO",
			want:  LogLevelInfo,
		},
		{
			name:  "WARNING",
			input: "WARNING",
			want:  LogLevelWarning,
		},
		{
			name:  "WARN",
			input: "WARN",
			want:  LogLevelWarning,
		},
		{
			name:  "ERROR",
			input: "ERROR",
			want:  LogLevelError,
		},
		{
			name:  "FATAL",
			input: "FATAL",
			want:  LogLevelFatal,
		},
		{
			name:  "Invalid",
			input: "INVALID",
			want:  LogLevelInfo, // Default
		},
		{
			name:  "Empty",
			input: "",
			want:  LogLevelInfo, // Default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LogLevelFromString(tt.input); got != tt.want {
				t.Errorf("LogLevelFromString() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDefaultLogger(t *testing.T) {
	if DefaultLogger == nil {
		t.Error("DefaultLogger is nil")
	}

	if DefaultLogger.level != LogLevelInfo {
		t.Errorf("DefaultLogger level = %v, want %v", DefaultLogger.level, LogLevelInfo)
	}
}

// captureStdio swaps os.Stdout and os.Stderr for files in a temp dir, and
// returns a function that restores them and answers what each received.
func captureStdio(t *testing.T) func() (stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	errf, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, errf
	return func() (string, string) {
		os.Stdout, os.Stderr = oldOut, oldErr
		_ = out.Close()
		_ = errf.Close()
		o, _ := os.ReadFile(out.Name())
		e, _ := os.ReadFile(errf.Name())
		return string(o), string(e)
	}
}

// A command's stdout is what it answers: `brokoli agents token create ... >
// triage.token` once wrote the log file's warning into the token file.
func TestLogLinesStayOffStdout(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BROKOLI_DATA_DIRS", t.TempDir()) // the working directory is outside it
	done := captureStdio(t)
	l := NewLogger(LogLevelDebug)
	l.Warning("first %d", 1)
	l.Info("second")
	l.Debug("a secret meant for the file alone")
	stdout, stderr := done()

	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "WARNING: first 1") || !strings.Contains(stderr, "second") {
		t.Errorf("stderr = %q, want both messages", stderr)
	}
	if n := strings.Count(stderr, "Failed to open log file"); n != 1 {
		t.Errorf("the log file's failure was said %d times, want once:\n%s", n, stderr)
	}
	if strings.Contains(stderr, "a secret") {
		t.Errorf("a debug line reached the console: %q", stderr)
	}
}

// DefaultLogger is made in every command; one that never logs must not
// create brok.log, or complain that it cannot.
func TestTheLogFileIsOpenedOnTheFirstMessage(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	done := captureStdio(t)
	l := NewLogger(LogLevelDebug)
	if _, err := os.Stat(filepath.Join(dir, LogFilePath)); !os.IsNotExist(err) {
		t.Fatalf("brok.log exists before anything was logged (err %v)", err)
	}
	l.Debug("only in the file")
	stdout, stderr := done()
	got, err := os.ReadFile(filepath.Join(dir, LogFilePath))
	if err != nil || !strings.Contains(string(got), "DEBUG: ") || !strings.Contains(string(got), "only in the file") {
		t.Fatalf("brok.log = %q (err %v), want the debug line", got, err)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("a debug line reached the console: stdout %q stderr %q", stdout, stderr)
	}
}
