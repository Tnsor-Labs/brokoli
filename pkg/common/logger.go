package common

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/hc12r/brokolisql-go/pkg/errors"
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

// NewLogger creates a new logger with the specified log level
// Logs will be written to both console and brok.log file
func NewLogger(level LogLevel) *Logger {
	// Create file writer for brok.log using safe file operations
	// Since SafeCreateFile doesn't support append mode, we'll use a combination of SafeOpenFile and os.OpenFile
	// First check if the file exists and is within allowed directories
	_, err := SafeOpenFile(LogFilePath)
	if err != nil && !os.IsNotExist(err) {
		// If there's an error other than "file doesn't exist", log and use stdout
		fmt.Printf("WARNING: Failed to safely access log file: %v\n", err)
		return NewLoggerWithWriter(os.Stdout, level)
	}

	// Now use os.OpenFile with the validated path
	fileWriter, err := os.OpenFile(LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		// If we can't open the log file, just log to console
		fmt.Printf("WARNING: Failed to open log file: %v\n", err)
		return NewLoggerWithWriter(os.Stdout, level)
	}

	// For console output, we only want to show important messages (info and above)
	// Debug messages will only go to the log file
	logger := NewLoggerWithWriters(fileWriter, os.Stdout, level)

	return logger
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

// Debug logs a message at debug level.
// Debug logs are only written to the log file, not to the console.
// This should be used for sensitive data that should not be exposed in normal operation.
func (l *Logger) Debug(format string, v ...interface{}) {
	if l.level <= LogLevelDebug {
		// Debug messages only go to the log file, not to console
		errors.CheckError(l.debugLogger.Output(2, fmt.Sprintf(format, v...)))
	}
}

// Info logs a message at info level.
// Info logs are written to both the log file and the console.
func (l *Logger) Info(format string, v ...interface{}) {
	if l.level <= LogLevelInfo {
		message := fmt.Sprintf(format, v...)
		errors.CheckError(l.infoLogger.Output(2, message))

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
		errors.CheckError(l.warningLogger.Output(2, message))

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
		errors.CheckError(l.errorLogger.Output(2, message))

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
		errors.CheckError(l.fatalLogger.Output(2, message))

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
		errors.CheckError(l.infoLogger.Output(2, fmt.Sprintf("PROGRESS: %s", message)))

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
