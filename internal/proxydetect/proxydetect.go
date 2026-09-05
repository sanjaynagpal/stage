// Package proxydetect auto-detects the system/network proxy needed to
// reach a URL, using WinHTTP's PAC-aware APIs, and builds an *http.Client
// that routes through an already-resolved proxy. Detection itself is
// install-time-only work (performed by the installer wizard, which decides
// what gets written to the registry); rigger.exe only ever calls Client
// with the value it already read from the registry, never re-detecting at
// launch (docs/REQUIREMENTS.md §17-18).
//
// golang.org/x/sys/windows has no WinHTTP/WinINET bindings, so this package
// calls winhttp.dll directly via LazyDLL/LazyProc, following the same
// calling idiom the rest of this repo already uses for other Windows APIs
// (see internal/procscan).
package proxydetect

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Result is a resolved proxy, or the zero value meaning "direct
// connection, no proxy".
type Result struct {
	Host string
	Port string
}

// Empty reports whether r represents a direct connection (no proxy).
func (r Result) Empty() bool {
	return r.Host == ""
}

// Client builds an *http.Client that connects through proxy, or directly
// if proxy is the zero value. It deliberately never consults OS/process
// proxy configuration (HTTP_PROXY etc.) — Stage resolves the proxy exactly
// once (at install time) and stores it explicitly, and this must not
// silently reintroduce a second, unmanaged proxy-resolution path.
func Client(proxy Result, timeout time.Duration) *http.Client {
	transport := &http.Transport{}
	if !proxy.Empty() {
		transport.Proxy = http.ProxyURL(&url.URL{
			Scheme: "http",
			Host:   net.JoinHostPort(proxy.Host, proxy.Port),
		})
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// DetectForURL auto-detects the effective proxy Windows would use to reach
// targetURL: it reads the per-user WinHTTP/IE proxy configuration, and if
// that configuration specifies auto-detection or a PAC (proxy
// auto-config) script, resolves the actual proxy for targetURL through
// WinHttpGetProxyForUrl. Falls back to any statically configured proxy,
// then to a direct connection, the same way a browser degrades when
// auto-configuration is unavailable. Install-time use only.
func DetectForURL(targetURL string) (Result, error) {
	raw, err := getIEProxyConfigForCurrentUser()
	if err != nil {
		return Result{}, err
	}
	ie := raw.toGoAndFree()

	if ie.autoDetect || ie.autoConfigURL != "" {
		result, err := getProxyForURLViaAutoDetect(targetURL, ie)
		if err == nil && !result.Empty() {
			return result, nil
		}
		// PAC/auto-detect found nothing usable (or failed outright) — fall
		// through rather than surface the error, matching how a browser
		// degrades when auto-configuration is unavailable.
	}

	if ie.proxy != "" {
		return resolveProxy(ie.proxy, ie.proxyBypass, targetURL)
	}
	return Result{}, nil
}

// resolveProxy picks the host/port applicable to targetURL's scheme out of
// a WinHTTP-style proxy string, honoring the bypass list.
func resolveProxy(proxyStr, bypassList, targetURL string) (Result, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return Result{}, fmt.Errorf("proxydetect: parse target URL %q: %w", targetURL, err)
	}
	if matchesBypassList(bypassList, u.Hostname()) {
		return Result{}, nil
	}
	host, port, ok := parseProxyList(proxyStr, u.Scheme)
	if !ok {
		return Result{}, nil
	}
	return Result{Host: host, Port: port}, nil
}

// parseProxyList parses a WinHTTP-style proxy string — either a bare
// "host:port" (applies to every scheme) or a per-scheme list like
// "http=proxy1:80;https=proxy2:443" — and returns the host/port applicable
// to targetScheme ("http" or "https").
func parseProxyList(proxyStr, targetScheme string) (host, port string, ok bool) {
	proxyStr = strings.TrimSpace(proxyStr)
	if proxyStr == "" {
		return "", "", false
	}
	if !strings.Contains(proxyStr, "=") {
		return splitHostPort(proxyStr)
	}
	for entry := range strings.SplitSeq(proxyStr, ";") {
		scheme, hostport, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(scheme), targetScheme) {
			return splitHostPort(strings.TrimSpace(hostport))
		}
	}
	return "", "", false
}

func splitHostPort(hostport string) (host, port string, ok bool) {
	hostport = strings.TrimPrefix(hostport, "http://")
	hostport = strings.TrimPrefix(hostport, "https://")
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", "", false
	}
	return h, p, true
}

// matchesBypassList reports whether host is covered by a WinHTTP-style
// proxy bypass list (e.g. "<local>;*.contoso.com"). Only "<local>" and
// glob-style wildcard host entries are handled — CIDR-range entries are
// not, since WinHTTP's own bypass syntax primarily uses the former in
// practice.
func matchesBypassList(bypassList, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for rawEntry := range strings.SplitSeq(bypassList, ";") {
		entry := strings.ToLower(strings.TrimSpace(rawEntry))
		if entry == "" {
			continue
		}
		if entry == "<local>" {
			if host == "localhost" || !strings.Contains(host, ".") {
				return true
			}
			continue
		}
		if matched, err := path.Match(entry, host); err == nil && matched {
			return true
		}
	}
	return false
}

// --- WinHTTP syscall layer ---

var (
	winhttp  = windows.NewLazySystemDLL("winhttp.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procWinHttpGetIEProxyConfigForCurrentUser = winhttp.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	procWinHttpOpen                           = winhttp.NewProc("WinHttpOpen")
	procWinHttpGetProxyForUrl                 = winhttp.NewProc("WinHttpGetProxyForUrl")
	procWinHttpCloseHandle                    = winhttp.NewProc("WinHttpCloseHandle")
	procGlobalFree                            = kernel32.NewProc("GlobalFree")
)

const (
	winHTTPAccessTypeNoProxy = 1

	winHTTPAutoProxyAutoDetect = 0x00000001
	winHTTPAutoProxyConfigURL  = 0x00000002
	winHTTPAutoDetectTypeDHCP  = 0x00000001
	winHTTPAutoDetectTypeDNSA  = 0x00000002
)

// ieProxyConfig mirrors WINHTTP_CURRENT_USER_IE_PROXY_CONFIG. Field order
// and the explicit padding field (matching the BOOL-then-pointer alignment
// gap on amd64) must exactly match the C struct's layout.
type ieProxyConfig struct {
	autoDetect    int32 // BOOL
	_             int32 // padding: aligns the following pointer to 8 bytes
	autoConfigURL *uint16
	proxy         *uint16
	proxyBypass   *uint16
}

type ieProxyConfigGo struct {
	autoDetect    bool
	autoConfigURL string
	proxy         string
	proxyBypass   string
}

func (cfg ieProxyConfig) toGoAndFree() ieProxyConfigGo {
	return ieProxyConfigGo{
		autoDetect:    cfg.autoDetect != 0,
		autoConfigURL: utf16PtrToStringAndFree(cfg.autoConfigURL),
		proxy:         utf16PtrToStringAndFree(cfg.proxy),
		proxyBypass:   utf16PtrToStringAndFree(cfg.proxyBypass),
	}
}

// autoProxyOptions mirrors WINHTTP_AUTOPROXY_OPTIONS.
type autoProxyOptions struct {
	flags                 uint32
	autoDetectFlags       uint32
	autoConfigURL         *uint16
	reserved              uintptr // lpvReserved, must be nil
	reservedSize          uint32  // dwReserved, must be 0
	autoLogonIfChallenged int32   // BOOL
}

// proxyInfo mirrors WINHTTP_PROXY_INFO.
type proxyInfo struct {
	accessType  uint32
	_           uint32 // padding: aligns the following pointer to 8 bytes
	proxy       *uint16
	proxyBypass *uint16
}

func getIEProxyConfigForCurrentUser() (ieProxyConfig, error) {
	var cfg ieProxyConfig
	ret, _, callErr := procWinHttpGetIEProxyConfigForCurrentUser.Call(uintptr(unsafe.Pointer(&cfg)))
	if ret == 0 {
		return ieProxyConfig{}, fmt.Errorf("proxydetect: WinHttpGetIEProxyConfigForCurrentUser: %w", callErr)
	}
	return cfg, nil
}

func getProxyForURLViaAutoDetect(targetURL string, ie ieProxyConfigGo) (Result, error) {
	session, err := openSession()
	if err != nil {
		return Result{}, err
	}
	defer closeSession(session)

	urlPtr, err := windows.UTF16PtrFromString(targetURL)
	if err != nil {
		return Result{}, fmt.Errorf("proxydetect: encode target URL: %w", err)
	}

	var opts autoProxyOptions
	if ie.autoConfigURL != "" {
		opts.flags = winHTTPAutoProxyConfigURL
		acPtr, err := windows.UTF16PtrFromString(ie.autoConfigURL)
		if err != nil {
			return Result{}, fmt.Errorf("proxydetect: encode auto-config URL: %w", err)
		}
		opts.autoConfigURL = acPtr
	} else {
		opts.flags = winHTTPAutoProxyAutoDetect
		opts.autoDetectFlags = winHTTPAutoDetectTypeDHCP | winHTTPAutoDetectTypeDNSA
	}

	var info proxyInfo
	ret, _, callErr := procWinHttpGetProxyForUrl.Call(
		session,
		uintptr(unsafe.Pointer(urlPtr)),
		uintptr(unsafe.Pointer(&opts)),
		uintptr(unsafe.Pointer(&info)),
	)
	if ret == 0 {
		return Result{}, fmt.Errorf("proxydetect: WinHttpGetProxyForUrl: %w", callErr)
	}

	proxy := utf16PtrToStringAndFree(info.proxy)
	bypass := utf16PtrToStringAndFree(info.proxyBypass)
	if proxy == "" {
		return Result{}, nil
	}
	return resolveProxy(proxy, bypass, targetURL)
}

func openSession() (uintptr, error) {
	agent, err := windows.UTF16PtrFromString("Stage/1.0")
	if err != nil {
		return 0, fmt.Errorf("proxydetect: encode user agent: %w", err)
	}
	ret, _, callErr := procWinHttpOpen.Call(
		uintptr(unsafe.Pointer(agent)),
		winHTTPAccessTypeNoProxy,
		0, // WINHTTP_NO_PROXY_NAME
		0, // WINHTTP_NO_PROXY_BYPASS
		0, // dwFlags: synchronous
	)
	if ret == 0 {
		return 0, fmt.Errorf("proxydetect: WinHttpOpen: %w", callErr)
	}
	return ret, nil
}

func closeSession(h uintptr) {
	_, _, _ = procWinHttpCloseHandle.Call(h)
}

// utf16PtrToStringAndFree converts a WinHTTP-allocated LPWSTR out-param to
// a Go string and frees the native memory with GlobalFree, as WinHTTP's
// documentation requires for these specific out-params. A nil pointer
// (the field was never set) converts to "".
func utf16PtrToStringAndFree(p *uint16) string {
	if p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	_, _, _ = procGlobalFree.Call(uintptr(unsafe.Pointer(p)))
	return s
}
