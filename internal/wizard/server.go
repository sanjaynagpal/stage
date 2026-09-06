package wizard

import (
	"net/http"
	"time"
)

// registerRoutes wires every route under the per-session token prefix —
// nothing is served without it, the defense against another local
// process/session driving this installer's endpoint (docs/REQUIREMENTS.md
// §23; the same "don't assume localhost is single-tenant" precaution
// internal/proxydetect's own doc comments already apply elsewhere).
func (u *UI) registerRoutes(mux *http.ServeMux) {
	prefix := "/" + u.token
	mux.HandleFunc(prefix+"/", u.handleIndex)
	mux.HandleFunc(prefix+"/screen", u.handleScreen)
	mux.HandleFunc(prefix+"/confirm", u.handleConfirm)
	mux.HandleFunc(prefix+"/readline", u.handleReadLine)
	mux.HandleFunc(prefix+"/retrycancel", u.handleRetryCancel)
	mux.HandleFunc(prefix+"/heartbeat", u.handleHeartbeat)
	mux.HandleFunc(prefix+"/htmx.min.js", u.handleHTMX)
}

func (u *UI) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", u.snapshot()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (u *UI) handleScreen(w http.ResponseWriter, r *http.Request) {
	u.renderFragment(w)
}

func (u *UI) renderFragment(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "content", u.snapshot()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (u *UI) handleConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	baseline := u.currentVersion()
	u.deliverAnswer(r.FormValue("value"))
	u.waitForVersionPast(baseline)
	u.renderFragment(w)
}

func (u *UI) handleReadLine(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	baseline := u.currentVersion()
	u.deliverAnswer(r.FormValue("value"))
	u.waitForVersionPast(baseline)
	u.renderFragment(w)
}

func (u *UI) handleRetryCancel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	baseline := u.currentVersion()
	u.deliverAnswer(r.FormValue("choice"))
	u.waitForVersionPast(baseline)
	u.renderFragment(w)
}

func (u *UI) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	u.heartbeatMu.Lock()
	u.lastHeartbeat = time.Now()
	u.heartbeatMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (u *UI) handleHTMX(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(htmxJS)
}

// deliverAnswer sends v to whichever installerUI call is currently
// blocked. Buffered size 1 plus a non-blocking send: only one call is ever
// outstanding at a time (the installer flow is sequential), but a stray
// duplicate submit (e.g. a double-click) must never block or panic the
// handler goroutine.
func (u *UI) deliverAnswer(v string) {
	select {
	case u.answerCh <- v:
	default:
	}
}
