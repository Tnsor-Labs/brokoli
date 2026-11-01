package common

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarning
	LogLevelError
	LogLevelFatal
)

const (
	LogFilePath = "brok.log"
)

type Logger struct {
	debugLogger   *log.Logger
	infoLogger    *log.Logger
	warningLogger *log.Logger
	errorLogger   *log.Logger
	fatalLogger   *log.Logger
	level         LogLevel
	fileWriter    io.Writer
	consoleWriter io.Writer
}

// NewLogger creates a logger at level. Every message goes to brok.log;
// info and above also go to stderr. Log lines are diagnostics, so they
// stay off stdout, where a command writes what it answers: a CLI command
// whose output is redirected (`brokoli agents token create ... > file`)
// must not find a log line in the file.
//
// The file is opened on the first message, not here: DefaultLogger is made
// when the package loads, in every command, and most never log.
func NewLogger(level LogLevel) *Logger {
	return NewLoggerWithWriters(&lazyLogFile{path: LogFilePath}, os.Stderr, level)
}

// lazyLogFile opens the log file on its first write. If it cannot be
// opened, that is said once on stderr; info and above still reach stderr,
// and debug lines, which are meant for the file alone, are dropped.
type lazyLogFile struct {
	path string
	once sync.Once
	w    io.Writer
}

func (f *lazyLogFile) Write(p []byte) (int, error) {
	f.once.Do(func() {
		file, err := OpenInDataDir(f.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: Failed to open log file: %v\n", err)
			f.w = io.Discard
			return
		}
		f.w = file
	})
	return f.w.Write(p)
}

// NewLoggerWithWriter creates a logger with a single writer for all log levels
func NewLoggerWithWriter(writer io.Writer, level LogLevel) *Logger {
	return NewLoggerWithWriters(writer, writer, level)
}

// NewLoggerWithWriters creates a logger with separate writers for file and console
func NewLoggerWithWriters(fileWriter io.Writer, consoleWriter io.Writer, level LogLevel) *Logger {
	// For file logging, we want all logs
	return &Logger{
		debugLogger:   log.New(fileWriter, "DEBUG: ", log.Ldate|log.Ltime),
		infoLogger:    log.New(fileWriter, "INFO: ", log.Ldate|log.Ltime),
		warningLogger: log.New(fileWriter, "WARNING: ", log.Ldate|log.Ltime),
		errorLogger:   log.New(fileWriter, "ERROR: ", log.Ldate|log.Ltime),
		fatalLogger:   log.New(fileWriter, "FATAL: ", log.Ldate|log.Ltime),
		level:         level,
		fileWriter:    fileWriter,
		consoleWriter: consoleWriter,
	}
}

func (l *Logger) SetLevel(level LogLevel) {
	l.level = level
}

// output writes one log line. Logging is best effort: a write that fails
// (a full disk, a closed file) is dropped. It used to go to
// errors.CheckError, which calls log.Fatalf, so a failed log write ended
// the whole server. Depth 3 keeps the file and line of the caller of
// Debug, Info and the rest.
func (l *Logger) output(lg *log.Logger, message string) {
	_ = lg.Output(3, message)
}

// Debug logs a message at debug level.
// Debug logs are only written to the log file, not to the console.
// This should be used for sensitive data that should not be exposed in normal operation.
func (l *Logger) Debug(format string, v ...interface{}) {
	if l.level <= LogLevelDebug {
		// Debug messages only go to the log file, not to console
		l.output(l.debugLogger, fmt.Sprintf(format, v...))
	}
}

// Info logs a message at info level.
// Info logs are written to both the log file and the console.
func (l *Logger) Info(format string, v ...interface{}) {
	if l.level <= LogLevelInfo {
		message := fmt.Sprintf(format, v...)
		l.output(l.infoLogger, message)

		// For console output, we might want to use in-place updates for progress messages
		if l.consoleWriter != nil && l.consoleWriter != l.fileWriter {
			if strings.HasPrefix(message, "Processing") || strings.Contains(message, "progress") {
				// Use carriage return for in-place updates
				_, _ = fmt.Fprintf(l.consoleWriter, "\r%s", message)
			} else {
				// Normal output with newline
				_, _ = fmt.Fprintf(l.consoleWriter, "%s\n", message)
			}
		}
	}
}

// Warning logs a message at warning level.
func (l *Logger) Warning(format string, v ...interface{}) {
	if l.level <= LogLevelWarning {
		message := fmt.Sprintf(format, v...)
		l.output(l.warningLogger, message)

		// Also output to console if it's different from the file writer
		if l.consoleWriter != nil && l.consoleWriter != l.fileWriter {
			_, _ = fmt.Fprintf(l.consoleWriter, "WARNING: %s\n", message)
		}
	}
}

// Error logs a message at error level.
func (l *Logger) Error(format string, v ...interface{}) {
	if l.level <= LogLevelError {
		message := fmt.Sprintf(format, v...)
		l.output(l.errorLogger, message)

		// Also output to console if it's different from the file writer
		if l.consoleWriter != nil && l.consoleWriter != l.fileWriter {
			_, _ = fmt.Fprintf(l.consoleWriter, "ERROR: %s\n", message)
		}
	}
}

// Fatal logs a message at fatal level and then exits the program.
func (l *Logger) Fatal(format string, v ...interface{}) {
	if l.level <= LogLevelFatal {
		message := fmt.Sprintf(format, v...)
		l.output(l.fatalLogger, message)

		// Also output to console if it's different from the file writer
		if l.consoleWriter != nil && l.consoleWriter != l.fileWriter {
			_, _ = fmt.Fprintf(l.consoleWriter, "FATAL: %s\n", message)
		}

		os.Exit(1)
	}
}

// Progress logs a message with a carriage return for in-place updates.
// This is useful for showing progress without filling the console with lines.
func (l *Logger) Progress(format string, v ...interface{}) {
	if l.level <= LogLevelInfo {
		message := fmt.Sprintf(format, v...)

		// Log to file with normal format
		l.output(l.infoLogger, fmt.Sprintf("PROGRESS: %s", message))

		// For console, use carriage return for in-place update
		if l.consoleWriter != nil && l.consoleWriter != l.fileWriter {
			_, _ = fmt.Fprintf(l.consoleWriter, "\r%s", message)
		}
	}
}

func LogLevelFromString(level string) LogLevel {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return LogLevelDebug
	case "INFO":
		return LogLevelInfo
	case "WARNING", "WARN":
		return LogLevelWarning
	case "ERROR":
		return LogLevelError
	case "FATAL":
		return LogLevelFatal
	default:
		return LogLevelInfo // Default to info
	}
}

var DefaultLogger = NewLogger(LogLevelInfo)
