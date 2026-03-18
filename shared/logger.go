package shared

import (
	"fmt"
	"strings"
	"time"
)

// LogLevel represents the severity of a log message
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

var currentLevel LogLevel = INFO

// SetLogLevel sets the global log level
func SetLogLevel(level string) {
	switch strings.ToLower(level) {
	case "debug":
		currentLevel = DEBUG
	case "info":
		currentLevel = INFO
	case "warn", "warning":
		currentLevel = WARN
	case "error":
		currentLevel = ERROR
	default:
		currentLevel = INFO
	}
}

// GetLogLevel returns the current log level as a string
func GetLogLevel() string {
	switch currentLevel {
	case DEBUG:
		return "debug"
	case INFO:
		return "info"
	case WARN:
		return "warn"
	case ERROR:
		return "error"
	default:
		return "info"
	}
}

// logJSON outputs a structured JSON log message if level >= current level
func logJSON(level LogLevel, levelStr string, msg string) {
	if level < currentLevel {
		return
	}
	fmt.Printf(`{"level":"%s","message":%q,"time":"%s"}`+"\n",
		levelStr, msg, time.Now().UTC().Format("2006-01-02T15:04:05Z"))
}

// Debug logs a debug message
func Debug(msg string) {
	logJSON(DEBUG, "debug", msg)
}

// Debugf logs a formatted debug message
func Debugf(format string, args ...interface{}) {
	logJSON(DEBUG, "debug", fmt.Sprintf(format, args...))
}

// Info logs an info message
func Info(msg string) {
	logJSON(INFO, "info", msg)
}

// Infof logs a formatted info message
func Infof(format string, args ...interface{}) {
	logJSON(INFO, "info", fmt.Sprintf(format, args...))
}

// Warn logs a warning message
func Warn(msg string) {
	logJSON(WARN, "warn", msg)
}

// Warnf logs a formatted warning message
func Warnf(format string, args ...interface{}) {
	logJSON(WARN, "warn", fmt.Sprintf(format, args...))
}

// Error logs an error message
func Error(msg string) {
	logJSON(ERROR, "error", msg)
}

// Errorf logs a formatted error message
func Errorf(format string, args ...interface{}) {
	logJSON(ERROR, "error", fmt.Sprintf(format, args...))
}

// IsDebugEnabled returns true if debug logging is enabled
func IsDebugEnabled() bool {
	return currentLevel <= DEBUG
}
