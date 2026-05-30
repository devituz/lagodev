// Package logger provides a small, dependency-free structured logger used
// across the framework. It supports colored output, leveled logs, SQL query
// logging, and slow-query reporting. Drop-in replacements can be wired by
// implementing the Logger interface in your application.
package logger

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level enumerates log severities.
type Level int8

const (
	LevelDebug Level = iota - 1
	LevelInfo
	LevelWarn
	LevelError
	LevelSilent Level = 99
)

// Logger writes leveled & SQL-aware log lines to an io.Writer.
type Logger struct {
	mu      sync.Mutex
	out     io.Writer
	err     io.Writer
	level   Level
	colored bool
}

// New creates a Logger writing to stdout/stderr.
func New(level Level) *Logger {
	return &Logger{
		out:     os.Stdout,
		err:     os.Stderr,
		level:   level,
		colored: isTTY(os.Stdout),
	}
}

// NewWith creates a Logger with custom writers (useful in tests).
func NewWith(out, err io.Writer, level Level, colored bool) *Logger {
	return &Logger{out: out, err: err, level: level, colored: colored}
}

// SetLevel updates the runtime level.
func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// Debug logs at the debug level.
func (l *Logger) Debug(args ...any) { l.write(LevelDebug, fmt.Sprint(args...)) }

// Debugf logs a formatted debug message.
func (l *Logger) Debugf(format string, args ...any) {
	l.write(LevelDebug, fmt.Sprintf(format, args...))
}

// Info logs at the info level.
func (l *Logger) Info(args ...any) { l.write(LevelInfo, fmt.Sprint(args...)) }

// Infof logs a formatted info message.
func (l *Logger) Infof(format string, args ...any) { l.write(LevelInfo, fmt.Sprintf(format, args...)) }

// Warn logs at the warn level.
func (l *Logger) Warn(args ...any) { l.write(LevelWarn, fmt.Sprint(args...)) }

// Warnf logs a formatted warn message.
func (l *Logger) Warnf(format string, args ...any) { l.write(LevelWarn, fmt.Sprintf(format, args...)) }

// Error logs at the error level.
func (l *Logger) Error(args ...any) { l.write(LevelError, fmt.Sprint(args...)) }

// Errorf logs a formatted error message.
func (l *Logger) Errorf(format string, args ...any) {
	l.write(LevelError, fmt.Sprintf(format, args...))
}

// SQL records a query trace.
func (l *Logger) SQL(_ context.Context, query string, args []any, took time.Duration, err error) {
	if l.level > LevelDebug {
		return
	}
	prefix := l.color("SQL", colorCyan)
	if err != nil {
		prefix = l.color("SQL!", colorRed)
	}
	l.write(LevelDebug, fmt.Sprintf("%s [%s] %s %s", prefix, took.Round(time.Microsecond), trim(query), formatArgs(args)))
}

// SlowSQL records a slow-query trace at warn level.
func (l *Logger) SlowSQL(_ context.Context, query string, args []any, took time.Duration, err error) {
	prefix := l.color("SLOW", colorYellow)
	if err != nil {
		prefix = l.color("SLOW!", colorRed)
	}
	l.write(LevelWarn, fmt.Sprintf("%s [%s] %s %s", prefix, took.Round(time.Microsecond), trim(query), formatArgs(args)))
}

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
)

func (l *Logger) color(s, c string) string {
	if !l.colored {
		return s
	}
	return c + s + colorReset
}

func (l *Logger) write(level Level, msg string) {
	if level < l.level {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.out
	tag := "INFO"
	switch level {
	case LevelDebug:
		tag = l.color("DEBUG", colorGray)
	case LevelInfo:
		tag = l.color("INFO ", colorGreen)
	case LevelWarn:
		tag = l.color("WARN ", colorYellow)
		w = l.err
	case LevelError:
		tag = l.color("ERROR", colorRed)
		w = l.err
	}
	stamp := time.Now().Format("15:04:05.000")
	fmt.Fprintf(w, "%s %s %s\n", l.color(stamp, colorGray), tag, msg)
}

func trim(q string) string {
	q = strings.TrimSpace(q)
	q = strings.Join(strings.Fields(q), " ")
	if len(q) > 400 {
		q = q[:397] + "..."
	}
	return q
}

func formatArgs(args []any) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = fmt.Sprintf("%v", a)
	}
	return "-- " + strings.Join(parts, ", ")
}

// isTTY is a best-effort check; we cannot import syscalls here, so we rely
// on the well-known environment hint.
func isTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
