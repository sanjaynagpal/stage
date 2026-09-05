package uriparse

import "testing"

func TestLooksLikeInvocation(t *testing.T) {
	const scheme = "acme-abc"
	cases := map[string]bool{
		"acme-abc://launch?token=abc123": true,
		"ACME-ABC://launch":              true, // scheme match is case-insensitive
		"https://example.com":            false,
		"":                               false,
		"just-a-plain-argument":          false,
		// A Windows drive-letter path is NOT an invocation of our scheme,
		// even though net/url parses "C:" as a scheme of its own.
		`C:\some\path`: false,
	}
	for arg, want := range cases {
		if got := LooksLikeInvocation(arg, scheme); got != want {
			t.Errorf("LooksLikeInvocation(%q, %q) = %v, want %v", arg, scheme, got, want)
		}
	}
}

func TestParseExtractsQueryParams(t *testing.T) {
	params, err := Parse("acme-abc://launch?token=abc123&param1=xyz")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if params["token"] != "abc123" || params["param1"] != "xyz" {
		t.Fatalf("Parse() = %v, want token=abc123, param1=xyz", params)
	}
}

func TestParseNoQueryReturnsEmptyMap(t *testing.T) {
	params, err := Parse("acme-abc://launch")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(params) != 0 {
		t.Fatalf("expected no params, got %v", params)
	}
}

func TestParseFirstValueWinsForRepeatedKey(t *testing.T) {
	params, err := Parse("acme-abc://launch?token=first&token=second")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if params["token"] != "first" {
		t.Fatalf("expected first value to win, got %q", params["token"])
	}
}

func TestParseRejectsInvalidURI(t *testing.T) {
	if _, err := Parse("://%zz"); err == nil {
		t.Fatal("expected error for malformed URI")
	}
}
