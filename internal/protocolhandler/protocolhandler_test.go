package protocolhandler

import (
	"fmt"
	"math/rand"
	"testing"

	"golang.org/x/sys/windows/registry"

	"github.com/sanjaynagpal/stage/internal/layout"
)

// testScheme returns a disposable scheme name unique to this test run, so
// tests never collide with a real registration or each other.
func testScheme(t *testing.T) string {
	t.Helper()
	scheme := fmt.Sprintf("stagetest%d", rand.Int63())
	t.Cleanup(func() {
		_ = Unregister(layout.ScopePerUser, scheme)
	})
	return scheme
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
	scheme := testScheme(t)
	riggerPath := `C:\Users\me\AppData\Local\ABC\rigger.exe`

	if err := Register(layout.ScopePerUser, scheme, "ABC", riggerPath); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := schemeKeyPath(scheme)
	if got, want := readDefaultValue(t, base), "URL:ABC Protocol"; got != want {
		t.Errorf("scheme key default = %q, want %q", got, want)
	}

	k, err := registry.OpenKey(registry.CURRENT_USER, base, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("OpenKey(%s): %v", base, err)
	}
	if _, _, err := k.GetStringValue("URL Protocol"); err != nil {
		t.Errorf("expected a URL Protocol marker value, got error: %v", err)
	}
	k.Close()

	cmdPath := base + `\shell\open\command`
	wantCmd := `"` + riggerPath + `" "%1"`
	if got := readDefaultValue(t, cmdPath); got != wantCmd {
		t.Errorf("command value = %q, want %q", got, wantCmd)
	}
}

func TestUnregisterRemovesKeys(t *testing.T) {
	scheme := testScheme(t)
	if err := Register(layout.ScopePerUser, scheme, "ABC", `C:\ABC\rigger.exe`); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := Unregister(layout.ScopePerUser, scheme); err != nil {
		t.Fatalf("Unregister: %v", err)
	}

	base := schemeKeyPath(scheme)
	if _, err := registry.OpenKey(registry.CURRENT_USER, base, registry.QUERY_VALUE); err == nil {
		t.Fatal("expected the scheme key to be gone after Unregister")
	}
}

func TestUnregisterIsIdempotent(t *testing.T) {
	scheme := testScheme(t)
	if err := Unregister(layout.ScopePerUser, scheme); err != nil {
		t.Fatalf("Unregister on a nonexistent scheme should not error, got: %v", err)
	}
}
