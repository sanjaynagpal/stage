package tui

import (
	"errors"
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

func TestCtrlCQuits(t *testing.T) {
	m := model{mode: modeConfirm}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected a tea.Quit command on Ctrl+C")
	}
}
