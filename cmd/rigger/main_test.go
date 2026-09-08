package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithBearerTokenSetsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	client := withBearerToken(srv.Client(), "demo123")
	if _, err := client.Get(srv.URL); err != nil {
		t.Fatalf("unexpected request error: %v", err)
	}
	if want := "Bearer demo123"; gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

func TestWithBearerTokenPreservesTimeout(t *testing.T) {
	client := &http.Client{Timeout: 42}
	wrapped := withBearerToken(client, "t")
	if wrapped.Timeout != 42 {
		t.Errorf("Timeout = %v, want 42", wrapped.Timeout)
	}
}

func TestParseURLFlagRequiresURL(t *testing.T) {
	if _, err := parseURLFlag("start browser", nil); err == nil {
		t.Fatal("expected error when -url is missing")
	}
}

func TestParseURLFlagReturnsGivenURL(t *testing.T) {
	url, err := parseURLFlag("start browser", []string{"-url", "https://example.com/auth"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://example.com/auth" {
		t.Errorf("url = %q, want %q", url, "https://example.com/auth")
	}
}
