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

// Notify posts a one-way status line (e.g. "Installing..."): it becomes the
// current status line and is also kept in the permanent activity log below
// it. Use this for discrete milestones — each call is a distinct event
// worth a permanent record.
func (u *UI) Notify(msg string) {
	u.program.Send(notifyMsg{text: msg})
}

// Progress updates the current status line in place without adding to the
// permanent activity log. Use this for a rapid, repeated report of the same
// ongoing operation (e.g. a download/extract percentage climbing) — unlike
// Notify, repeated calls don't each leave their own line in the log, since
// they're updates to one event, not a sequence of distinct ones.
func (u *UI) Progress(msg string) {
	u.program.Send(progressMsg{text: msg})
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

type progressMsg struct {
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
	// status is the current single-line status: set by both Notify (which
	// also appends to log) and Progress (which doesn't) — it's what the
	// idle-mode box body shows, decoupled from the permanent log history.
	status string

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
		m.status = msg.text
		return m, nil
	case progressMsg:
		m.status = msg.text
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
	// colorValue highlights a filesystem/registry location named inside a
	// message (see highlightValues) — distinct from colorPrimary, which is
	// already the chrome/accent color (header background, box border), so a
	// value reads as "data" rather than as more chrome.
	colorValue = lipgloss.AdaptiveColor{Light: "30", Dark: "51"}

	headerStyle     = lipgloss.NewStyle().Bold(true).Foreground(colorInverted).Background(colorPrimary).Padding(0, 2)
	boxStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPrimary).Padding(1, 2)
	promptStyle     = lipgloss.NewStyle().Foreground(colorPrimaryText)
	errStyle        = lipgloss.NewStyle().Bold(true).Foreground(colorError)
	cursorStyle     = lipgloss.NewStyle().Foreground(colorInverted).Background(colorPrimary)
	hintKeyStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorPrimaryText).Background(colorPill).Padding(0, 1)
	hintDescStyle   = lipgloss.NewStyle().Foreground(colorFaintText).PaddingRight(2)
	logCurrentStyle = lipgloss.NewStyle().Foreground(colorPrimaryText)
	logFaintStyle   = lipgloss.NewStyle().Foreground(colorFaintText)
	valueStyle      = lipgloss.NewStyle().Foreground(colorValue)
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

// highlightValues renders text in base, except substrings wrapped in
// backticks (the convention cmd/installer's Notify/Progress messages use to
// mark a filesystem path or registry key), which render in valueStyle
// instead — so "Writing registry values to `HKCU\Software\ABC`..." shows
// the location in a distinct color from the surrounding sentence. Segments
// are rendered independently and concatenated (not nested Renders), so
// there's no risk of an inner style's reset code prematurely ending the
// outer one. A message with no backticks (or an odd, unclosed one) renders
// unchanged in base.
func highlightValues(text string, base lipgloss.Style) string {
	parts := strings.Split(text, "`")
	if len(parts) == 1 {
		return base.Render(text)
	}
	var out strings.Builder
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i%2 == 1 {
			out.WriteString(valueStyle.Render(p))
		} else {
			out.WriteString(base.Render(p))
		}
	}
	return out.String()
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
		if m.status != "" {
			body = highlightValues(m.status, logCurrentStyle)
		} else {
			body = logFaintStyle.Render("Working...")
		}
	}
	hints = append(hints, [2]string{"Ctrl+C", "quit"})

	box := boxStyle.Width(contentWidth).Render(body)
	view := header + "\n" + box + "\n" + renderHints(hints)

	// The box body already shows the current status while idle — if that
	// status is also the log's last entry (the common case: no Progress
	// ticks have landed since the last Notify), the history panel below
	// omits it to avoid showing the same line twice. Once Progress calls
	// have moved the status past the last logged milestone, they no longer
	// match, so the full log (that milestone included) shows normally.
	logLines := m.log
	if m.mode == modeIdle && len(logLines) > 0 && logLines[len(logLines)-1] == m.status {
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
			lines = append(lines, highlightValues(logLines[i], style))
		}
		view += "\n\n" + strings.Join(lines, "\n")
	}

	return view + "\n"
}
