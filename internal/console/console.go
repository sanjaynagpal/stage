// Package console provides the minimal, plain-text prompts cmd/installer
// and cmd/uninstaller use as their UI for now, in place of the polished
// WebView2 wizard planned for later (docs/REQUIREMENTS.md §11b, §16) —
// simple enough to be fully unit-testable by feeding a fake stdin, and to
// work whether or not rigger-style windowsgui-subsystem output visibility
// even matters yet (these two binaries are plain console-subsystem exes).
package console

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Confirm prints a y/n prompt to stdout and reads a line from stdin,
// returning defaultYes if the user just presses Enter.
func Confirm(prompt string, defaultYes bool) bool {
	return confirm(os.Stdin, os.Stdout, prompt, defaultYes)
}

func confirm(in io.Reader, out io.Writer, prompt string, defaultYes bool) bool {
	hint := "[Y/n]"
	if !defaultYes {
		hint = "[y/N]"
	}
	fmt.Fprintf(out, "%s %s ", prompt, hint)
	line := strings.ToLower(strings.TrimSpace(readLine(in)))
	if line == "" {
		return defaultYes
	}
	return line == "y" || line == "yes"
}

// RetryCancel repeatedly calls check. If check reports an error, prompt
// (describing the problem) is shown with a [R]etry/[C]ancel choice: Retry
// (or any input other than Cancel) calls check again; Cancel returns the
// last error. Returns nil as soon as check succeeds.
func RetryCancel(prompt string, check func() error) error {
	return retryCancel(os.Stdin, os.Stdout, prompt, check)
}

func retryCancel(in io.Reader, out io.Writer, prompt string, check func() error) error {
	for {
		err := check()
		if err == nil {
			return nil
		}
		fmt.Fprintf(out, "%s: %v\n[R]etry/[C]ancel: ", prompt, err)
		line := strings.ToLower(strings.TrimSpace(readLine(in)))
		if strings.HasPrefix(line, "c") {
			return err
		}
	}
}

// ReadLine prints prompt (no trailing newline added) and returns the
// trimmed line read from stdin.
func ReadLine(prompt string) string {
	return readLineWithPrompt(os.Stdin, os.Stdout, prompt)
}

func readLineWithPrompt(in io.Reader, out io.Writer, prompt string) string {
	fmt.Fprint(out, prompt)
	return strings.TrimSpace(readLine(in))
}

func readLine(in io.Reader) string {
	scanner := bufio.NewScanner(in)
	if scanner.Scan() {
		return scanner.Text()
	}
	return ""
}
