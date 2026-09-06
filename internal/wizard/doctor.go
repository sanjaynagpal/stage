package wizard

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CheckResult is one diagnostic check's outcome for rendering. Deliberately
// decoupled from internal/doctor.Check (its Status is a plain string, not
// that package's Status type) so this package — a generic local-browser-UI
// toolkit — doesn't need to import a rigger-specific package; callers
// convert. Status must be "ok", "warn", or "fail", matching the CSS
// classes in templates/doctor.html.
type CheckResult struct {
	Name, Status, Detail string
}

// DoctorReport is everything rigger.exe's --doctor mode needs rendered.
type DoctorReport struct {
	AppID        string
	Checks       []CheckResult
	LogZipPath   string // "" if log collection wasn't run or found nothing
	SupportEmail string // "" disables the "Contact Support" button
}

type doctorPageData struct {
	AppID      string
	Token      string
	Checks     []CheckResult
	LogZipPath string
	MailtoURL  string // "" disables the button; pre-built here, not in the template, since mailto: query encoding needs real percent-encoding
}

// doctorSession holds one --doctor page's server-side state: just enough
// to render the (already-fully-computed) report and detect the browser
// closing via a heartbeat, mirroring UI's shape but far simpler — there's
// no multi-screen flow, just one page and one background wait.
type doctorSession struct {
	token string
	data  doctorPageData

	mu            sync.Mutex
	lastHeartbeat time.Time
}

func newDoctorSession(report DoctorReport) (*doctorSession, error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("wizard: generate session token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	return &doctorSession{
		token: token,
		data: doctorPageData{
			AppID:      report.AppID,
			Token:      token,
			Checks:     report.Checks,
			LogZipPath: report.LogZipPath,
			MailtoURL:  buildMailtoURL(report),
		},
		lastHeartbeat: time.Now(),
	}, nil
}

func (d *doctorSession) registerRoutes(mux *http.ServeMux) {
	prefix := "/" + d.token
	mux.HandleFunc(prefix+"/", d.handleIndex)
	mux.HandleFunc(prefix+"/heartbeat", d.handleHeartbeat)
	mux.HandleFunc(prefix+"/htmx.min.js", d.handleHTMX)
}

func (d *doctorSession) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "doctor.html", d.data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (d *doctorSession) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.lastHeartbeat = time.Now()
	d.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (d *doctorSession) handleHTMX(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(htmxJS)
}

// waitForClose blocks until no heartbeat has arrived for heartbeatTimeout,
// polling every 2s.
func (d *doctorSession) waitForClose(heartbeatTimeout time.Duration) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		d.mu.Lock()
		last := d.lastHeartbeat
		d.mu.Unlock()
		if time.Since(last) > heartbeatTimeout {
			return
		}
	}
}

// ShowDoctorReport opens a local browser page rendering report and blocks
// until the operator closes it. Unlike the installer wizard's heartbeat
// timeout (which aborts an in-progress install), the browser closing here
// is doctor mode's normal, expected end — this returns nil rather than
// exiting the process (docs/REQUIREMENTS.md §24).
func ShowDoctorReport(report DoctorReport) error {
	session, err := newDoctorSession(report)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("wizard: listen on loopback: %w", err)
	}

	mux := http.NewServeMux()
	session.registerRoutes(mux)
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	pageURL := fmt.Sprintf("http://%s/%s/", ln.Addr().String(), session.token)
	fmt.Println("Opening diagnostics in your browser:", pageURL)
	if err := openBrowser(pageURL); err != nil {
		return fmt.Errorf("wizard: open browser: %w", err)
	}

	session.waitForClose(20 * time.Second)
	return nil
}

// buildMailtoURL constructs a mailto: link pre-filled with a diagnostic
// summary and the log bundle's path (docs/REQUIREMENTS.md §20: "the log
// file's path," not an attachment — mailto: can't carry one). Returns ""
// if report.SupportEmail is empty, disabling the button.
func buildMailtoURL(report DoctorReport) string {
	if report.SupportEmail == "" {
		return ""
	}
	subject := fmt.Sprintf("%s Diagnostics", report.AppID)

	var body strings.Builder
	if report.LogZipPath != "" {
		fmt.Fprintf(&body, "Diagnostic logs: %s\n\n", report.LogZipPath)
	}
	body.WriteString("Checks:\n")
	for _, c := range report.Checks {
		fmt.Fprintf(&body, "- [%s] %s: %s\n", strings.ToUpper(c.Status), c.Name, c.Detail)
	}

	// url.Values.Encode() percent-encodes with "+" for space
	// (application/x-www-form-urlencoded); RFC 6068 mailto query strings
	// want %20, not "+" — most clients tolerate either, but this is cheap
	// to get right.
	values := url.Values{"subject": {subject}, "body": {body.String()}}
	query := strings.ReplaceAll(values.Encode(), "+", "%20")
	return "mailto:" + report.SupportEmail + "?" + query
}
