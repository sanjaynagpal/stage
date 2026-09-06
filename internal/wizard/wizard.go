// Package wizard implements cmd/installer's -gui UI: a local HTTP server
// (Go html/template + HTMX for interactivity) opened as a browser "app
// window" (docs/REQUIREMENTS.md §23). Unlike the embedded-WebView2 approach
// originally considered, this needs no COM, no Win32 message loop, and no
// native binary dependency — HTMX is plain JS source, checked in like any
// other file.
//
// It implements the same installerUI shape cmd/installer defines
// (Confirm/RetryCancel/ReadLine/Notify) as blocking calls: each renders a
// screen by bumping a version counter on shared state and blocks on an
// answer channel until the matching HTTP handler receives the operator's
// response — the browser-backed twin of internal/tui's Bubble-Tea-backed
// blocking calls.
package wizard

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed assets/htmx.min.js
var htmxJS []byte

var tmpl = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// heartbeatTimeout is how long the page can go without posting a heartbeat
// before the wizard assumes the browser window was closed and aborts —
// the browser-UI equivalent of internal/tui's Ctrl+C-quits behavior.
const heartbeatTimeout = 20 * time.Second

// nextScreenTimeout bounds how long an HTTP handler waits for the
// installer's next screen to become ready before responding anyway with
// whatever's current — a safety valve, not the normal path (the normal
// path is the installer calling the next installerUI method within
// milliseconds of receiving an answer).
const nextScreenTimeout = 30 * time.Second

type screenKind int

const (
	screenNone screenKind = iota
	screenConfirm
	screenReadLine
	screenRetryCancel
	screenDone
)

type screenData struct {
	Title      string
	Token      string
	Kind       screenKind
	Prompt     string
	DefaultYes bool
	ErrText    string
	Log        []string
}

type state struct {
	mu      sync.Mutex
	cond    *sync.Cond
	kind    screenKind
	prompt  string
	defYes  bool
	errText string
	log     []string
	version int
}

// UI drives cmd/installer's browser wizard.
type UI struct {
	title string
	token string
	state *state

	answerCh chan string // single in-flight answer at a time, matching the installer's sequential flow

	heartbeatMu   sync.Mutex
	lastHeartbeat time.Time

	srv    *http.Server
	closed chan struct{}
}

// New generates a per-session token, starts the HTTP server on an ephemeral
// loopback port, launches the browser, and returns once the server is
// accepting connections. It does not wait for the browser to actually load.
func New(title string) (*UI, error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("wizard: generate session token: %w", err)
	}

	st := &state{}
	st.cond = sync.NewCond(&st.mu)

	u := &UI{
		title:         title,
		token:         hex.EncodeToString(tokenBytes),
		state:         st,
		answerCh:      make(chan string, 1),
		lastHeartbeat: time.Now(),
		closed:        make(chan struct{}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("wizard: listen on loopback: %w", err)
	}

	mux := http.NewServeMux()
	u.registerRoutes(mux)
	u.srv = &http.Server{Handler: mux}
	go u.srv.Serve(ln)
	go u.watchdog()

	url := fmt.Sprintf("http://%s/%s/", ln.Addr().String(), u.token)
	fmt.Println("Opening setup in your browser:", url)
	if err := openBrowser(url); err != nil {
		u.srv.Close()
		return nil, fmt.Errorf("wizard: open browser: %w", err)
	}
	return u, nil
}

// Confirm shows a y/n prompt and blocks until the operator answers.
func (u *UI) Confirm(prompt string, defaultYes bool) bool {
	u.setState(screenConfirm, prompt, defaultYes, "")
	answer := <-u.answerCh
	u.setIdle()
	return answer == "yes"
}

// ReadLine shows a free-text prompt and blocks until the operator submits.
func (u *UI) ReadLine(prompt string) string {
	u.setState(screenReadLine, prompt, false, "")
	answer := <-u.answerCh
	u.setIdle()
	return answer
}

// RetryCancel repeatedly calls check, showing its error with a
// Retry/Cancel choice each time it fails, until check succeeds or the
// operator cancels.
func (u *UI) RetryCancel(prompt string, check func() error) error {
	for {
		err := check()
		if err == nil {
			return nil
		}
		u.setState(screenRetryCancel, prompt, false, err.Error())
		choice := <-u.answerCh
		u.setIdle()
		if choice != "retry" {
			return err
		}
	}
}

// Notify posts a one-way status line (e.g. "Installing..."). It
// deliberately does not bump state.version/Broadcast: the idle screen
// (screenNone) polls on its own timer, so a Notify between two prompts is
// picked up on the next poll rather than needing to wake a waiter — waking
// waitForNextScreen here would let a Notify be mistaken for the next real
// screen change and re-render the *previous*, already-answered prompt
// (kind doesn't change, only the log does).
func (u *UI) Notify(msg string) {
	u.state.mu.Lock()
	u.state.log = append(u.state.log, msg)
	u.state.mu.Unlock()
}

// setIdle transitions to the log/progress screen between prompts, which is
// what lets waitForNextScreen return promptly after an answer instead of
// blocking all the way until the *next* interactive prompt (which could be
// seconds away, e.g. across the extraction phase) — the operator sees
// progress in between, via the idle screen's own polling.
func (u *UI) setIdle() {
	u.state.mu.Lock()
	u.state.kind = screenNone
	u.state.version++
	u.state.cond.Broadcast()
	u.state.mu.Unlock()
}

// Close tells the page the install is done and shuts the server down.
func (u *UI) Close() {
	u.setState(screenDone, "", false, "")
	time.Sleep(1500 * time.Millisecond) // let an in-flight poll pick up the "done" screen
	close(u.closed)
	u.srv.Close()
}

func (u *UI) setState(kind screenKind, prompt string, defaultYes bool, errText string) {
	u.state.mu.Lock()
	u.state.kind = kind
	u.state.prompt = prompt
	u.state.defYes = defaultYes
	u.state.errText = errText
	u.state.version++
	u.state.cond.Broadcast()
	u.state.mu.Unlock()
}

func (u *UI) snapshot() screenData {
	u.state.mu.Lock()
	defer u.state.mu.Unlock()
	logCopy := make([]string, len(u.state.log))
	copy(logCopy, u.state.log)
	return screenData{
		Title: u.title, Token: u.token, Kind: u.state.kind,
		Prompt: u.state.prompt, DefaultYes: u.state.defYes,
		ErrText: u.state.errText, Log: logCopy,
	}
}

// currentVersion returns state.version. Callers use this to capture a
// baseline *before* delivering an answer, then pass it to
// waitForVersionPast — capturing the baseline after delivering would race
// against the installer goroutine, which can advance state (via setIdle)
// essentially immediately after receiving the answer.
func (u *UI) currentVersion() int {
	u.state.mu.Lock()
	defer u.state.mu.Unlock()
	return u.state.version
}

// waitForVersionPast blocks until state.version differs from baseline (or
// nextScreenTimeout elapses) before returning, so an HTTP response to an
// answer arrives already showing the next screen instead of requiring a
// separate poll.
func (u *UI) waitForVersionPast(baseline int) {
	u.state.mu.Lock()
	defer u.state.mu.Unlock()
	done := false
	timer := time.AfterFunc(nextScreenTimeout, func() {
		u.state.mu.Lock()
		done = true
		u.state.cond.Broadcast()
		u.state.mu.Unlock()
	})
	defer timer.Stop()
	for u.state.version == baseline && !done {
		u.state.cond.Wait()
	}
}

func (u *UI) watchdog() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-u.closed:
			return
		case <-ticker.C:
			u.heartbeatMu.Lock()
			last := u.lastHeartbeat
			u.heartbeatMu.Unlock()
			if time.Since(last) > heartbeatTimeout {
				fmt.Fprintln(os.Stderr, "wizard: browser window appears to have closed; aborting setup")
				os.Exit(1)
			}
		}
	}
}
