// Package tui implements cmd/installer's default UI: a polished terminal
// wizard built on Bubble Tea/Lipgloss (docs/REQUIREMENTS.md §23) — a
// deliberate, permanent upgrade over internal/console's plain stdin/stdout
// prompts, not a stand-in for a future GUI the way internal/console
// originally was. It implements the same installerUI shape cmd/installer
// defines (Confirm/RetryCancel/ReadLine/Notify) as blocking calls into a
// single long-lived Bubble Tea program running on its own goroutine: each
// call sends a request message into the program (Program.Send) and blocks
// on a per-call response channel that the program's Update loop populates
// once the operator answers — the same shape internal/wizard's browser UI
// uses, just backed by Bubble Tea's message loop instead of HTTP handlers.
package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// UI drives cmd/installer's terminal wizard.
type UI struct {
	program *tea.Program
	done    chan struct{}
}

// New starts the Bubble Tea program on its own goroutine and returns
// immediately; the returned *UI's methods drive it. If the program ends any
// way other than Close() being called (most notably Ctrl+C), the whole
// process exits — the same effective behavior a plain console prompt has
// today when interrupted, not a regression.
func New(title string) *UI {
	p := tea.NewProgram(model{title: title})
	ui := &UI{program: p, done: make(chan struct{})}
	go func() {
		p.Run()
		select {
		case <-ui.done:
			// Expected shutdown via Close().
		default:
			os.Exit(1)
		}
	}()
	return ui
}

// Close ends the Bubble Tea program normally. Safe to call once, after the
// installer flow is done with the UI.
func (u *UI) Close() {
	close(u.done)
	u.program.Quit()
}

// Confirm shows a y/n prompt and blocks until the operator answers.
func (u *UI) Confirm(prompt string, defaultYes bool) bool {
	resp := make(chan bool, 1)
	u.program.Send(confirmRequest{prompt: prompt, defaultYes: defaultYes, resp: resp})
	return <-resp
}

// ReadLine shows a free-text prompt and blocks until the operator submits a
// line (Enter).
func (u *UI) ReadLine(prompt string) string {
	resp := make(chan string, 1)
	u.program.Send(readLineRequest{prompt: prompt, resp: resp})
	return <-resp
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
		resp := make(chan bool, 1)
		u.program.Send(retryCancelRequest{prompt: prompt, err: err, resp: resp})
		if !<-resp {
			return err
		}
	}
}

// Notify posts a one-way status line (e.g. "Installing...").
func (u *UI) Notify(msg string) {
	u.program.Send(notifyMsg{text: msg})
}

// --- Bubble Tea model ---

type uiMode int

const (
	modeIdle uiMode = iota
	modeConfirm
	modeReadLine
	modeRetryCancel
)

type confirmRequest struct {
	prompt     string
	defaultYes bool
	resp       chan bool
}

type readLineRequest struct {
	prompt string
	resp   chan string
}

type retryCancelRequest struct {
	prompt string
	err    error
	resp   chan bool // true = retry, false = cancel
}

type notifyMsg struct {
	text string
}

type model struct {
	title string
	mode  uiMode

	prompt     string
	defaultYes bool
	errText    string
	input      string
	log        []string

	confirmResp     chan bool
	readLineResp    chan string
	retryCancelResp chan bool
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case confirmRequest:
		m.mode = modeConfirm
		m.prompt = msg.prompt
		m.defaultYes = msg.defaultYes
		m.confirmResp = msg.resp
		return m, nil
	case readLineRequest:
		m.mode = modeReadLine
		m.prompt = msg.prompt
		m.input = ""
		m.readLineResp = msg.resp
		return m, nil
	case retryCancelRequest:
		m.mode = modeRetryCancel
		m.prompt = msg.prompt
		if msg.err != nil {
			m.errText = msg.err.Error()
		}
		m.retryCancelResp = msg.resp
		return m, nil
	case notifyMsg:
		m.log = append(m.log, msg.text)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch m.mode {
	case modeConfirm:
		switch strings.ToLower(msg.String()) {
		case "y":
			m.confirmResp <- true
			m.mode = modeIdle
		case "n":
			m.confirmResp <- false
			m.mode = modeIdle
		case "enter":
			m.confirmResp <- m.defaultYes
			m.mode = modeIdle
		}
	case modeReadLine:
		switch msg.Type {
		case tea.KeyEnter:
			m.readLineResp <- m.input
			m.input = ""
			m.mode = modeIdle
		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		case tea.KeyRunes:
			m.input += string(msg.Runes)
		}
	case modeRetryCancel:
		switch strings.ToLower(msg.String()) {
		case "r", "enter":
			m.retryCancelResp <- true
			m.mode = modeIdle
		case "c":
			m.retryCancelResp <- false
			m.mode = modeIdle
		}
	}
	return m, nil
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Padding(0, 1)
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("39")).Padding(1, 2)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	hintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	logStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

func (m model) View() string {
	var body string
	switch m.mode {
	case modeConfirm:
		hint := "[y/N]"
		if m.defaultYes {
			hint = "[Y/n]"
		}
		body = fmt.Sprintf("%s %s", m.prompt, hintStyle.Render(hint))
	case modeReadLine:
		body = fmt.Sprintf("%s\n> %s█", m.prompt, m.input)
	case modeRetryCancel:
		body = fmt.Sprintf("%s\n%s\n\n%s", m.prompt, errStyle.Render(m.errText), hintStyle.Render("[R]etry / [C]ancel"))
	default:
		if len(m.log) > 0 {
			body = m.log[len(m.log)-1]
		} else {
			body = "Working..."
		}
	}

	view := titleStyle.Render(m.title) + "\n" + boxStyle.Render(body)
	if len(m.log) > 0 {
		view += "\n\n" + logStyle.Render(strings.Join(m.log, "\n"))
	}
	return view + "\n"
}
