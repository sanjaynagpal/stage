package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func update(m model, msg tea.Msg) model {
	newModel, _ := m.Update(msg)
	return newModel.(model)
}

func key(runes ...rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: runes}
}

func TestConfirmYesNo(t *testing.T) {
	resp := make(chan bool, 1)
	m := update(model{}, confirmRequest{prompt: "ok?", defaultYes: false, resp: resp})
	if m.mode != modeConfirm {
		t.Fatalf("mode = %v, want modeConfirm", m.mode)
	}
	m = update(m, key('y'))
	if got := <-resp; got != true {
		t.Fatalf("got %v, want true", got)
	}
	if m.mode != modeIdle {
		t.Fatalf("mode = %v, want modeIdle after answering", m.mode)
	}
}

func TestConfirmDefaultOnEnter(t *testing.T) {
	resp := make(chan bool, 1)
	m := update(model{}, confirmRequest{prompt: "ok?", defaultYes: true, resp: resp})
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := <-resp; got != true {
		t.Fatalf("got %v, want true (defaultYes)", got)
	}
	_ = m
}

func TestReadLineTypeBackspaceEnter(t *testing.T) {
	resp := make(chan string, 1)
	m := update(model{}, readLineRequest{prompt: "host:port", resp: resp})
	for _, r := range "abc" {
		m = update(m, key(r))
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
	m = update(m, key('d'))
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := <-resp; got != "abd" {
		t.Fatalf("got %q, want %q", got, "abd")
	}
	if m.mode != modeIdle {
		t.Fatalf("mode = %v, want modeIdle", m.mode)
	}
}

func TestRetryCancelChoices(t *testing.T) {
	respRetry := make(chan bool, 1)
	m := update(model{}, retryCancelRequest{prompt: "still running", err: errors.New("locked"), resp: respRetry})
	if m.errText != "locked" {
		t.Fatalf("errText = %q, want %q", m.errText, "locked")
	}
	m = update(m, key('r'))
	if got := <-respRetry; got != true {
		t.Fatalf("got %v, want true (retry)", got)
	}

	respCancel := make(chan bool, 1)
	m = update(model{}, retryCancelRequest{prompt: "still running", err: errors.New("locked"), resp: respCancel})
	m = update(m, key('c'))
	if got := <-respCancel; got != false {
		t.Fatalf("got %v, want false (cancel)", got)
	}
}

func TestNotifyAppendsLog(t *testing.T) {
	m := update(model{}, notifyMsg{text: "Installing..."})
	m = update(m, notifyMsg{text: "Done."})
	if len(m.log) != 2 || m.log[0] != "Installing..." || m.log[1] != "Done." {
		t.Fatalf("log = %v, want [Installing... Done.]", m.log)
	}
}

func TestNotifySetsStatus(t *testing.T) {
	m := update(model{}, notifyMsg{text: "Installing..."})
	if m.status != "Installing..." {
		t.Fatalf("status = %q, want %q", m.status, "Installing...")
	}
}

func TestProgressUpdatesStatusWithoutAppendingLog(t *testing.T) {
	m := update(model{}, notifyMsg{text: "Installing Java runtime 21.0.2+13..."})
	m = update(m, progressMsg{text: "Installing Java runtime 21.0.2+13... 10%"})
	m = update(m, progressMsg{text: "Installing Java runtime 21.0.2+13... 20%"})

	if m.status != "Installing Java runtime 21.0.2+13... 20%" {
		t.Fatalf("status = %q, want the latest Progress call's text", m.status)
	}
	if len(m.log) != 1 || m.log[0] != "Installing Java runtime 21.0.2+13..." {
		t.Fatalf("log = %v, want only the original Notify milestone (Progress calls must not append)", m.log)
	}
}

func TestHighlightValuesStripsBackticksAndPreservesText(t *testing.T) {
	got := highlightValues("Writing registry values to `HKEY_CURRENT_USER\\Software\\ABC`...", logCurrentStyle)
	if strings.Contains(got, "`") {
		t.Fatalf("rendered text %q still contains a literal backtick", got)
	}
	if !strings.Contains(got, "HKEY_CURRENT_USER\\Software\\ABC") {
		t.Fatalf("rendered text %q lost the highlighted value", got)
	}
	if !strings.Contains(got, "Writing registry values to") || !strings.Contains(got, "...") {
		t.Fatalf("rendered text %q lost surrounding plain text", got)
	}
}

func TestHighlightValuesNoBackticksUnchanged(t *testing.T) {
	got := highlightValues("Installation complete.", logCurrentStyle)
	want := logCurrentStyle.Render("Installation complete.")
	if got != want {
		t.Fatalf("highlightValues with no backticks = %q, want %q (identical to a plain Render)", got, want)
	}
}

func TestViewOmitsHistoryDuplicateOfCurrentStatus(t *testing.T) {
	// A bare Notify with no Progress calls since: the box body and the log's
	// last entry are the same text, so the history panel below must not
	// repeat it.
	m := update(model{}, notifyMsg{text: "Writing registry values..."})
	view := m.View()
	if got := strings.Count(view, "Writing registry values..."); got != 1 {
		t.Fatalf("\"Writing registry values...\" appears %d times in the view, want exactly 1 (no duplicate in the history panel)", got)
	}
}

func TestViewShowsMilestoneAndLiveProgressSeparately(t *testing.T) {
	// Once a Progress call has moved the status past the last Notify
	// milestone, that milestone must still appear in the history panel
	// (it's no longer redundant with the box body) on its own line,
	// distinct from the box body's live-percentage line.
	m := update(model{}, notifyMsg{text: "Installing Java runtime 21.0.2+13..."})
	m = update(m, progressMsg{text: "Installing Java runtime 21.0.2+13... 42%"})

	var sawProgressLine, sawMilestoneOnlyLine bool
	for l := range strings.SplitSeq(m.View(), "\n") {
		hasMilestone := strings.Contains(l, "Installing Java runtime 21.0.2+13...")
		hasPct := strings.Contains(l, "42%")
		sawProgressLine = sawProgressLine || (hasMilestone && hasPct)
		sawMilestoneOnlyLine = sawMilestoneOnlyLine || (hasMilestone && !hasPct)
	}
	if !sawProgressLine {
		t.Fatal("expected the box body to show the live progress percentage")
	}
	if !sawMilestoneOnlyLine {
		t.Fatal("expected the history panel to still show the original milestone line separately from the live progress percentage")
	}
}

func TestCtrlCQuits(t *testing.T) {
	m := model{mode: modeConfirm}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected a tea.Quit command on Ctrl+C")
	}
}
