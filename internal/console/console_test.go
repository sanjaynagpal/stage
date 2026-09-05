package console

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestConfirmDefaultOnBlankInput(t *testing.T) {
	var out bytes.Buffer
	if got := confirm(strings.NewReader("\n"), &out, "Accept?", true); got != true {
		t.Errorf("confirm(blank, defaultYes=true) = %v, want true", got)
	}
	if got := confirm(strings.NewReader("\n"), &out, "Accept?", false); got != false {
		t.Errorf("confirm(blank, defaultYes=false) = %v, want false", got)
	}
}

func TestConfirmParsesYesAndNo(t *testing.T) {
	var out bytes.Buffer
	cases := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"n\n", false},
		{"no\n", false},
		{"garbage\n", false},
	}
	for _, c := range cases {
		if got := confirm(strings.NewReader(c.input), &out, "Accept?", true); got != c.want {
			t.Errorf("confirm(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestConfirmPrintsPromptAndHint(t *testing.T) {
	var out bytes.Buffer
	confirm(strings.NewReader("\n"), &out, "Accept the license?", true)
	if got := out.String(); !strings.Contains(got, "Accept the license?") || !strings.Contains(got, "[Y/n]") {
		t.Errorf("output = %q, want it to contain the prompt and [Y/n] hint", got)
	}
}

func TestRetryCancelReturnsNilOnImmediateSuccess(t *testing.T) {
	var out bytes.Buffer
	calls := 0
	err := retryCancel(strings.NewReader(""), &out, "checking", func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected check to be called exactly once, got %d", calls)
	}
}

func TestRetryCancelRetriesUntilSuccess(t *testing.T) {
	var out bytes.Buffer
	calls := 0
	err := retryCancel(strings.NewReader("r\nr\n"), &out, "checking", func() error {
		calls++
		if calls < 3 {
			return errors.New("still locked")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls (2 retries + final success), got %d", calls)
	}
}

func TestRetryCancelReturnsErrorOnCancel(t *testing.T) {
	var out bytes.Buffer
	wantErr := errors.New("still locked")
	err := retryCancel(strings.NewReader("c\n"), &out, "checking", func() error {
		return wantErr
	})
	if err != wantErr {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestReadLineWithPromptTrimsAndPrintsPrompt(t *testing.T) {
	var out bytes.Buffer
	got := readLineWithPrompt(strings.NewReader("  proxy.example.com:8080  \n"), &out, "Proxy: ")
	if got != "proxy.example.com:8080" {
		t.Errorf("readLineWithPrompt = %q, want trimmed value", got)
	}
	if !strings.Contains(out.String(), "Proxy: ") {
		t.Errorf("expected the prompt to be printed, got %q", out.String())
	}
}
