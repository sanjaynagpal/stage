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
	width int // terminal width, from tea.WindowSizeMsg; 0 until the first resize event

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
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
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

// Colors are adaptive (light/dark terminal background) rather than single
// ANSI codes, since operators run this over a wide range of terminal themes.
var (
	colorPrimary     = lipgloss.AdaptiveColor{Light: "25", Dark: "39"}
	colorPrimaryText = lipgloss.AdaptiveColor{Light: "235", Dark: "252"}
	colorFaintText   = lipgloss.AdaptiveColor{Light: "243", Dark: "245"}
	colorInverted    = lipgloss.AdaptiveColor{Light: "255", Dark: "235"}
	colorError       = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}
	colorPill        = lipgloss.AdaptiveColor{Light: "254", Dark: "236"}

	headerStyle     = lipgloss.NewStyle().Bold(true).Foreground(colorInverted).Background(colorPrimary).Padding(0, 2)
	boxStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPrimary).Padding(1, 2)
	promptStyle     = lipgloss.NewStyle().Foreground(colorPrimaryText)
	errStyle        = lipgloss.NewStyle().Bold(true).Foreground(colorError)
	cursorStyle     = lipgloss.NewStyle().Foreground(colorInverted).Background(colorPrimary)
	hintKeyStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorPrimaryText).Background(colorPill).Padding(0, 1)
	hintDescStyle   = lipgloss.NewStyle().Foreground(colorFaintText).PaddingRight(2)
	logCurrentStyle = lipgloss.NewStyle().Foreground(colorPrimaryText)
	logFaintStyle   = lipgloss.NewStyle().Foreground(colorFaintText)
)

const (
	maxBoxContentWidth = 72
	minBoxContentWidth = 24
	maxLogLines        = 6
)

// boxContentWidth derives the wizard box's interior width from the terminal
// width, clamped so the box neither overflows a narrow terminal nor sprawls
// absurdly wide on a maximized one. Falls back to a sane default before the
// first tea.WindowSizeMsg arrives.
func boxContentWidth(termWidth int) int {
	if termWidth <= 0 {
		termWidth = 80
	}
	w := termWidth - 8 // border (2) + horizontal padding (4), plus a small margin
	return max(min(w, maxBoxContentWidth), minBoxContentWidth)
}

// renderHints draws a [KEY] description legend: each key as a filled pill,
// its description in faint text, joined on one line.
func renderHints(pairs [][2]string) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, hintKeyStyle.Render(p[0])+hintDescStyle.Render(" "+p[1]))
	}
	return strings.Join(parts, "  ")
}

func (m model) View() string {
	contentWidth := boxContentWidth(m.width)
	// +2 so the header bar's rendered width matches the box's outer width
	// (boxStyle's rounded border adds 1 column on each side beyond Width).
	header := headerStyle.Width(contentWidth + 2).Render(m.title)

	var body string
	var hints [][2]string
	switch m.mode {
	case modeConfirm:
		body = promptStyle.Render(m.prompt)
		def := "no"
		if m.defaultYes {
			def = "yes"
		}
		hints = [][2]string{{"Y", "yes"}, {"N", "no"}, {"Enter", "default: " + def}}
	case modeReadLine:
		body = promptStyle.Render(m.prompt) + "\n\n> " + m.input + cursorStyle.Render(" ")
		hints = [][2]string{{"Enter", "submit"}, {"Backspace", "delete"}}
	case modeRetryCancel:
		body = promptStyle.Render(m.prompt) + "\n" + errStyle.Render("✗ "+m.errText)
		hints = [][2]string{{"R", "retry"}, {"C", "cancel"}}
	default:
		if len(m.log) > 0 {
			body = logCurrentStyle.Render(m.log[len(m.log)-1])
		} else {
			body = logFaintStyle.Render("Working...")
		}
	}
	hints = append(hints, [2]string{"Ctrl+C", "quit"})

	box := boxStyle.Width(contentWidth).Render(body)
	view := header + "\n" + box + "\n" + renderHints(hints)

	// The box body already shows the most recent log line while idle, so the
	// history panel below omits it there to avoid showing it twice.
	logLines := m.log
	if m.mode == modeIdle && len(logLines) > 0 {
		logLines = logLines[:len(logLines)-1]
	}
	if len(logLines) > 0 {
		start := 0
		if n := len(logLines); n > maxLogLines {
			start = n - maxLogLines
		}
		var lines []string
		if start > 0 {
			lines = append(lines, logFaintStyle.Render(fmt.Sprintf("… %d earlier line(s)", start)))
		}
		for i := start; i < len(logLines); i++ {
			style := logFaintStyle
			if i == len(logLines)-1 {
				style = logCurrentStyle
			}
			lines = append(lines, style.Render(logLines[i]))
		}
		view += "\n\n" + strings.Join(lines, "\n")
	}

	return view + "\n"
}
