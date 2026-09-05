// Package javainvoke builds and launches the java command line resolved
// from a manifest. Rigger execs the JVM and exits immediately — it does not
// supervise it (docs/REQUIREMENTS.md §6).
package javainvoke

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// LaunchSpec is a fully-resolved java invocation: every placeholder has
// already been substituted (see internal/manifest.Manifest.Resolve).
type LaunchSpec struct {
	// JREDir is the JRE's directory (e.g. <root>/jre/21.0.2+13).
	JREDir string
	// Classpath entries, already resolved (glob-expanded, root-relative
	// entries turned absolute) — see ResolveClasspath.
	Classpath  []string
	MainClass  string
	JVMOptions []string
	Arguments  []string
}

// JavaExePath returns javaw.exe (no console window) under a JRE directory.
func JavaExePath(jreDir string) string {
	return filepath.Join(jreDir, "bin", "javaw.exe")
}

// ResolveClasspath turns a manifest's classpath entries (root-relative,
// possibly containing "*" globs such as "1.5.0/lib/*.jar") into absolute
// paths for the java -cp argument.
func ResolveClasspath(root string, entries []string) ([]string, error) {
	var out []string
	for _, entry := range entries {
		full := filepath.Join(root, entry)
		if strings.ContainsAny(entry, "*?[") {
			matches, err := filepath.Glob(full)
			if err != nil {
				return nil, fmt.Errorf("javainvoke: invalid classpath glob %q: %w", entry, err)
			}
			out = append(out, matches...)
			continue
		}
		out = append(out, full)
	}
	return out, nil
}

// CommandLine builds the full argv for the java invocation:
// [jvmOptions..., -cp, classpath, mainClass, arguments...].
func (s LaunchSpec) CommandLine() []string {
	args := make([]string, 0, len(s.JVMOptions)+2+1+len(s.Arguments))
	args = append(args, s.JVMOptions...)
	if len(s.Classpath) > 0 {
		args = append(args, "-cp", strings.Join(s.Classpath, ";"))
	}
	args = append(args, s.MainClass)
	args = append(args, s.Arguments...)
	return args
}

// Launch execs java as a detached, fire-and-forget process: it is not tied
// to rigger.exe's own lifetime, and Rigger does not wait for it to exit.
func Launch(spec LaunchSpec) error {
	javaExe := JavaExePath(spec.JREDir)
	if _, err := os.Stat(javaExe); err != nil {
		return fmt.Errorf("javainvoke: java executable not found at %s: %w", javaExe, err)
	}

	cmd := exec.Command(javaExe, spec.CommandLine()...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("javainvoke: failed to start %s: %w", javaExe, err)
	}
	return cmd.Process.Release()
}
