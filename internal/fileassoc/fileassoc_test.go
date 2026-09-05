package fileassoc

import (
	"fmt"
	"math/rand"
	"testing"

	"golang.org/x/sys/windows/registry"

	"github.com/sanjaynagpal/stage/internal/layout"
)

func testExtension(t *testing.T) string {
	t.Helper()
	ext := fmt.Sprintf(".stagetest%d", rand.Int63())
	t.Cleanup(func() {
		_ = Unregister(layout.ScopePerUser, ext, progID("StageTest", ext))
	})
	return ext
}

func readDefaultValue(t *testing.T, path string) string {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("OpenKey(%s): %v", path, err)
	}
	defer k.Close()
	s, _, err := k.GetStringValue("")
	if err != nil {
		t.Fatalf("GetStringValue(%s, \"\"): %v", path, err)
	}
	return s
}

func TestRegisterWritesExpectedValues(t *testing.T) {
	ext := testExtension(t)
	riggerPath := `C:\Users\me\AppData\Local\ABC\rigger.exe`
	spec := Spec{Extension: ext, Description: "ABC Document", IconPath: `C:\ABC\app.ico`}

	id, err := Register(layout.ScopePerUser, "StageTest", spec, riggerPath)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if want := "StageTest." + ext[1:]; id != want {
		t.Fatalf("progID = %q, want %q", id, want)
	}

	if got, want := readDefaultValue(t, `Software\Classes\`+ext), id; got != want {
		t.Errorf("extension default value = %q, want %q", got, want)
	}
	if got, want := readDefaultValue(t, `Software\Classes\`+id), spec.Description; got != want {
		t.Errorf("ProgID default value = %q, want %q", got, want)
	}
	if got, want := readDefaultValue(t, `Software\Classes\`+id+`\DefaultIcon`), spec.IconPath; got != want {
		t.Errorf("DefaultIcon value = %q, want %q", got, want)
	}
	wantCmd := `"` + riggerPath + `" "%1"`
	if got := readDefaultValue(t, `Software\Classes\`+id+`\shell\open\command`); got != wantCmd {
		t.Errorf("command value = %q, want %q", got, wantCmd)
	}
}

func TestUnregisterRemovesKeys(t *testing.T) {
	ext := testExtension(t)
	id, err := Register(layout.ScopePerUser, "StageTest", Spec{Extension: ext, Description: "d"}, `C:\ABC\rigger.exe`)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := Unregister(layout.ScopePerUser, ext, id); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if _, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\`+id, registry.QUERY_VALUE); err == nil {
		t.Fatal("expected the ProgID key to be gone after Unregister")
	}
	if _, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\`+ext, registry.QUERY_VALUE); err == nil {
		t.Fatal("expected the extension key to be gone after Unregister")
	}
}

func TestUnregisterIsIdempotent(t *testing.T) {
	ext := testExtension(t)
	if err := Unregister(layout.ScopePerUser, ext, progID("StageTest", ext)); err != nil {
		t.Fatalf("Unregister on a nonexistent association should not error, got: %v", err)
	}
}
