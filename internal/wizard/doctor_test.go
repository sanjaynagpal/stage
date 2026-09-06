package wizard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDoctorPageRendersChecks(t *testing.T) {
	session, err := newDoctorSession(DoctorReport{
		AppID: "ABC",
		Checks: []CheckResult{
			{Name: "Registry", Status: "ok", Detail: "Install scope: PerUser"},
			{Name: "Java runtime", Status: "fail", Detail: "javaw.exe not found"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	session.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/" + session.token + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)

	for _, want := range []string{"ABC Diagnostics", "Registry", "Install scope: PerUser", "Java runtime", "javaw.exe not found"} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q; body: %s", want, body)
		}
	}
}

func TestDoctorPageOmitsSupportButtonWithoutEmail(t *testing.T) {
	session, err := newDoctorSession(DoctorReport{AppID: "ABC"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	session.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/" + session.token + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if strings.Contains(readAll(t, resp), "Contact Support") {
		t.Fatal("expected no Contact Support button when SupportEmail is empty")
	}
}

func TestDoctorPageShowsSupportButtonAndLogPath(t *testing.T) {
	session, err := newDoctorSession(DoctorReport{
		AppID:        "ABC",
		SupportEmail: "support@example.com",
		LogZipPath:   `C:\data\diagnostics-20260101-000000.zip`,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	session.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/" + session.token + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)
	if !strings.Contains(body, "Contact Support") {
		t.Fatal("expected a Contact Support button when SupportEmail is set")
	}
	if !strings.Contains(body, `mailto:support@example.com`) {
		t.Fatalf("expected a mailto: link, got: %s", body)
	}
	if !strings.Contains(body, `diagnostics-20260101-000000.zip`) {
		t.Fatalf("expected the log zip path to be shown, got: %s", body)
	}
}

func TestBuildMailtoURLEncodesBody(t *testing.T) {
	got := buildMailtoURL(DoctorReport{
		AppID:        "ABC",
		SupportEmail: "support@example.com",
		Checks:       []CheckResult{{Name: "Registry", Status: "fail", Detail: "missing key"}},
	})
	if !strings.HasPrefix(got, "mailto:support@example.com?") {
		t.Fatalf("got %q, want a mailto: URL prefix", got)
	}
	if strings.Contains(got, " ") || strings.Contains(got, "\n") {
		t.Fatalf("mailto: URL contains raw space/newline, not percent-encoded: %q", got)
	}
	if strings.Contains(got, "+") {
		t.Fatalf("mailto: URL uses '+' for space instead of %%20: %q", got)
	}
}

func TestBuildMailtoURLEmptyWithoutSupportEmail(t *testing.T) {
	if got := buildMailtoURL(DoctorReport{AppID: "ABC"}); got != "" {
		t.Fatalf("got %q, want empty string when SupportEmail is unset", got)
	}
}

func TestDoctorSessionWaitForCloseReturnsAfterHeartbeatTimeout(t *testing.T) {
	session, err := newDoctorSession(DoctorReport{AppID: "ABC"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a heartbeat already long past, so waitForClose returns
	// almost immediately rather than the test waiting out a real timeout.
	session.mu.Lock()
	session.lastHeartbeat = time.Now().Add(-time.Hour)
	session.mu.Unlock()

	done := make(chan struct{})
	go func() {
		session.waitForClose(1 * time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForClose did not return after the heartbeat timeout elapsed")
	}
}
