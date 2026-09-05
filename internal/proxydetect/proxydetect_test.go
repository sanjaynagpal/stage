package proxydetect

import (
	"net/http"
	"testing"
)

func TestParseProxyListBareHostAppliesToAnyScheme(t *testing.T) {
	host, port, ok := parseProxyList("proxy.example.com:8080", "https")
	if !ok || host != "proxy.example.com" || port != "8080" {
		t.Fatalf("parseProxyList = (%q, %q, %v), want (proxy.example.com, 8080, true)", host, port, ok)
	}
}

func TestParseProxyListPerSchemeList(t *testing.T) {
	list := "http=proxy1.example.com:80;https=proxy2.example.com:443"

	host, port, ok := parseProxyList(list, "https")
	if !ok || host != "proxy2.example.com" || port != "443" {
		t.Fatalf("parseProxyList(https) = (%q, %q, %v), want (proxy2.example.com, 443, true)", host, port, ok)
	}

	host, port, ok = parseProxyList(list, "http")
	if !ok || host != "proxy1.example.com" || port != "80" {
		t.Fatalf("parseProxyList(http) = (%q, %q, %v), want (proxy1.example.com, 80, true)", host, port, ok)
	}

	if _, _, ok := parseProxyList(list, "ftp"); ok {
		t.Fatal("expected no match for a scheme not present in the per-scheme list")
	}
}

func TestParseProxyListEmptyReturnsNotOK(t *testing.T) {
	if _, _, ok := parseProxyList("", "http"); ok {
		t.Fatal("expected empty proxy string to report not-ok")
	}
}

func TestMatchesBypassListLocal(t *testing.T) {
	if !matchesBypassList("<local>", "myhost") {
		t.Fatal("expected <local> to match a hostname with no dot")
	}
	if matchesBypassList("<local>", "www.example.com") {
		t.Fatal("expected <local> not to match a fully-qualified hostname")
	}
}

func TestMatchesBypassListWildcard(t *testing.T) {
	if !matchesBypassList("<local>;*.contoso.com", "www.contoso.com") {
		t.Fatal("expected *.contoso.com to match www.contoso.com")
	}
	if matchesBypassList("*.contoso.com", "www.other.com") {
		t.Fatal("expected *.contoso.com not to match an unrelated host")
	}
}

func TestMatchesBypassListCaseInsensitive(t *testing.T) {
	if !matchesBypassList("*.CONTOSO.com", "www.contoso.com") {
		t.Fatal("expected bypass list matching to be case-insensitive")
	}
}

// TestDetectForURLAgainstRealSystemConfig exercises the real WinHTTP calls
// on whatever Windows machine runs the test, matching the precedent set by
// internal/diskspace and internal/procscan for machine-dependent Windows
// APIs: it asserts only that the call succeeds, and logs the result rather
// than asserting a specific value, since actual proxy config varies by
// machine (no proxy is a valid, common result).
func TestDetectForURLAgainstRealSystemConfig(t *testing.T) {
	result, err := DetectForURL("https://example.com")
	if err != nil {
		t.Fatalf("DetectForURL: %v", err)
	}
	t.Logf("detected proxy: %+v", result)
}

func TestClientDirectWhenProxyEmpty(t *testing.T) {
	c := Client(Result{}, 0)
	transport, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Client transport = %T, want *http.Transport", c.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected no Proxy func set on the transport for an empty Result (must not fall back to env vars)")
	}
}

func TestClientRoutesThroughConfiguredProxy(t *testing.T) {
	c := Client(Result{Host: "proxy.example.com", Port: "8080"}, 0)
	transport, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Client transport = %T, want *http.Transport", c.Transport)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("transport.Proxy: %v", err)
	}
	if proxyURL == nil || proxyURL.Host != "proxy.example.com:8080" {
		t.Fatalf("proxyURL = %v, want host proxy.example.com:8080", proxyURL)
	}
}
