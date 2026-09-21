package teely

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func fixture() *Config {
	return &Config{ListenAddress: "127.0.0.1:8417", AdminHostname: "teely.localhost", Apps: []AppConfig{
		{ID: "one", Hostname: "one.localhost", Port: 3001}, {ID: "two", Hostname: "two.localhost", Port: 3002},
	}}
}

func TestLANValidation(t *testing.T) {
	oldAvailable := lanAddressAvailable
	lanAddressAvailable = func(string) bool { return true }
	defer func() { lanAddressAvailable = oldAvailable }()
	cfg := fixture()
	cfg.LAN = LANConfig{Enabled: true, Address: "192.168.1.20", Port: 9443, Suffix: "mac", Username: "reader", PasswordHash: "$2a$10$" + strings.Repeat("a", 53)}
	cfg.Apps[0].ShareLAN = true
	if err := validateLAN(cfg); err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{80, 443, 2019, 3001, 8417, 65536} {
		copy := cloneConfig(cfg)
		copy.LAN.Port = port
		if err := validateLAN(copy); err == nil {
			t.Fatalf("accepted port %d", port)
		}
	}
	for _, ip := range []string{"0.0.0.0", "127.0.0.1", "8.8.8.8", "::1"} {
		copy := cloneConfig(cfg)
		copy.LAN.Address = ip
		if err := validateLAN(copy); err == nil {
			t.Fatalf("accepted address %s", ip)
		}
	}
	copy := cloneConfig(cfg)
	copy.Apps[0].Hostname = cfg.AdminHostname
	if validateLAN(copy) == nil {
		t.Fatal("accepted admin hostname")
	}
	copy = cloneConfig(cfg)
	copy.LAN.PasswordHash = ""
	if validateLAN(copy) == nil {
		t.Fatal("accepted missing credentials")
	}
	m := &Manager{config: cfg}
	if strings.Contains(m.CaddySnippet(), cfg.LAN.PasswordHash) {
		t.Fatal("public snippet leaked hash")
	}
	lanAddressAvailable = func(string) bool { return false }
	if text := m.caddySnippetLocked(); strings.Contains(text, "bind 192.168.1.20") || !strings.Contains(text, "one.localhost") {
		t.Fatal("missing LAN address must not block local routes")
	}
	cfg.LAN.Enabled = false
	if strings.Contains(m.caddySnippetLocked(), "one-mac.local") {
		t.Fatal("disabled sharing still routed")
	}
}

func TestLANCaddyRouting(t *testing.T) {
	oldAvailable := lanAddressAvailable
	lanAddressAvailable = func(string) bool { return true }
	defer func() { lanAddressAvailable = oldAvailable }()
	binary := os.Getenv("TEELY_TEST_CADDY")
	if binary == "" {
		t.Skip("set TEELY_TEST_CADDY to an existing Caddy binary")
	}
	const password = "preview-test-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	var m *Manager
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(lanClientIPHeader) != "127.0.0.1" {
			t.Error("Caddy did not overwrite the client IP with its socket peer")
		}
		if m.handleLANRequest(w, r) {
			return
		}
		if strings.Contains(r.Header.Get("Cookie"), "__Host-teely-") {
			t.Error("session credentials reached upstream")
		}
		if r.Header.Get(lanClientIPHeader) != "" {
			t.Error("internal client header reached project code")
		}
		if r.Header.Get("Upgrade") == "websocket" {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			rw.Flush()
			return
		}
		fmt.Fprintf(w, "%s %s", r.Host, r.URL.RequestURI())
	}))
	defer upstream.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cfg := fixture()
	cfg.ListenAddress = strings.TrimPrefix(upstream.URL, "http://")

	cfg.LAN = LANConfig{Enabled: true, Address: "192.168.1.20", Port: port, Suffix: "mac", Username: "reader", PasswordHash: strings.TrimSpace(string(hash))}
	for i := range cfg.Apps {
		cfg.Apps[i].ShareLAN = true
	}
	cfg.AdminHostname = ""
	m = &Manager{config: cfg}
	hosts := []string{cfg.LAN.hostname(cfg.Apps[0]), cfg.LAN.hostname(cfg.Apps[1])}
	sparePort := func() int {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	localPort, httpPort, adminPort := sparePort(), sparePort(), sparePort()
	build := func() string {
		text := m.caddySnippetLocked()
		text = strings.Replace(text, "{\n", fmt.Sprintf("{\n admin 127.0.0.1:%d\n https_port %d\n http_port %d\n", adminPort, localPort, httpPort), 1)
		return strings.ReplaceAll(text, "bind 192.168.1.20", "bind 127.0.0.1")
	}
	config := build()
	dir := t.TempDir()
	path := filepath.Join(dir, "Caddyfile")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "run", "--config", path)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dir, "XDG_CONFIG_HOME="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cmd.Wait() }()
	pool := x509.NewCertPool()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(dir, "caddy", "pki", "authorities", "local", "root.crt"))
		if pool.AppendCertsFromPEM(data) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no test CA")
		}
		time.Sleep(50 * time.Millisecond)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i, host := range hosts {
		target := fmt.Sprintf("https://%s:%d/assets/main.js?q=1", host, port)
		resp, err := client.Get(target)
		for err != nil && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			resp, err = client.Get(target)
		}
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatal(resp.StatusCode)
		}
		origin := fmt.Sprintf("https://%s:%d", host, port)
		login, err := client.Get(origin + lanLoginPath)
		if err != nil {
			t.Fatal(err)
		}
		page, _ := io.ReadAll(login.Body)
		login.Body.Close()
		csrf := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindSubmatch(page)
		if len(csrf) != 2 {
			t.Fatalf("missing login form: %s", page)
		}
		form := url.Values{"csrf": {string(csrf[1])}, "username": {"reader"}, "password": {password}, "next": {"/assets/main.js?q=1"}}
		post, _ := http.NewRequest("POST", origin+lanLoginPath, strings.NewReader(form.Encode()))
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		post.Header.Set("Origin", origin)
		post.Header.Set(lanClientIPHeader, "203.0.113.55")
		post.Header.Set("X-Forwarded-For", "203.0.113.56")
		for _, cookie := range login.Cookies() {
			post.AddCookie(cookie)
		}
		signedIn, err := client.Do(post)
		if err != nil {
			t.Fatal(err)
		}
		signedIn.Body.Close()
		if signedIn.StatusCode != 303 || signedIn.Header.Get("Location") != "/assets/main.js?q=1" {
			t.Fatalf("sign in: %d", signedIn.StatusCode)
		}
		r, _ := http.NewRequest("GET", target, nil)
		for _, cookie := range signedIn.Cookies() {
			r.AddCookie(cookie)
		}
		resp, err = client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != cfg.Apps[i].Hostname+" /assets/main.js?q=1" || resp.StatusCode != 200 {
			t.Fatalf("route: %d %s", resp.StatusCode, body)
		}
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		resp, err = client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 101 {
			t.Fatal(resp.StatusCode)
		}
	}
	reload := func() {
		t.Helper()
		if err := os.WriteFile(path, []byte(build()), 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, "reload", "--config", path, "--address", fmt.Sprintf("127.0.0.1:%d", adminPort))
		command.Env = cmd.Env
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("reload: %v %s", err, out)
		}
		transport.CloseIdleConnections()
	}
	// Removing one app leaves the other route intact, with the same shared port.
	cfg.Apps = cfg.Apps[1:]
	reload()
	r, _ := http.NewRequest("GET", fmt.Sprintf("https://%s:%d/", hosts[0], port), nil)
	r.SetBasicAuth("reader", password)
	resp, err := client.Do(r)
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), "one.localhost") {
			t.Fatal("deleted app still routed")
		}
	}
	resp, err = client.Get(fmt.Sprintf("https://%s:%d/", hosts[1], port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	oldPort := port
	port = sparePort()
	cfg.LAN.Port = port
	reload()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(oldPort)), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("old shared port remained open")
	}
	resp, err = client.Get(fmt.Sprintf("https://%s:%d/", hosts[1], port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("new port is not protected")
	}
	cfg.Apps[0].ShareLAN = false
	reload()
	resp, err = client.Get(fmt.Sprintf("https://%s:%d/", hosts[1], port))
	if err == nil {
		resp.Body.Close()
		t.Fatal("disabled final shared listener remained open")
	}
}

func TestLANUIAndAdminBoundary(t *testing.T) {
	cfg := fixture()
	cfg.LAN = LANConfig{Enabled: true, Address: "192.168.1.20", Port: 9443, Suffix: "mac", Username: "reader", PasswordHash: "$2a$10$" + strings.Repeat("a", 53)}
	m := &Manager{config: cfg}
	for _, path := range []string{"/", "/__teely/apps", "/__teely/lan/certificate"} {
		r := httptest.NewRequest("GET", "https://teely.localhost"+path, nil)
		r.RemoteAddr = "192.168.1.20:1234"
		w := httptest.NewRecorder()
		m.handleAdmin(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	r := httptest.NewRequest("POST", "https://teely.localhost/__teely/lan/save", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	m.handleAdmin(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	renderDashboard(w, dashboardView{Config: *cfg, ShowModal: true, ShowLANDetails: true})
	for _, s := range []string{"LAN Access", "Share on LAN", "Shared HTTPS Port", "setup-lan-detail-body"} {
		if !strings.Contains(w.Body.String(), s) {
			t.Fatalf("missing %s", s)
		}
	}
	if strings.Contains(w.Body.String(), cfg.LAN.PasswordHash) {
		t.Fatal("hash leaked into dashboard")
	}
}

func TestLANSaveDisableAndPasswordRetention(t *testing.T) {
	dir := t.TempDir()
	cfg := fixture()
	cfg.Caddy = CaddyConfig{BinaryPath: filepath.Join(dir, "missing"), CaddyfilePath: filepath.Join(dir, "Caddyfile")}
	cfg.LAN = LANConfig{Enabled: true, PasswordHash: "$2a$10$" + strings.Repeat("a", 53)}
	m := &Manager{config: cfg, configPath: filepath.Join(dir, "config.json"), runtimes: map[string]*appRuntime{}}
	form := url.Values{"disable": {"yes"}}
	r := httptest.NewRequest("POST", "/__teely/lan/save", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	m.handleLANSave(w, r)
	if w.Code != 303 || m.config.LAN.Enabled || m.config.LAN.PasswordHash != cfg.LAN.PasswordHash {
		t.Fatal("disable lost saved credentials or did not disable")
	}
	info, err := os.Stat(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}

func TestBonjourReconcile(t *testing.T) {
	oldAvailable := lanAddressAvailable
	lanAddressAvailable = func(string) bool { return true }
	defer func() { lanAddressAvailable = oldAvailable }()
	old := bonjourCommand
	defer func() { bonjourCommand = old }()
	started := make(chan []string, 10)
	bonjourCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		started <- args
		return exec.CommandContext(ctx, "/bin/sleep", "60")
	}
	cfg := fixture()
	cfg.LAN = LANConfig{Enabled: true, Address: "192.168.1.20", Port: 9443, Suffix: "mac"}
	cfg.Apps[0].ShareLAN = true
	m := &Manager{config: cfg, runtimes: map[string]*appRuntime{}}
	defer m.Close()
	m.StartLAN()
	select {
	case args := <-started:
		if !strings.Contains(strings.Join(args, " "), "one-mac.local") {
			t.Fatal(args)
		}
	case <-time.After(time.Second):
		t.Fatal("not advertised")
	}
	m.mu.Lock()
	m.config.Apps[1].ShareLAN = true
	m.reconcileBonjourLocked()
	m.mu.Unlock()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("new app not advertised")
		}
	}
	m.mu.Lock()
	m.config.LAN.Enabled = false
	m.reconcileBonjourLocked()
	m.mu.Unlock()
	select {
	case <-started:
		t.Fatal("advertised while disabled")
	case <-time.After(20 * time.Millisecond):
	}
}
