package teely

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const lanLoginPath = "/__teely/lan/login"
const lanLogoutPath = "/__teely/lan/logout"
const lanSessionCookie = "__Host-teely-session"
const lanCSRFCookie = "__Host-teely-csrf"
const lanSessionLifetime = 12 * time.Hour
const lanClientIPHeader = "X-Teely-Client-IP"

type lanSession struct {
	host    string
	config  [32]byte
	expires time.Time
}

type lanAuthState struct {
	mu        sync.Mutex
	sessions  map[[32]byte]lanSession
	clients   map[string]lanLoginWindow
	nextPrune time.Time
}

type lanLoginWindow struct {
	expires  time.Time
	attempts int
}

func lanLoginClient(r *http.Request) string {
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(peer)
	// Only our loopback proxy may supply this header. Caddy overwrites it with
	// the socket peer, not X-Forwarded-For or another visitor-controlled value.
	if ip != nil && ip.IsLoopback() && len(r.Header.Values(lanClientIPHeader)) == 1 {
		if client := net.ParseIP(r.Header.Get(lanClientIPHeader)); client != nil {
			return client.String()
		}
	}
	if ip != nil {
		return ip.String()
	}
	return "unknown"
}

func (a *lanAuthState) allowLogin(client string, now time.Time) (bool, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.clients == nil {
		a.clients = make(map[string]lanLoginWindow)
	}
	if !now.Before(a.nextPrune) {
		for ip, window := range a.clients {
			if !now.Before(window.expires) {
				delete(a.clients, ip)
			}
		}
		a.nextPrune = now.Add(time.Minute)
	}
	window, exists := a.clients[client]
	if !exists && len(a.clients) >= 4096 {
		return false, 60 // Bound memory without evicting another client's limit.
	}
	if !now.Before(window.expires) {
		window = lanLoginWindow{expires: now.Add(time.Minute)}
	}
	if window.attempts >= 10 {
		return false, int((window.expires.Sub(now) + time.Second - 1) / time.Second)
	}
	window.attempts++
	a.clients[client] = window
	return true, 0
}

func lanAuthConfig(cfg *Config) [32]byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%+v", cfg.LAN)
	for _, app := range cfg.Apps {
		if app.ShareLAN {
			fmt.Fprintf(&b, "\n%s %s", app.ID, app.Hostname)
		}
	}
	return sha256.Sum256([]byte(b.String()))
}

func (a *lanAuthState) revoke() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions = nil
}

func randomLANToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func lanCookie(name, value string, age int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age}
}

func (a *lanAuthState) authenticated(r *http.Request, cfg [32]byte) bool {
	cookie, err := r.Cookie(lanSessionCookie)
	if err != nil || len(cookie.Value) != 64 {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := sha256.Sum256([]byte(cookie.Value))
	session, ok := a.sessions[key]
	if ok && time.Now().Before(session.expires) && session.config == cfg && session.host == r.Host {
		return true
	}
	delete(a.sessions, key)
	return false
}

func safeLANReturn(value string) string {
	u, err := url.Parse(value)
	if err != nil || !strings.HasPrefix(value, "/") || strings.HasPrefix(u.Path, "//") || strings.ContainsAny(u.Path, "\\\r\n") || u.Host != "" || u.IsAbs() || strings.HasPrefix(u.Path, "/__teely/lan/") {
		return "/"
	}
	return u.RequestURI()
}

// Shared hosts must pass authentication before reaching the normal app handler.
// No forwarded header can turn an arbitrary request into a trusted local route.
func (m *Manager) handleLANRequest(w http.ResponseWriter, r *http.Request) bool {
	cfg := m.Config()
	var app *AppConfig
	for i := range cfg.Apps {
		if strings.EqualFold(r.Host, net.JoinHostPort(cfg.LAN.hostname(cfg.Apps[i]), fmt.Sprint(cfg.LAN.Port))) {
			app = &cfg.Apps[i]
			break
		}
	}
	if app == nil {
		return false
	}
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(peer); ip == nil || !ip.IsLoopback() || !cfg.LAN.Enabled || !app.ShareLAN {
		http.Error(w, "This app is not shared.", http.StatusForbidden)
		return true
	}
	key := lanAuthConfig(&cfg)
	if r.URL.Path == "/__teely/lan/icon.png" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		if r.Method == http.MethodGet {
			_, _ = w.Write(teelyIconPNG)
		}
		return true
	}
	if r.URL.Path == lanLoginPath || r.URL.Path == lanLogoutPath {
		m.serveLANLogin(w, r, &cfg, *app, key)
		return true
	}
	if !m.lanAuth.authenticated(r, key) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet && !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, lanLoginPath+"?next="+url.QueryEscape(safeLANReturn(r.URL.RequestURI())), http.StatusSeeOther)
		} else {
			http.Error(w, "Sign in at "+lanLoginPath+" to access this app.", http.StatusUnauthorized)
		}
		return true
	}
	if (r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions) || strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		if origin := r.Header.Get("Origin"); (origin != "" && origin != cfg.LAN.appURL(*app)) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Cross-origin requests are not allowed.", 403)
			return true
		}
	}
	r.Host = app.Hostname
	r.Header.Del(lanClientIPHeader)
	// Teely credentials must never reach project code. Preserve app-owned cookies
	// and bearer tokens, but drop Basic credentials cached by pre-session clients.
	if scheme, _, _ := strings.Cut(r.Header.Get("Authorization"), " "); strings.EqualFold(scheme, "Basic") {
		r.Header.Del("Authorization")
	}
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, cookie := range cookies {
		if cookie.Name != lanSessionCookie && cookie.Name != lanCSRFCookie {
			r.AddCookie(cookie)
		}
	}
	return false
}

func (m *Manager) serveLANLogin(w http.ResponseWriter, r *http.Request, cfg *Config, app AppConfig, key [32]byte) {
	w.Header().Set("Cache-Control", "no-store")
	// Keep the Origin header on same-origin form submissions for CSRF checks.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", 405)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		m.renderLANLogin(w, r, app, key, "/", "Could not read the form. Please try again.", 400)
		return
	}
	next := safeLANReturn(r.FormValue("next"))
	message := ""
	status := http.StatusOK
	if r.Method == http.MethodPost {
		csrf, err := r.Cookie(lanCSRFCookie)
		if r.Header.Get("Origin") != cfg.LAN.appURL(app) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			m.renderLANLogin(w, r, app, key, next, "Could not verify this request. Please try again from this page.", 403)
			return
		}
		if err != nil || !validLANCSRF(csrf.Value) || subtle.ConstantTimeCompare([]byte(csrf.Value), []byte(r.PostForm.Get("csrf"))) != 1 {
			m.renderLANLogin(w, r, app, key, next, "This form is no longer valid. Please try again below. Cookies must be enabled.", 403)
			return
		}
		if r.URL.Path == lanLogoutPath {
			if cookie, err := r.Cookie(lanSessionCookie); err == nil {
				m.lanAuth.mu.Lock()
				delete(m.lanAuth.sessions, sha256.Sum256([]byte(cookie.Value)))
				m.lanAuth.mu.Unlock()
			}
			http.SetCookie(w, lanCookie(lanSessionCookie, "", -1))
			http.Redirect(w, r, lanLoginPath, 303)
			return
		}
		allowed, retry := m.lanAuth.allowLogin(lanLoginClient(r), time.Now())
		if !allowed {
			w.Header().Set("Retry-After", fmt.Sprint(retry))
			message, status = fmt.Sprintf("Too many sign-in attempts from this address. Try again in %d seconds.", retry), 429
		} else {
			password := r.PostForm.Get("password")
			valid := len(password) <= 72 && bcrypt.CompareHashAndPassword([]byte(cfg.LAN.PasswordHash), []byte(password)) == nil
			if valid && subtle.ConstantTimeCompare([]byte(r.PostForm.Get("username")), []byte(cfg.LAN.Username)) == 1 {
				token, err := randomLANToken()
				if err != nil {
					m.renderLANLogin(w, r, app, key, next, "Could not create a session. Please try again.", 500)
					return
				}
				m.lanAuth.mu.Lock()
				if m.lanAuth.sessions == nil {
					m.lanAuth.sessions = make(map[[32]byte]lanSession)
				}
				for id, session := range m.lanAuth.sessions {
					if time.Now().After(session.expires) || session.config != key {
						delete(m.lanAuth.sessions, id)
					}
				}
				if len(m.lanAuth.sessions) >= 256 {
					m.lanAuth.mu.Unlock()
					m.renderLANLogin(w, r, app, key, next, "Session limit reached. Try again later.", 503)
					return
				}
				if old, err := r.Cookie(lanSessionCookie); err == nil {
					delete(m.lanAuth.sessions, sha256.Sum256([]byte(old.Value)))
				}
				m.lanAuth.sessions[sha256.Sum256([]byte(token))] = lanSession{r.Host, key, time.Now().Add(lanSessionLifetime)}
				m.lanAuth.mu.Unlock()
				http.SetCookie(w, lanCookie(lanSessionCookie, token, int(lanSessionLifetime.Seconds())))
				http.Redirect(w, r, next, 303)
				return
			}
			message, status = "The username or password is incorrect.", 401
		}
	}
	m.renderLANLogin(w, r, app, key, next, message, status)
}

func validLANCSRF(token string) bool {
	decoded, err := hex.DecodeString(token)
	return err == nil && len(decoded) == 32
}

func (m *Manager) renderLANLogin(w http.ResponseWriter, r *http.Request, app AppConfig, key [32]byte, next, message string, status int) {
	// Reuse the browser's token so opening another tab does not invalidate an
	// already displayed form. Refresh its 30-minute cookie lifetime on display.
	csrf := ""
	if cookie, err := r.Cookie(lanCSRFCookie); err == nil && validLANCSRF(cookie.Value) {
		csrf = cookie.Value
	} else {
		var err error
		csrf, err = randomLANToken()
		if err != nil {
			message, status = "Could not create a sign-in form. Please reload and try again.", 500
		}
	}
	if csrf != "" {
		http.SetCookie(w, lanCookie(lanCSRFCookie, csrf, 1800))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = lanLoginTemplate.Execute(w, struct {
		App, Next, CSRF, Error string
		SignedIn               bool
	}{app.Name, next, csrf, message, m.lanAuth.authenticated(r, key)})
}

var lanLoginTemplate = template.Must(template.New("lan-login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Sign in · Teely</title>
<style>
:root{color-scheme:light dark;--bg:#f4f1e8;--card:#fffdf8;--text:#19231b;--muted:#647263;--field:#f3f1e8;--line:#dedfd5;--accent:#547d60;--error:#8b332c}
@media(prefers-color-scheme:dark){:root{--bg:#0d1310;--card:#18221b;--text:#edf3eb;--muted:#a8b8a8;--field:#202c23;--line:#39483d;--accent:#547d60;--error:#ffaba3}}
*{box-sizing:border-box}body{margin:0;min-height:100svh;display:grid;place-items:center;padding:24px;background:var(--bg);color:var(--text);font-family:"SF Pro Text","SF Pro Display","Helvetica Neue",-apple-system,BlinkMacSystemFont,sans-serif}
main{width:100%;max-width:440px;padding:36px;background:var(--card);border:1px solid var(--line);border-radius:24px}
.brand{display:flex;align-items:center;gap:10px;min-width:0;margin:0 0 26px}
.brand-mark{width:32px;height:32px;border-radius:9px;flex:0 0 auto;display:block;box-shadow:0 8px 18px rgba(21,49,31,0.12)}
.brand-name{font-size:24px;line-height:1.05;font-weight:700}
h1{font-size:28px;line-height:1.2;margin:0 0 12px}p{color:var(--muted);line-height:1.5;margin:0 0 24px;overflow-wrap:anywhere}strong{color:var(--text)}form{display:grid;gap:20px}label{display:grid;gap:8px;font-size:12px;font-weight:600;color:var(--muted);line-height:1.2}input{width:100%;min-width:0;padding:13px 14px;border:1px solid var(--line);border-radius:10px;background:var(--field);color:var(--text);font:inherit;font-size:16px}input:focus-visible,button:focus-visible,a:focus-visible{outline:2px solid var(--accent);outline-offset:3px}button,.continue{border:0;border-radius:10px;padding:14px 18px;background:var(--accent);color:white;font:inherit;font-weight:700;cursor:pointer;text-align:center;text-decoration:none}.error{padding:14px;border-radius:10px;background:color-mix(in srgb,var(--error) 10%,transparent);color:var(--error);font-size:14px;margin-bottom:20px} .secondary{background:var(--field);color:var(--text);border:1px solid var(--line)}
@media(max-width:480px){main{padding:28px 22px}}
</style></head><body><main><div class="brand"><img class="brand-mark" src="/__teely/lan/icon.png" alt="Teely icon"><span class="brand-name">Teely</span></div>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
{{if .SignedIn}}<h1>You're signed in</h1><p>Continue to <strong>{{.App}}</strong> or sign out on this browser.</p>
<form method="post" action="/__teely/lan/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><a class="continue" href="{{.Next}}">Open app</a><button class="secondary">Sign out</button></form>
{{else}}<h1>Sign in to continue</h1><p>Access <strong>{{.App}}</strong> using the LAN credentials from Teely Setup.</p>
<form method="post" action="/__teely/lan/login"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="next" value="{{.Next}}">
<label>Username<input name="username" autocomplete="username" required maxlength="64"></label><label>Password<input type="password" name="password" autocomplete="current-password" required maxlength="72"></label><button>Sign in</button></form>
{{end}}
</main></body></html>`))
