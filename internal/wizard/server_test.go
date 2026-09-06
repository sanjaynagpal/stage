package wizard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestUI builds a *UI with a real HTTP handler (via httptest) but
// without actually launching a browser, so handler/state logic can be
// exercised directly.
func newTestUI(t *testing.T) (*UI, *httptest.Server) {
	t.Helper()
	st := &state{}
	st.cond = sync.NewCond(&st.mu)
	u := &UI{
		title:         "ABC",
		token:         "testtoken",
		state:         st,
		answerCh:      make(chan string, 1),
		lastHeartbeat: time.Now(),
		closed:        make(chan struct{}),
	}
	mux := http.NewServeMux()
	u.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return u, srv
}

func TestIndexRendersCurrentScreen(t *testing.T) {
	u, srv := newTestUI(t)
	u.setState(screenConfirm, "Accept the license?", false, "")

	resp, err := http.Get(srv.URL + "/testtoken/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)
	if !strings.Contains(body, "Accept the license?") {
		t.Fatalf("expected prompt in body, got: %s", body)
	}
}

func TestConfirmHandlerDeliversAnswerAndWaitsForNextScreen(t *testing.T) {
	u, srv := newTestUI(t)
	u.setState(screenConfirm, "Accept?", false, "")

	// Simulate the installer goroutine: read the answer, then advance to a
	// new screen, exactly like the real Confirm()/next-call sequence.
	go func() {
		v := <-u.answerCh
		if v != "yes" {
			t.Errorf("answerCh = %q, want %q", v, "yes")
		}
		u.setState(screenReadLine, "Proxy override?", false, "")
	}()

	resp, err := http.PostForm(srv.URL+"/testtoken/confirm", url.Values{"value": {"yes"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)
	if !strings.Contains(body, "Proxy override?") {
		t.Fatalf("expected the next screen's prompt in response, got: %s", body)
	}
}

func TestRetryCancelHandler(t *testing.T) {
	u, srv := newTestUI(t)
	u.setState(screenRetryCancel, "Still running", false, "locked")

	go func() {
		v := <-u.answerCh
		if v != "cancel" {
			t.Errorf("answerCh = %q, want %q", v, "cancel")
		}
		u.setState(screenDone, "", false, "")
	}()

	resp, err := http.PostForm(srv.URL+"/testtoken/retrycancel", url.Values{"choice": {"cancel"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)
	if !strings.Contains(body, "Setup has finished") {
		t.Fatalf("expected the done screen, got: %s", body)
	}
}

func TestHeartbeatUpdatesLastSeen(t *testing.T) {
	u, srv := newTestUI(t)
	u.heartbeatMu.Lock()
	u.lastHeartbeat = time.Time{}
	u.heartbeatMu.Unlock()

	resp, err := http.Post(srv.URL+"/testtoken/heartbeat", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	u.heartbeatMu.Lock()
	last := u.lastHeartbeat
	u.heartbeatMu.Unlock()
	if time.Since(last) > time.Second {
		t.Fatalf("lastHeartbeat not updated recently: %v", last)
	}
}

func TestHTMXAssetServed(t *testing.T) {
	_, srv := newTestUI(t)
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

// TestNotifyBetweenPromptsDoesNotStallOrCorruptNextScreen is a regression
// test for a real bug found during manual end-to-end testing: Notify used
// to bump state.version, which made waitForVersionPast (then
// waitForNextScreen) wake up on a log-only change and re-render the
// *previous*, already-answered prompt (its Kind hadn't changed yet),
// leaving the operator stuck looking at a form they'd already submitted.
func TestNotifyBetweenPromptsDoesNotStallOrCorruptNextScreen(t *testing.T) {
	u, srv := newTestUI(t)
	u.setState(screenReadLine, "Proxy override?", false, "")

	go func() {
		answer := <-u.answerCh
		if answer != "" {
			t.Errorf("answerCh = %q, want empty", answer)
		}
		u.setIdle() // what the real ReadLine() does immediately after answering
		u.Notify("Installing...")
		u.Notify("Registering...")
		u.setState(screenConfirm, "Launch ABC now?", true, "")
	}()

	resp, err := http.PostForm(srv.URL+"/testtoken/readline", url.Values{"value": {""}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)
	if strings.Contains(body, `type="text"`) {
		t.Fatalf("response still shows the just-answered readline form: %s", body)
	}

	// The idle screen's own polling (not this handler) is what eventually
	// surfaces the log lines and the next prompt — confirm it does, within
	// a short deadline.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(srv.URL + "/testtoken/screen")
		if err != nil {
			t.Fatal(err)
		}
		body := readAll(t, resp)
		resp.Body.Close()
		if strings.Contains(body, "Launch ABC now?") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("next prompt never appeared via polling; last body: %s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDeliverAnswerNeverBlocksOnDuplicateSubmit(t *testing.T) {
	u, _ := newTestUI(t)
	u.deliverAnswer("yes") // fills the buffered channel
	done := make(chan struct{})
	go func() {
		u.deliverAnswer("yes-again") // must not block even though the channel is full
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deliverAnswer blocked on a duplicate submit")
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return string(buf)
}
