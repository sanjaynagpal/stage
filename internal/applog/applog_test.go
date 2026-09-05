package applog

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestOpenCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "dir", "rigger.log")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("expected parent directory to exist, got: %v", err)
	}
}

func TestInfoAndWarnWriteFileContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rigger.log")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	l.Info("hello %d", 1)
	l.Warn("uh %s", "oh")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), lines)
	}

	infoRe := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} INFO hello 1$`)
	if !infoRe.MatchString(lines[0]) {
		t.Errorf("line 0 = %q, want to match %s", lines[0], infoRe)
	}
	warnRe := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} WARN uh oh$`)
	if !warnRe.MatchString(lines[1]) {
		t.Errorf("line 1 = %q, want to match %s", lines[1], warnRe)
	}
}

func TestAppendAcrossMultipleOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rigger.log")

	l1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	l1.Info("first")
	if err := l1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	l2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	l2.Info("second")
	if err := l2.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "first") || !strings.Contains(content, "second") {
		t.Fatalf("expected both entries to be present (append, not truncate), got: %q", content)
	}
}

func TestNilLoggerMethodsAreSafe(t *testing.T) {
	var l *Logger
	l.Info("x")
	l.Warn("y")
	l.LogFatal(errors.New("z"))
	if err := l.Close(); err != nil {
		t.Fatalf("Close on nil Logger = %v, want nil", err)
	}
}

func TestNilLoggerStillWritesConsole(t *testing.T) {
	stdout := redirect(t, &os.Stdout)
	stderr := redirect(t, &os.Stderr)

	var l *Logger
	l.Info("info-message")
	l.Warn("warn-message")

	if got := stdout(); !strings.Contains(got, "info-message") {
		t.Errorf("expected nil Logger's Info to still print to stdout, got %q", got)
	}
	if got := stderr(); !strings.Contains(got, "warn-message") {
		t.Errorf("expected nil Logger's Warn to still print to stderr, got %q", got)
	}
}

func TestLogFatalWritesFileOnlyNotConsole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rigger.log")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	stdout := redirect(t, &os.Stdout)
	stderr := redirect(t, &os.Stderr)

	l.LogFatal(errors.New("boom"))
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := stdout(); got != "" {
		t.Errorf("expected no stdout output from LogFatal, got %q", got)
	}
	if got := stderr(); got != "" {
		t.Errorf("expected no stderr output from LogFatal, got %q", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "FATAL") || !strings.Contains(string(data), "boom") {
		t.Fatalf("expected a FATAL line mentioning the error, got: %q", data)
	}
}

func TestCloseIsIdempotentEnoughForDoubleCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rigger.log")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = l.Close()
	_ = l.Close() // must not panic, even if this returns an error
}

// redirect points *target at a pipe for the duration of the test and
// returns a function that closes the write end and returns everything
// written to it. A goroutine drains the read end continuously, since a
// pipe's read blocks until EOF (i.e. until the write end is closed) and
// its buffer is bounded.
func redirect(t *testing.T, target **os.File) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	original := *target
	*target = w

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()

	t.Cleanup(func() {
		*target = original
	})

	return func() string {
		w.Close()
		content := <-done
		r.Close()
		return content
	}
}
