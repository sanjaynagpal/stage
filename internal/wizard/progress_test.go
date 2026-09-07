package wizard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestProgress builds a *progressState with a real HTTP handler (via
// httptest) but without ShowProgress's listener/browser-launch, so
// handler/state logic can be exercised directly — mirroring newTestUI.
func newTestProgress(t *testing.T) (*progressState, *httptest.Server) {
	t.Helper()
	s := &progressState{title: "ABC", token: "testtoken"}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func TestProgressIndexRendersStatusAndLog(t *testing.T) {
	s, srv := newTestProgress(t)
	s.notify("Downloading Java runtime 21.0.2+13...")
	s.progress("Downloading Java runtime 21.0.2+13... 42%")

	resp, err := http.Get(srv.URL + "/testtoken/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)

	if !strings.Contains(body, "Downloading Java runtime 21.0.2+13... 42%") {
		t.Fatalf("expected the live status in body, got: %s", body)
	}
	if !strings.Contains(body, "Downloading Java runtime 21.0.2+13...") {
		t.Fatalf("expected the original milestone in the log, got: %s", body)
	}
	if !strings.Contains(body, "Starting ABC") {
		t.Fatalf("expected the page title/heading to name the app, got: %s", body)
	}
}

func TestProgressDoesNotAppendToLog(t *testing.T) {
	s, _ := newTestProgress(t)
	s.notify("Downloading Java runtime 21.0.2+13...")
	s.progress("Downloading Java runtime 21.0.2+13... 10%")
	s.progress("Downloading Java runtime 21.0.2+13... 20%")

	data := s.snapshot()
	if data.Status != "Downloading Java runtime 21.0.2+13... 20%" {
		t.Fatalf("status = %q, want the latest progress call's text", data.Status)
	}
	if len(data.Log) != 1 || data.Log[0] != "Downloading Java runtime 21.0.2+13..." {
		t.Fatalf("log = %v, want only the original Notify milestone (progress calls must not append)", data.Log)
	}
}

func TestProgressScreenHighlightsBacktickedValues(t *testing.T) {
	s, srv := newTestProgress(t)
	s.notify("Installed Java runtime 21.0.2+13 to `C:\\Users\\you\\AppData\\Local\\ABC\\jre\\21.0.2+13`.")

	resp, err := http.Get(srv.URL + "/testtoken/screen")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)

	if strings.Contains(body, "`") {
		t.Fatalf("literal backtick leaked into rendered HTML: %s", body)
	}
	if !strings.Contains(body, `<span class="value">`) {
		t.Fatalf("expected the backticked path to be wrapped in a value span, got: %s", body)
	}
}

func TestProgressScreenBeforeFinishKeepsPolling(t *testing.T) {
	s, srv := newTestProgress(t)
	s.notify("Downloading app.jar...")

	resp, err := http.Get(srv.URL + "/testtoken/screen")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)

	if !strings.Contains(body, `hx-trigger="every 1s"`) {
		t.Fatalf("expected the page to keep auto-polling before finish, got: %s", body)
	}
	if strings.Contains(body, "data-progress-done") {
		t.Fatalf("did not expect the done marker before finish, got: %s", body)
	}
}

func TestProgressScreenAfterFinishStopsPollingAndShowsTerminalState(t *testing.T) {
	s, srv := newTestProgress(t)
	s.notify("Downloading app.jar...")
	s.finish()

	resp, err := http.Get(srv.URL + "/testtoken/screen")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)

	if strings.Contains(body, `hx-trigger="every 1s"`) {
		t.Fatalf("expected auto-polling to stop once finished, got: %s", body)
	}
	if !strings.Contains(body, "you may also close it now") {
		t.Fatalf("expected a terminal notice once finished, got: %s", body)
	}
	if !strings.Contains(body, "Downloading app.jar...") {
		t.Fatalf("expected the log history to still be shown, got: %s", body)
	}
	if !strings.Contains(body, "data-progress-done") {
		t.Fatalf("expected the done marker the page's auto-close script looks for, got: %s", body)
	}
}

func TestProgressPageShellIncludesAutoCloseScript(t *testing.T) {
	// The auto-close script must live in the static shell (loaded once at
	// GET /, not swapped in later via /screen) since script tags injected
	// via innerHTML swaps never execute — see progress.html's comment.
	_, srv := newTestProgress(t)
	resp, err := http.Get(srv.URL + "/testtoken/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)

	if !strings.Contains(body, "setTimeout(function () { window.close(); }, CLOSE_DELAY_MS)") {
		t.Fatalf("expected the auto-close to be deferred by CLOSE_DELAY_MS, not fire immediately on detecting the marker (the final status/log needs to actually be readable), got: %s", body)
	}
	if !strings.Contains(body, "data-progress-done") {
		t.Fatalf("expected the auto-close script to look for the done marker, got: %s", body)
	}
}

func TestProgressHTMXAssetServed(t *testing.T) {
	_, srv := newTestProgress(t)
	resp, err := http.Get(srv.URL + "/testtoken/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)
	if !strings.Contains(body, "htmx") {
		t.Fatalf("expected htmx source, got %d bytes", len(body))
	}
}

func TestProgressUINilSafe(t *testing.T) {
	var p *ProgressUI
	p.Notify("should not panic")
	p.Progress("should not panic")
	p.Close()
}

func TestProgressUICloseIsIdempotent(t *testing.T) {
	// Built directly rather than via ShowProgress, which launches a real
	// browser — http.Server.Close is safe to call even without a prior
	// Serve, so this exercises Close's own idempotency without that side
	// effect.
	p := &ProgressUI{state: &progressState{title: "ABC", token: "t"}, srv: &http.Server{}}
	p.Close()
	if !p.state.snapshot().Done {
		t.Fatal("expected Close to mark the page done")
	}
	p.Close() // must not block or panic on a second call
}
