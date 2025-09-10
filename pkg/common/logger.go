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

type Logger struct {
	debugLogger   *log.Logger
	infoLogger    *log.Logger
	warningLogger *log.Logger
	errorLogger   *log.Logger
	fatalLogger   *log.Logger
	level         LogLevel
}

func NewLogger(level LogLevel) *Logger {
	return NewLoggerWithWriter(os.Stdout, level)
}

func NewLoggerWithWriter(writer io.Writer, level LogLevel) *Logger {
	return &Logger{
		debugLogger:   log.New(writer, "DEBUG: ", log.Ldate|log.Ltime),
		infoLogger:    log.New(writer, "INFO: ", log.Ldate|log.Ltime),
		warningLogger: log.New(writer, "WARNING: ", log.Ldate|log.Ltime),
		errorLogger:   log.New(writer, "ERROR: ", log.Ldate|log.Ltime),
		fatalLogger:   log.New(writer, "FATAL: ", log.Ldate|log.Ltime),
		level:         level,
	}
}

func (l *Logger) SetLevel(level LogLevel) {
	l.level = level
}

func (l *Logger) Debug(format string, v ...interface{}) {
	if l.level <= LogLevelDebug {
		errors.CheckError(l.debugLogger.Output(2, fmt.Sprintf(format, v...)))
	}
}

func (l *Logger) Info(format string, v ...interface{}) {
	if l.level <= LogLevelInfo {
		errors.CheckError(l.infoLogger.Output(2, fmt.Sprintf(format, v...)))
	}
}

func (l *Logger) Warning(format string, v ...interface{}) {
	if l.level <= LogLevelWarning {
		errors.CheckError(l.warningLogger.Output(2, fmt.Sprintf(format, v...)))
	}
}

func (l *Logger) Error(format string, v ...interface{}) {
	if l.level <= LogLevelError {
		errors.CheckError(l.errorLogger.Output(2, fmt.Sprintf(format, v...)))
	}
}

func (l *Logger) Fatal(format string, v ...interface{}) {
	if l.level <= LogLevelFatal {
		errors.CheckError(l.fatalLogger.Output(2, fmt.Sprintf(format, v...)))
		os.Exit(1)
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
