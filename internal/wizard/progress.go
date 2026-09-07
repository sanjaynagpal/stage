package wizard

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

type progressPageData struct {
	Title  string
	Token  string
	Status string
	Log    []string
	Done   bool
}

// progressState holds one progress page's server-side state — a status
// line plus a permanent log, the same Notify/Progress split
// internal/tui and cmd/installer's browser wizard already use, so a rapid
// percentage climb updates one line instead of flooding the log the way a
// plain, undifferentiated Notify would.
type progressState struct {
	title string
	token string

	mu     sync.Mutex
	status string
	log    []string
	done   bool
}

func (s *progressState) notify(msg string) {
	s.mu.Lock()
	s.log = append(s.log, msg)
	s.status = msg
	s.mu.Unlock()
}

func (s *progressState) progress(msg string) {
	s.mu.Lock()
	s.status = msg
	s.mu.Unlock()
}

// finish marks the page done: the next poll renders a terminal state (no
// further auto-refresh, an explicit "you may close this window" notice)
// instead of silently going stale once Close shuts the server down —
// rigger.exe has no way to close the actual browser tab/window it opened
// (see ProgressUI.Close's doc comment), so the page needs to say so itself
// rather than just stop responding.
func (s *progressState) finish() {
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
}

func (s *progressState) snapshot() progressPageData {
	s.mu.Lock()
	defer s.mu.Unlock()
	logCopy := make([]string, len(s.log))
	copy(logCopy, s.log)
	return progressPageData{Title: s.title, Token: s.token, Status: s.status, Log: logCopy, Done: s.done}
}

func (s *progressState) registerRoutes(mux *http.ServeMux) {
	prefix := "/" + s.token
	mux.HandleFunc(prefix+"/", s.handleIndex)
	mux.HandleFunc(prefix+"/screen", s.handleScreen)
	mux.HandleFunc(prefix+"/htmx.min.js", s.handleHTMX)
}

func (s *progressState) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "progress-page", s.snapshot()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *progressState) handleScreen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "progress", s.snapshot()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *progressState) handleHTMX(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(htmxJS)
}

// ProgressUI is a one-way, self-closing progress page — unlike UI
// (cmd/installer's interactive wizard), there's no Confirm/ReadLine to
// block on and no heartbeat-triggered abort: closing the browser tab just
// stops the operator watching, it doesn't cancel whatever's running.
// rigger.exe opens one of these only when it actually needs to fetch a
// JRE or app-jars archive on demand (Dynamic package mode,
// docs/REQUIREMENTS.md §12-14) — never on an ordinary launch where
// everything's already on disk, so this never adds browser-window
// flicker to the common path — and decides for itself when to Close it,
// rather than waiting on the operator the way ShowDoctorReport does.
type ProgressUI struct {
	state *progressState
	srv   *http.Server
	once  sync.Once
}

// ShowProgress starts the local HTTP server and opens the browser,
// returning immediately — it does not block, unlike ShowDoctorReport.
func ShowProgress(title string) (*ProgressUI, error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("wizard: generate session token: %w", err)
	}
	state := &progressState{title: title, token: hex.EncodeToString(tokenBytes)}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("wizard: listen on loopback: %w", err)
	}

	mux := http.NewServeMux()
	state.registerRoutes(mux)
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	pageURL := fmt.Sprintf("http://%s/%s/", ln.Addr().String(), state.token)
	fmt.Println("Opening progress in your browser:", pageURL)
	if err := openBrowser(pageURL); err != nil {
		srv.Close()
		return nil, fmt.Errorf("wizard: open browser: %w", err)
	}

	return &ProgressUI{state: state, srv: srv}, nil
}

// Notify posts a discrete milestone, kept in the page's permanent log.
// Safe on a nil *ProgressUI (a no-op) — matching internal/applog.Logger's
// nil-receiver-safe convention, so cmd/rigger doesn't need a nil check at
// every call site for the common case where no page was ever opened.
func (p *ProgressUI) Notify(msg string) {
	if p == nil {
		return
	}
	p.state.notify(msg)
}

// Progress updates the current status line in place without adding to the
// log — for a rapidly repeated report of the same ongoing operation (a
// download/extract percentage climbing). Safe on a nil *ProgressUI.
func (p *ProgressUI) Progress(msg string) {
	if p == nil {
		return
	}
	p.state.progress(msg)
}

// Close marks the page done — its next poll renders a terminal "you may
// close this window" state and stops auto-refreshing — gives that poll a
// brief window to land, then shuts the server down. It does not, and
// cannot, close the actual browser tab/window itself: for the common case
// (no Edge at its well-known path) openBrowser falls back to the
// operator's default browser via `cmd /c start`, which can just as well
// open the URL as a new tab in an already-running browser session: rigger
// has no way to identify or close only that one tab, and finding and
// killing the *process* it started would risk taking the operator's whole
// browser down with it if it wasn't actually a separate one. Leaving the
// window open with a clear terminal state is the honest alternative to a
// page that silently goes stale. Safe to call more than once (only the
// first call does anything) and safe on a nil *ProgressUI.
func (p *ProgressUI) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.state.finish()
		time.Sleep(1200 * time.Millisecond)
		p.srv.Close()
	})
}
