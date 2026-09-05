package javainvoke

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJavaExePath(t *testing.T) {
	got := JavaExePath(`C:\Program Files\ABC\jre\21.0.2+13`)
	want := filepath.Join(`C:\Program Files\ABC\jre\21.0.2+13`, "bin", "javaw.exe")
	if got != want {
		t.Errorf("JavaExePath = %q, want %q", got, want)
	}
}

func TestCommandLineOrder(t *testing.T) {
	spec := LaunchSpec{
		Classpath:  []string{`C:\ABC\1.5.0\app.jar`, `C:\ABC\1.5.0\lib\a.jar`},
		MainClass:  "com.example.abc.Main",
		JVMOptions: []string{"-Xmx512m", "-Dabc.env=PROD"},
		Arguments:  []string{"--mode", "standard"},
	}
	got := spec.CommandLine()
	want := []string{
		"-Xmx512m", "-Dabc.env=PROD",
		"-cp", `C:\ABC\1.5.0\app.jar;C:\ABC\1.5.0\lib\a.jar`,
		"com.example.abc.Main",
		"--mode", "standard",
	}
	if len(got) != len(want) {
		t.Fatalf("CommandLine() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CommandLine()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestCommandLineWithNoClasspath(t *testing.T) {
	spec := LaunchSpec{MainClass: "Main"}
	got := spec.CommandLine()
	want := []string{"Main"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("CommandLine() = %v, want %v", got, want)
	}
}

func TestResolveClasspathPlainEntries(t *testing.T) {
	root := t.TempDir()
	out, err := ResolveClasspath(root, []string{"1.5.0/app.jar"})
	if err != nil {
		t.Fatalf("ResolveClasspath: %v", err)
	}
	want := []string{filepath.Join(root, "1.5.0", "app.jar")}
	if len(out) != 1 || out[0] != want[0] {
		t.Fatalf("ResolveClasspath = %v, want %v", out, want)
	}
}

func TestResolveClasspathExpandsGlobs(t *testing.T) {
	root := t.TempDir()
	libDir := filepath.Join(root, "1.5.0", "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.jar", "b.jar"} {
		if err := os.WriteFile(filepath.Join(libDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := ResolveClasspath(root, []string{"1.5.0/lib/*.jar"})
	if err != nil {
		t.Fatalf("ResolveClasspath: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 glob-expanded jars, got %v", out)
	}
}

func TestLaunchErrorsWhenJavaMissing(t *testing.T) {
	root := t.TempDir() // no bin/javaw.exe here
	err := Launch(LaunchSpec{JREDir: root, MainClass: "Main"})
	if err == nil {
		t.Fatal("expected error when javaw.exe does not exist")
	}
}
