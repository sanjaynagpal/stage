// Package applog is Rigger's (and later maintain.exe's) tiered feedback
// logger: every message is written to the console immediately, and
// durably appended to a per-install log file, so a launch's history
// survives past the point rigger.exe stops having a console at all (see
// internal/uierror's documented windowsgui-subsystem end state).
package applog

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Logger writes tiered feedback: Info/Warn always go to console, and to
// the log file too when one is open. All methods are safe to call on a
// nil *Logger — console output still happens; only the file write is
// skipped — so a caller that gets a nil Logger back from a failed Open
// never needs its own nil-checks at each call site.
type Logger struct {
	f *os.File
}

// Open creates path's parent directory if needed and opens it for
// durable, history-preserving appends: rigger.exe runs fresh on every
// launch (unlike a long-lived daemon), so O_APPEND|O_CREATE is what makes
// every launch's entries accumulate rather than overwrite the last one.
//
// Known accepted limitation: there is no rotation or size cap, so the log
// file grows unboundedly over an install's life. Out of scope for now.
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("applog: create log directory for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("applog: open %s: %w", path, err)
	}
	return &Logger{f: f}, nil
}

// Info records a normal status line: written to stdout, and to the log
// file if one is open.
func (l *Logger) Info(format string, args ...any) {
	l.write(os.Stdout, "INFO", format, args...)
}

// Warn records a non-fatal problem: written to stderr, and to the log
// file if one is open.
func (l *Logger) Warn(format string, args ...any) {
	l.write(os.Stderr, "WARN", format, args...)
}

func (l *Logger) write(console *os.File, level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(console, msg)
	if l != nil && l.f != nil {
		fmt.Fprintf(l.f, "%s %-4s %s\n", stamp(), level, msg)
	}
}

// LogFatal records a fatal error to the log file only — never console.
// The caller (main's uierror.Fatalf) is already responsible for the
// user-visible report of a fatal error, so duplicating it on stderr here
// would just be noise.
func (l *Logger) LogFatal(err error) {
	if l == nil || l.f == nil || err == nil {
		return
	}
	fmt.Fprintf(l.f, "%s %-4s %v\n", stamp(), "FATAL", err)
}

// Close closes the underlying log file. Safe to call on a nil Logger.
func (l *Logger) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	return l.f.Close()
}

func stamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}
