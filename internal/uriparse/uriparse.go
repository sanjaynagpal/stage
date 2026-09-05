// Package uriparse parses a protocol-handler invocation URI (e.g.
// "acme-abc://launch?token=abc123&param1=xyz") received as Rigger's
// argv[1], extracting its query parameters (docs/REQUIREMENTS.md §11).
package uriparse

import (
	"fmt"
	"net/url"
	"strings"
)

// LooksLikeInvocation reports whether arg is an invocation of
// expectedScheme (the app's own registered protocol scheme, from the
// registry), distinguishing "Rigger was launched by its protocol handler"
// from "Rigger was launched from a shortcut" (no arguments) or any other
// unrelated argument. Matching against the app's specific scheme — rather
// than just checking for "some scheme-shaped prefix" — matters because
// net/url happily parses a bare Windows drive letter like "C:\some\path" as
// having scheme "c".
func LooksLikeInvocation(arg, expectedScheme string) bool {
	u, err := url.Parse(arg)
	return err == nil && strings.EqualFold(u.Scheme, expectedScheme)
}

// Parse parses a protocol-handler invocation URI and returns its query
// parameters as a flat map (the first value wins for a repeated key). Only
// params a manifest explicitly declares in ProtocolParams are ever actually
// substituted into JVM options/arguments — see
// internal/manifest.Manifest.Resolve — so this function itself performs no
// allowlisting; that happens at resolution time.
func Parse(raw string) (map[string]string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("uriparse: parse %q: %w", raw, err)
	}
	params := make(map[string]string, len(u.Query()))
	for k, v := range u.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}
	return params, nil
}
