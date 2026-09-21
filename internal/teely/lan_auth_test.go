package teely

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestLANSessionBoundary(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password-only"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixture()
	cfg.LAN = LANConfig{Enabled: true, Port: 9443, Suffix: "mac", Username: "reader", PasswordHash: string(hash)}
	cfg.Apps[0].ShareLAN, cfg.Apps[1].ShareLAN = true, true
	cfg.Apps[0].Name = `<script>alert(1)</script>`
	m := &Manager{config: cfg}
	origin := cfg.LAN.appURL(cfg.Apps[0])
	request := func(method, path string, form url.Values, cookies ...*http.Cookie) (*httptest.ResponseRecorder, *http.Request, bool) {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(form.Encode()))
		r.RemoteAddr = "127.0.0.1:9000"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handled := m.handleLANRequest(w, r)
		return w, r, handled
	}
	page, _, _ := request("GET", lanLoginPath, nil)
	if !strings.Contains(page.Body.String(), `src="/__teely/lan/icon.png"`) || strings.Contains(page.Body.String(), "Stay signed in") || strings.Contains(page.Body.String(), "Local access on the host Mac") {
		t.Fatal("login branding or simplified copy regressed")
	}
	icon, _, handled := request("GET", "/__teely/lan/icon.png", nil)
	if !handled || icon.Code != 200 || icon.Header().Get("Content-Type") != "image/png" || icon.Body.Len() != len(teelyIconPNG) {
		t.Fatal("login icon is not available before authentication")
	}
	if strings.Contains(page.Body.String(), cfg.Apps[0].Name) || !strings.Contains(page.Body.String(), "&lt;script&gt;") {
		t.Fatal("unescaped app name")
	}
	csrf := page.Result().Cookies()[0]
	if page.Header().Get("Referrer-Policy") != "same-origin" || csrf.MaxAge != 1800 {
		t.Fatal("form origin policy or token lifetime regressed")
	}
	secondTab, _, _ := request("GET", lanLoginPath, nil, csrf)
	if secondTab.Result().Cookies()[0].Value != csrf.Value {
		t.Fatal("opening another login tab invalidated the first form")
	}
	form := url.Values{"username": {"reader"}, "password": {"test-password-only"}, "csrf": {csrf.Value}, "next": {"/nested?q=1"}}
	missing, _, _ := request("POST", lanLoginPath, form)
	if missing.Code != 403 || !strings.Contains(missing.Body.String(), `role="alert"`) || !strings.Contains(missing.Body.String(), `name="csrf"`) || missing.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal("accepted missing CSRF cookie")
	}
	replacement := missing.Result().Cookies()[0]
	form.Set("csrf", replacement.Value)
	retried, _, _ := request("POST", lanLoginPath, form, replacement)
	if retried.Code != 303 {
		t.Fatal("refreshed form could not be submitted after a missing token")
	}
	form.Set("csrf", csrf.Value)
	form.Set("password", "wrong-password")
	failed, _, _ := request("POST", lanLoginPath, form, csrf)
	if failed.Code != 401 || !strings.Contains(failed.Body.String(), "username or password is incorrect") {
		t.Fatal("bad credentials were not rejected")
	}
	form.Set("password", "test-password-only")
	signedIn, _, _ := request("POST", lanLoginPath, form, csrf)
	if signedIn.Code != 303 || signedIn.Header().Get("Location") != "/nested?q=1" {
		t.Fatalf("login: %d %s", signedIn.Code, signedIn.Body.String())
	}
	session := signedIn.Result().Cookies()[0]
	if session.Name != lanSessionCookie || !session.Secure || !session.HttpOnly || session.Domain != "" || session.Path != "/" || session.SameSite != http.SameSiteLaxMode || session.MaxAge != 43200 {
		t.Fatalf("unsafe session attributes: name=%s", session.Name)
	}
	_, forwarded, handled := request("GET", "/nested?q=1", nil, session, csrf, &http.Cookie{Name: "app-cookie", Value: "keep"})
	if handled || forwarded.Host != cfg.Apps[0].Hostname || forwarded.Header.Get("Cookie") != "app-cookie=keep" {
		t.Fatal("authorized route or cookie isolation failed")
	}
	for _, authorization := range []string{"Basic b2xkOmNyZWRlbnRpYWxz", "Bearer app-token"} {
		r := httptest.NewRequest("GET", origin+"/", nil)
		r.RemoteAddr = "127.0.0.1:9000"
		r.Header.Set("Authorization", authorization)
		r.AddCookie(session)
		if m.handleLANRequest(httptest.NewRecorder(), r) {
			t.Fatal("valid session rejected")
		}
		want := authorization
		if strings.HasPrefix(authorization, "Basic ") {
			want = ""
		}
		if r.Header.Get("Authorization") != want {
			t.Fatal("app authorization or cached credential isolation failed")
		}
	}
	r := httptest.NewRequest("GET", cfg.LAN.appURL(cfg.Apps[1])+"/", nil)
	r.RemoteAddr = "127.0.0.1:9000"
	r.AddCookie(session)
	w := httptest.NewRecorder()
	if !m.handleLANRequest(w, r) || w.Code != 401 {
		t.Fatal("session crossed app host boundary")
	}
	for _, path := range []string{"/", "/__teely/register", "/__teely/apps", "/assets/main.js"} {
		w, _, handled := request("GET", path, nil)
		if !handled || w.Code != 401 || w.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("unauthenticated request was not blocked: %s", path)
		}
	}
	r = httptest.NewRequest("GET", origin+"/nested?q=1", nil)
	r.RemoteAddr = "127.0.0.1:9000"
	r.Header.Set("Accept", "text/html")
	w = httptest.NewRecorder()
	if !m.handleLANRequest(w, r) || w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "next=%2Fnested%3Fq%3D1") {
		t.Fatal("document was not redirected to login")
	}
	for _, originValue := range []string{"", "https://evil.example", "null"} {
		r = httptest.NewRequest("POST", origin+lanLoginPath, strings.NewReader(form.Encode()))
		r.RemoteAddr = "127.0.0.1:9000"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", originValue)
		r.AddCookie(csrf)
		w = httptest.NewRecorder()
		m.handleLANRequest(w, r)
		if w.Code != 403 || !strings.Contains(w.Body.String(), `role="alert"`) || strings.Contains(w.Body.String(), "form expired") {
			t.Fatal("accepted untrusted origin")
		}
	}
	logout, _, _ := request("POST", lanLogoutPath, url.Values{"csrf": {csrf.Value}}, csrf, session)
	if logout.Code != 303 {
		t.Fatal("logout failed")
	}
	blocked, _, _ := request("GET", "/", nil, session)
	if blocked.Code != 401 {
		t.Fatal("logged-out session still worked")
	}
	key := sha256.Sum256([]byte(session.Value))
	m.lanAuth.sessions[key] = lanSession{host: strings.TrimPrefix(origin, "https://"), config: lanAuthConfig(cfg), expires: time.Now().Add(-time.Second)}
	blocked, _, _ = request("GET", "/", nil, session)
	if blocked.Code != 401 {
		t.Fatal("expired session still worked")
	}
	m.lanAuth.sessions[key] = lanSession{host: strings.TrimPrefix(origin, "https://"), config: lanAuthConfig(cfg), expires: time.Now().Add(time.Hour)}
	cfg.LAN.Username = "new-user"
	blocked, _, _ = request("GET", "/", nil, session)
	if blocked.Code != 401 {
		t.Fatal("credential change did not invalidate session")
	}
	m.lanAuth.clients = map[string]lanLoginWindow{"127.0.0.1": {expires: time.Now().Add(time.Minute), attempts: 10}}
	limited, _, _ := request("POST", lanLoginPath, form, csrf)
	if limited.Code != 429 || limited.Header().Get("Retry-After") == "" {
		t.Fatal("login was not rate limited")
	}
}

func TestLANLoginLimitPerClient(t *testing.T) {
	var auth lanAuthState
	now := time.Now()
	for i := 0; i < 10; i++ {
		if allowed, _ := auth.allowLogin("192.168.1.10", now); !allowed {
			t.Fatal("client blocked too early")
		}
	}
	if allowed, retry := auth.allowLogin("192.168.1.10", now.Add(20*time.Second)); allowed || retry != 40 {
		t.Fatalf("expected client-specific limit and 40s retry, got %v %d", allowed, retry)
	}
	if allowed, _ := auth.allowLogin("192.168.1.11", now.Add(20*time.Second)); !allowed {
		t.Fatal("one client blocked another")
	}
	if allowed, _ := auth.allowLogin("192.168.1.10", now.Add(time.Minute)); !allowed {
		t.Fatal("limit did not expire")
	}
	auth.allowLogin("192.168.1.12", now.Add(3*time.Minute))
	if len(auth.clients) != 1 {
		t.Fatal("expired clients were not pruned")
	}
}

func TestLANLoginClientAddress(t *testing.T) {
	for _, tt := range []struct{ peer, header, want string }{
		{"127.0.0.1:1000", "192.168.1.10", "192.168.1.10"},
		{"[::1]:1000", "::ffff:192.168.1.10", "192.168.1.10"},
		{"192.168.1.11:1000", "192.168.1.10", "192.168.1.11"},
		{"127.0.0.1:1000", "not-an-ip", "127.0.0.1"},
		{"127.0.0.1:1000", "192.168.1.10, 192.168.1.11", "127.0.0.1"},
	} {
		r := httptest.NewRequest("POST", "https://example.local/", nil)
		r.RemoteAddr = tt.peer
		r.Header.Set(lanClientIPHeader, tt.header)
		r.Header.Set("X-Forwarded-For", "203.0.113.55")
		if got := lanLoginClient(r); got != tt.want {
			t.Fatalf("client address = %q, want %q", got, tt.want)
		}
	}
}

func TestLANReturnPath(t *testing.T) {
	for _, path := range []string{"https://evil.example", "//evil.example", "/\\evil.example", "/%2fevil.example", "/%5cevil.example", "/__teely/lan/login", "\r\nLocation: elsewhere"} {
		if safeLANReturn(path) != "/" {
			t.Fatalf("unsafe redirect allowed: %q", path)
		}
	}
	if safeLANReturn("/folder?q=1") != "/folder?q=1" {
		t.Fatal("valid return path lost")
	}
}
