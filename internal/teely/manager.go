package teely

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type AppStatus string

const (
	StatusStopped  AppStatus = "stopped"
	StatusStarting AppStatus = "starting"
	StatusRunning  AppStatus = "running"
	StatusError    AppStatus = "error"
)

type AppState struct {
	Config       AppConfig     `json:"config"`
	Status       AppStatus     `json:"status"`
	PID          int           `json:"pid,omitempty"`
	LastUsedAt   *time.Time    `json:"last_used_at,omitempty"`
	StartedAt    *time.Time    `json:"started_at,omitempty"`
	LastError    string        `json:"last_error,omitempty"`
	ExitCode     *int          `json:"exit_code,omitempty"`
	LogTail      string        `json:"log_tail,omitempty"`
	Ready        bool          `json:"ready"`
	ProxyTarget  string        `json:"proxy_target,omitempty"`
	PortConflict *PortConflict `json:"port_conflict,omitempty"`
}

type PortConflict struct {
	PID            int    `json:"pid"`
	Command        string `json:"command"`
	Address        string `json:"address,omitempty"`
	ManagedByTeely bool   `json:"managed_by_teely,omitempty"`
}

type SetupCheck struct {
	ID          string
	Label       string
	Detail      string
	Installed   bool
	StatusLabel string
	StatusClass string
	Action      string
	ActionLabel string
}

type SetupState struct {
	Checks []SetupCheck
	AI     AISetupState
}

type Manager struct {
	configPath string

	mu             sync.RWMutex
	config         *Config
	runtimes       map[string]*appRuntime
	hostToApp      map[string]string
	httpClient     *http.Client
	aiModelOptions map[string][]AIModelOption
	aiModelErrors  map[string]string
}

type appRuntime struct {
	cfg AppConfig

	mu           sync.Mutex
	status       AppStatus
	adopted      bool
	managedPID   int
	stopping     bool
	cmd          *exec.Cmd
	cancel       context.CancelFunc
	waitDone     chan struct{}
	startedAt    *time.Time
	lastUsedAt   *time.Time
	lastError    string
	exitCode     *int
	ready        bool
	logs         *logBuffer
	proxy        *httputil.ReverseProxy
	portConflict *PortConflict
}

func NewManager(configPath string) (*Manager, error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}

	m := &Manager{
		configPath:     configPath,
		config:         cfg,
		runtimes:       map[string]*appRuntime{},
		hostToApp:      map[string]string{},
		httpClient:     &http.Client{Timeout: 2 * time.Second},
		aiModelOptions: map[string][]AIModelOption{},
		aiModelErrors:  map[string]string{},
	}
	m.rebuildFromConfigLocked()
	go m.idleLoop()
	return m, nil
}

func (m *Manager) Close() {
	for _, rt := range m.runtimes {
		_ = rt.stop("teely shutting down")
	}
}

func (m *Manager) Config() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return *m.config
}

func (m *Manager) ListApps() []AppState {
	m.mu.RLock()
	apps := append([]AppConfig(nil), m.config.Apps...)
	runtimes := make(map[string]*appRuntime, len(m.runtimes))
	for id, rt := range m.runtimes {
		runtimes[id] = rt
	}
	client := m.httpClient
	m.mu.RUnlock()

	out := make([]AppState, 0, len(apps))
	for _, app := range apps {
		if rt := runtimes[app.ID]; rt != nil {
			rt.refreshObservedState(client)
			out = append(out, rt.snapshot())
		}
	}
	return out
}

func (m *Manager) GetAppByID(id string) (AppState, bool) {
	m.mu.RLock()
	rt, ok := m.runtimes[id]
	client := m.httpClient
	if !ok {
		m.mu.RUnlock()
		return AppState{}, false
	}
	m.mu.RUnlock()
	rt.refreshObservedState(client)
	return rt.snapshot(), true
}

func (m *Manager) FindByHost(host string) (AppState, bool) {
	host = normalizeHost(host)
	m.mu.RLock()
	id, ok := m.hostToApp[host]
	client := m.httpClient
	if !ok {
		m.mu.RUnlock()
		return AppState{}, false
	}
	rt := m.runtimes[id]
	m.mu.RUnlock()
	rt.refreshObservedState(client)
	return rt.snapshot(), true
}

func (m *Manager) HandleAppRequest(w http.ResponseWriter, r *http.Request) {
	host := normalizeHost(r.Host)
	m.mu.RLock()
	id, ok := m.hostToApp[host]
	rt := m.runtimes[id]
	adminHost := strings.ToLower(m.config.AdminHostname)
	m.mu.RUnlock()

	if host == adminHost || isDirectAdminHost(host) {
		m.handleAdmin(w, r)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	if err := rt.ensureStarted(m.httpClient, false); err != nil {
		renderAppRequestError(w, r, rt.snapshot(), err)
		return
	}

	if !rt.isReady() {
		if !isDocumentRequest(r) {
			ready, waitErr := rt.waitForReady(r.Context(), m.httpClient, backgroundRequestHoldTimeout(rt.cfg))
			if waitErr != nil {
				renderAppRequestError(w, r, rt.snapshot(), waitErr)
				return
			}
			if ready {
				rt.markUsed()
				rt.proxy.ServeHTTP(w, r)
				return
			}
			state := rt.snapshot()
			if isAPIRequest(r) {
				renderStartingAPI(w, state)
				return
			}
			renderStartingUnavailable(w)
			return
		}
		renderStartupPage(w, rt.snapshot())
		return
	}

	rt.markUsed()
	rt.proxy.ServeHTTP(w, r)
}

func (m *Manager) StartApp(id string) error {
	m.mu.RLock()
	rt, ok := m.runtimes[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown app %q", id)
	}
	return rt.ensureStarted(m.httpClient, true)
}

func (m *Manager) StopApp(id string) error {
	m.mu.RLock()
	rt, ok := m.runtimes[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown app %q", id)
	}
	return rt.stop("stopped from Teely UI")
}

func (m *Manager) RestartApp(id string) error {
	if err := m.StopApp(id); err != nil && !errors.Is(err, errAlreadyStopped) {
		return err
	}
	return m.StartApp(id)
}

func (m *Manager) DeleteApp(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	index := -1
	deletedHost := ""
	for i := range m.config.Apps {
		if m.config.Apps[i].ID == id {
			index = i
			deletedHost = strings.TrimSpace(m.config.Apps[i].Hostname)
			break
		}
	}
	if index == -1 {
		return fmt.Errorf("unknown app %q", id)
	}

	if rt, ok := m.runtimes[id]; ok {
		if err := rt.stop("deleted from Teely UI"); err != nil && !errors.Is(err, errAlreadyStopped) {
			return err
		}
	}

	next := cloneConfig(m.config)
	next.Apps = append(next.Apps[:index], next.Apps[index+1:]...)
	if err := m.commitConfigLocked(next); err != nil {
		return err
	}
	if deletedHost != "" {
		if err := removeLocalCaddyCertCache(deletedHost); err != nil {
			log.Printf("warning: failed to remove cached Caddy cert for %s: %v", deletedHost, err)
		}
	}
	return nil
}

func (m *Manager) TerminatePortOwner(id string) error {
	m.mu.RLock()
	rt, ok := m.runtimes[id]
	client := m.httpClient
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown app %q", id)
	}
	rt.refreshObservedState(client)
	state := rt.snapshot()
	if state.PortConflict == nil {
		return errors.New("no conflicting port owner found")
	}
	if state.PortConflict.ManagedByTeely {
		return errors.New("the conflicting process is already managed by Teely")
	}
	process, err := os.FindProcess(state.PortConflict.PID)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	log.Printf("sent SIGTERM to external process %d (%s) occupying app %s port %d", state.PortConflict.PID, state.PortConflict.Command, id, state.Config.Port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !probeTCP(state.Config.Port) {
			rt.mu.Lock()
			rt.portConflict = nil
			if rt.status == StatusError && strings.Contains(rt.lastError, "port") {
				rt.status = StatusStopped
				rt.lastError = ""
			}
			rt.mu.Unlock()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not release port %d after SIGTERM", state.PortConflict.PID, state.Config.Port)
}

func (m *Manager) UpsertApp(app AppConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	normalized, err := normalizeNewApp(m.configPath, app)
	if err != nil {
		return err
	}
	for _, existing := range m.config.Apps {
		if existing.ID != normalized.ID && strings.EqualFold(existing.Hostname, normalized.Hostname) {
			return fmt.Errorf("hostname %q is already used by %q", normalized.Hostname, existing.ID)
		}
		if existing.ID != normalized.ID && existing.Port == normalized.Port {
			return duplicatePortError(normalized, existing)
		}
	}

	found := false
	next := cloneConfig(m.config)
	previousHost := ""
	for i := range next.Apps {
		if next.Apps[i].ID == normalized.ID {
			previousHost = strings.TrimSpace(next.Apps[i].Hostname)
			next.Apps[i] = normalized
			found = true
			break
		}
	}
	if !found {
		next.Apps = append(next.Apps, normalized)
		previousHost = ""
	}
	if err := m.commitConfigLocked(next); err != nil {
		return err
	}
	if previousHost != "" && !strings.EqualFold(previousHost, normalized.Hostname) {
		if err := removeLocalCaddyCertCache(previousHost); err != nil {
			log.Printf("warning: failed to remove cached Caddy cert for renamed host %s: %v", previousHost, err)
		}
	}
	return nil
}

func (m *Manager) SetupState() SetupState {
	cfg := m.Config()
	home, _ := os.UserHomeDir()
	teelyLabel := teelyLaunchdLabel()
	teelyPlist := filepath.Join(home, "Library", "LaunchAgents", teelyLabel+".plist")
	trustReady, trustDetail := caddyTrustStatus()
	teelyLoginInstalled := fileExists(teelyPlist)
	checks := []SetupCheck{
		setupCheck(fileExists(cfg.Caddy.BinaryPath), "caddy_runtime", "Caddy Runtime", cfg.Caddy.BinaryPath, "install_caddy", "Install"),
		setupCheck(fileExists(cfg.Caddy.CaddyfilePath), "caddyfile", "Generated Caddyfile", cfg.Caddy.CaddyfilePath, "write_caddyfile", "Write"),
		setupCheck(trustReady, "trust", "Trusted Local HTTPS", trustDetail, "trust_caddy", "Trust"),
		singleLaunchdCheck(teelyLoginInstalled, teelyPlist),
	}
	ai := buildAISetupState(cfg)
	if strings.TrimSpace(ai.Provider) != "" {
		ai.ModelOptions, ai.ModelError = m.modelOptionsSnapshot(ai.Provider)
	}
	return SetupState{Checks: checks, AI: ai}
}

func setupCheck(installed bool, id, label, detail, action, actionLabel string) SetupCheck {
	statusLabel := "Missing"
	statusClass := "stopped"
	if installed {
		statusLabel = "Ready"
		statusClass = "running"
		action = ""
		actionLabel = ""
	}
	return SetupCheck{
		ID:          id,
		Label:       label,
		Detail:      detail,
		Installed:   installed,
		StatusLabel: statusLabel,
		StatusClass: statusClass,
		Action:      action,
		ActionLabel: actionLabel,
	}
}

func launchdCheck(installed bool, id, label, detail, installAction, installLabel, uninstallAction, uninstallLabel string) SetupCheck {
	check := setupCheck(installed, id, label, detail, installAction, installLabel)
	if installed {
		check.StatusLabel = "Enabled"
		check.StatusClass = "running"
		check.Action = uninstallAction
		check.ActionLabel = uninstallLabel
	}
	return check
}

func singleLaunchdCheck(installed bool, teelyPlist string) SetupCheck {
	detail := fmt.Sprintf("Teely launch agent: %s (%s)", loginStatusText(installed), teelyPlist)
	return launchdCheck(
		installed,
		"login_bundle",
		"Run Teely At Login",
		detail,
		"install_login_bundle",
		"Enable",
		"uninstall_login_bundle",
		"Disable",
	)
}

func loginStatusText(installed bool) string {
	if installed {
		return "Enabled"
	}
	return "Missing"
}

func (m *Manager) RunSetupAction(action string) (string, error) {
	switch action {
	case "install_caddy":
		return m.runProjectScript("install-caddy.sh", m.configPath)
	case "write_caddyfile":
		return m.runProjectScript("write-caddyfile.sh", m.configPath)
	case "install_login_bundle":
		return m.runProjectScript("install-launchd.sh", m.configPath)
	case "uninstall_login_bundle":
		return m.runProjectScript("uninstall-launchd.sh")
	case "trust_caddy":
		return m.runTrustCommand()
	default:
		return "", fmt.Errorf("unknown setup action %q", action)
	}
}

func (m *Manager) runProjectScript(name string, args ...string) (string, error) {
	root, err := teelyProjectRoot()
	if err != nil {
		return "", err
	}
	scriptPath := filepath.Join(root, "scripts", name)
	cmd := exec.Command(scriptPath, args...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (m *Manager) runTrustCommand() (string, error) {
	root, err := teelyProjectRoot()
	if err != nil {
		return "", err
	}
	scriptPath := filepath.Join(root, "scripts", "trust-caddy.sh")
	if runtime.GOOS == "darwin" {
		command := "cd " + shellQuote(root) + " && " + shellQuote(scriptPath) + " " + shellQuote(m.configPath)
		cmd := exec.Command(
			"/usr/bin/osascript",
			"-e", fmt.Sprintf(`tell application "Terminal" to do script %q`, command),
			"-e", `tell application "Terminal" to activate`,
		)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			return string(output), err
		}
		return "Opened Terminal to finish trusting Caddy's local CA. Approve the prompt there, then refresh Teely.", nil
	}
	cmd := exec.Command(scriptPath, m.configPath)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func teelyProjectRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func teelyLaunchdLabel() string {
	if label := strings.TrimSpace(os.Getenv("TEELY_LAUNCHD_LABEL")); label != "" {
		return label
	}
	return "com.marksowell.teely"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isCaddyTrusted() bool {
	trusted, _ := caddyTrustStatus()
	return trusted
}

func caddyTrustStatus() (bool, string) {
	rootCertPath, err := findCaddyRootCertPath()
	if err != nil {
		return false, "Caddy root certificate not found on disk yet."
	}
	cmd := exec.Command("/usr/bin/security", "verify-cert", "-c", rootCertPath)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	lowerText := strings.ToLower(text)
	if strings.Contains(text, "Cert Verify Result: No error.") || strings.Contains(lowerText, "certificate verification successful") {
		return true, "Verified by macOS using " + rootCertPath
	}
	if text == "" && err != nil {
		return false, "macOS trust check failed for " + rootCertPath + ": " + err.Error()
	}
	if text == "" {
		return false, "macOS did not return a trust result for " + rootCertPath
	}
	firstLine := strings.Split(text, "\n")[0]
	return false, firstLine + " (" + rootCertPath + ")"
}

func findCaddyRootCertPath() (string, error) {
	if value := strings.TrimSpace(os.Getenv("CADDY_DATA_DIR")); value != "" {
		path := filepath.Join(value, "pki", "authorities", "local", "root.crt")
		if fileExists(path) {
			return path, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	candidates := []string{
		filepath.Join(home, "Library", "Application Support", "Caddy", "pki", "authorities", "local", "root.crt"),
		filepath.Join(home, ".local", "share", "caddy", "pki", "authorities", "local", "root.crt"),
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (m *Manager) CaddySnippet() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.caddySnippetLocked()
}

func (m *Manager) syncCaddyLocked() error {
	if m.config == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.config.Caddy.CaddyfilePath), 0o755); err != nil {
		return fmt.Errorf("prepare caddyfile directory: %w", err)
	}
	if err := os.WriteFile(m.config.Caddy.CaddyfilePath, []byte(m.caddySnippetLocked()), 0o644); err != nil {
		return fmt.Errorf("write caddyfile: %w", err)
	}
	if _, err := os.Stat(m.config.Caddy.BinaryPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("check caddy binary: %w", err)
	}
	cmd := exec.Command(m.config.Caddy.BinaryPath, "reload", "--config", m.config.Caddy.CaddyfilePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return fmt.Errorf("reload caddy: %w", err)
		}
		return fmt.Errorf("reload caddy: %s", trimmed)
	}
	return nil
}

func (m *Manager) commitConfigLocked(next *Config) error {
	previous := cloneConfig(m.config)
	if err := SaveConfig(m.configPath, next); err != nil {
		return err
	}

	m.config = next
	m.rebuildFromConfigLocked()
	if err := m.syncCaddyLocked(); err != nil {
		m.config = previous
		m.rebuildFromConfigLocked()

		restoreErr := SaveConfig(m.configPath, previous)
		resyncErr := m.syncCaddyLocked()
		switch {
		case restoreErr != nil && resyncErr != nil:
			return fmt.Errorf("%w (restore config: %v; restore caddy: %v)", err, restoreErr, resyncErr)
		case restoreErr != nil:
			return fmt.Errorf("%w (restore config: %v)", err, restoreErr)
		case resyncErr != nil:
			return fmt.Errorf("%w (restore caddy: %v)", err, resyncErr)
		default:
			return err
		}
	}
	return nil
}

func (m *Manager) caddySnippetLocked() string {
	cfg := m.config
	var b strings.Builder
	b.WriteString("{\n")
	b.WriteString("\tlocal_certs\n")
	b.WriteString("\tskip_install_trust\n")
	b.WriteString("\tpki {\n")
	b.WriteString("\t\tca local {\n")
	b.WriteString("\t\t\tintermediate_lifetime 180d\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n\n")
	sharedHosts := combinedLocalhostHosts(cfg)
	if len(sharedHosts) > 0 {
		fmt.Fprintf(&b, "%s {\n", strings.Join(sharedHosts, ", "))
		b.WriteString("\tbind 0.0.0.0 ::\n")
		writeTeelyTLS(&b)
		fmt.Fprintf(&b, "\treverse_proxy %s\n", cfg.ListenAddress)
		b.WriteString("}\n\n")
	}
	for _, app := range cfg.Apps {
		if shouldUseSharedLocalhostRoute(app, cfg.ListenAddress) {
			continue
		}
		fmt.Fprintf(&b, "%s {\n", app.Hostname)
		b.WriteString("\tbind 0.0.0.0 ::\n")
		writeTeelyTLS(&b)
		if strings.TrimSpace(app.CaddyDirectives) != "" {
			for _, line := range strings.Split(app.CaddyDirectives, "\n") {
				trimmed := strings.TrimRight(line, " \t")
				if trimmed == "" {
					b.WriteString("\n")
					continue
				}
				b.WriteString("\t")
				b.WriteString(trimmed)
				b.WriteString("\n")
			}
		} else {
			fmt.Fprintf(&b, "\treverse_proxy %s\n", cfg.ListenAddress)
		}
		b.WriteString("}\n\n")
	}
	return b.String()
}

func writeTeelyTLS(b *strings.Builder) {
	b.WriteString("\ttls {\n")
	b.WriteString("\t\tissuer internal {\n")
	b.WriteString("\t\t\tlifetime 30d\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t}\n")
}

func combinedLocalhostHosts(cfg *Config) []string {
	seen := map[string]bool{}
	var hosts []string
	adminHost := strings.ToLower(strings.TrimSpace(cfg.AdminHostname))
	if strings.HasSuffix(adminHost, ".localhost") {
		hosts = append(hosts, cfg.AdminHostname)
		seen[adminHost] = true
	}
	for _, app := range cfg.Apps {
		if !shouldUseSharedLocalhostRoute(app, cfg.ListenAddress) {
			continue
		}
		host := strings.ToLower(strings.TrimSpace(app.Hostname))
		if host == "" || seen[host] {
			continue
		}
		hosts = append(hosts, app.Hostname)
		seen[host] = true
	}
	return hosts
}

func shouldUseSharedLocalhostRoute(app AppConfig, listenAddress string) bool {
	hostname := strings.ToLower(strings.TrimSpace(app.Hostname))
	if !strings.HasSuffix(hostname, ".localhost") {
		return false
	}
	directives := strings.TrimSpace(app.CaddyDirectives)
	if directives == "" {
		return true
	}
	return directives == defaultCaddyDirectives(listenAddress)
}

func removeLocalCaddyCertCache(hostname string) error {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	candidates := []string{
		filepath.Join(home, "Library", "Application Support", "Caddy", "certificates", "local", hostname),
		filepath.Join(home, ".local", "share", "caddy", "certificates", "local", hostname),
	}
	var firstErr error
	for _, path := range candidates {
		if err := os.RemoveAll(path); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) rebuildFromConfigLocked() {
	hostToApp := map[string]string{}
	newRuntimes := map[string]*appRuntime{}
	for _, app := range m.config.Apps {
		hostToApp[strings.ToLower(app.Hostname)] = app.ID
		if existing, ok := m.runtimes[app.ID]; ok {
			existing.cfg = app
			existing.proxy = newReverseProxy(app)
			newRuntimes[app.ID] = existing
			continue
		}
		newRuntimes[app.ID] = newAppRuntime(app)
	}
	m.runtimes = newRuntimes
	m.hostToApp = hostToApp
}

func (m *Manager) idleLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.RLock()
		runtimes := make([]*appRuntime, 0, len(m.runtimes))
		for _, rt := range m.runtimes {
			runtimes = append(runtimes, rt)
		}
		m.mu.RUnlock()
		for _, rt := range runtimes {
			rt.stopIfIdle()
		}
	}
}

func newAppRuntime(cfg AppConfig) *appRuntime {
	return &appRuntime{
		cfg:    cfg,
		status: StatusStopped,
		logs:   newLogBuffer(16 * 1024),
		proxy:  newReverseProxy(cfg),
	}
}

func newReverseProxy(cfg AppConfig) *httputil.ReverseProxy {
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", cfg.Port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.Host = target.Host
	}
	return proxy
}

var errAlreadyStopped = errors.New("already stopped")

func (rt *appRuntime) ensureStarted(client *http.Client, allowAutoStart bool) error {
	rt.mu.Lock()
	switch rt.status {
	case StatusRunning:
		log.Printf("app %s already running; marking used", rt.cfg.ID)
		rt.markUsedLocked()
		rt.mu.Unlock()
		return nil
	case StatusStarting:
		log.Printf("app %s already starting; marking used", rt.cfg.ID)
		rt.markUsedLocked()
		rt.mu.Unlock()
		return nil
	}
	if rt.probeReady(client) {
		log.Printf("app %s already serving on port %d; adopting existing process", rt.cfg.ID, rt.cfg.Port)
		rt.status = StatusRunning
		rt.ready = true
		rt.adopted = true
		rt.managedPID = 0
		rt.lastError = ""
		rt.exitCode = nil
		now := time.Now()
		rt.startedAt = &now
		rt.markUsedLocked()
		rt.logs.Reset()
		rt.logs.Write([]byte("[teely] adopted existing process already serving port\n"))
		rt.mu.Unlock()
		return nil
	}
	if probeTCP(rt.cfg.Port) {
		err := fmt.Errorf("port %d is already in use, but the app did not pass its HTTP health check; stop the existing process or fix the app before starting it with Teely", rt.cfg.Port)
		rt.status = StatusError
		rt.ready = false
		rt.adopted = false
		rt.managedPID = 0
		rt.lastError = err.Error()
		rt.exitCode = nil
		now := time.Now()
		rt.startedAt = &now
		rt.markUsedLocked()
		rt.logs.Reset()
		rt.logs.Write([]byte("[teely] existing process detected on configured port, but HTTP health checks failed\n"))
		log.Printf("app %s port %d is occupied by an unready or unhealthy process", rt.cfg.ID, rt.cfg.Port)
		rt.mu.Unlock()
		return err
	}

	rt.status = StatusStarting
	rt.adopted = false
	rt.managedPID = 0
	rt.ready = false
	rt.lastError = ""
	rt.exitCode = nil
	now := time.Now()
	rt.startedAt = &now
	rt.markUsedLocked()
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	rt.logs.Reset()
	rt.waitDone = make(chan struct{})

	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", rt.cfg.Command)
	cmd.Dir = rt.cfg.WorkingDir
	cmd.Stdout = rt.logs
	cmd.Stderr = rt.logs
	cmd.Env = append(os.Environ(), flattenEnv(rt.cfg.Env)...)
	log.Printf("starting app %s in %s with command %q on port %d", rt.cfg.ID, rt.cfg.WorkingDir, rt.cfg.Command, rt.cfg.Port)
	if err := ensureWorkingDir(rt.cfg.WorkingDir); err != nil {
		rt.status = StatusError
		rt.lastError = err.Error()
		log.Printf("app %s failed before start: %v", rt.cfg.ID, err)
		rt.mu.Unlock()
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		rt.status = StatusError
		rt.lastError = err.Error()
		log.Printf("app %s start command failed: %v", rt.cfg.ID, err)
		rt.mu.Unlock()
		cancel()
		return err
	}
	rt.cmd = cmd
	log.Printf("app %s spawned with pid %d", rt.cfg.ID, cmd.Process.Pid)
	rt.mu.Unlock()

	go rt.watchProcess()
	go rt.waitUntilReady(client)
	return nil
}

func (rt *appRuntime) watchProcess() {
	defer close(rt.waitDone)
	err := rt.cmd.Wait()
	rt.mu.Lock()
	defer rt.mu.Unlock()

	intentionalStop := rt.stopping
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			} else {
				exitCode = 1
			}
		} else {
			exitCode = 1
		}
		if !intentionalStop {
			rt.lastError = err.Error()
		}
	}
	rt.exitCode = &exitCode
	rt.cmd = nil
	if rt.managedPID == 0 {
		rt.adopted = false
	}
	if rt.status == StatusStarting || rt.status == StatusRunning {
		if intentionalStop {
			rt.status = StatusStopped
		} else if rt.ready {
			rt.status = StatusStopped
		} else {
			rt.status = StatusError
		}
	}
	rt.ready = false
	if intentionalStop {
		rt.lastError = ""
	}
	rt.stopping = false
	log.Printf("app %s process exited with code %d; status=%s error=%q", rt.cfg.ID, exitCode, rt.status, rt.lastError)
}

func (rt *appRuntime) waitUntilReady(client *http.Client) {
	timeout, _ := appParsedStartupTimeout(rt.cfg)
	deadline := time.Now().Add(timeout)
	log.Printf("waiting for app %s readiness on http://127.0.0.1:%d%s for up to %s", rt.cfg.ID, rt.cfg.Port, rt.cfg.HealthPath, timeout)
	for time.Now().Before(deadline) {
		if rt.probeReady(client) {
			rt.mu.Lock()
			if rt.status == StatusStarting {
				rt.status = StatusRunning
				rt.ready = true
				if conflict := detectPortConflict(rt.cfg.Port); conflict != nil {
					rt.managedPID = conflict.PID
				}
			}
			rt.mu.Unlock()
			log.Printf("app %s became ready on port %d", rt.cfg.ID, rt.cfg.Port)
			return
		}
		time.Sleep(1 * time.Second)
	}
	rt.mu.Lock()
	if rt.status == StatusStarting {
		rt.status = StatusError
		rt.lastError = fmt.Sprintf("startup timeout after %s", timeout)
	}
	rt.mu.Unlock()
	log.Printf("app %s failed readiness check: startup timeout after %s", rt.cfg.ID, timeout)
}

func (rt *appRuntime) refreshObservedState(client *http.Client) {
	conflict := detectPortConflict(rt.cfg.Port)
	rt.mu.Lock()
	cmdActive := rt.cmd != nil
	statusStarting := rt.status == StatusStarting
	rt.portConflict = conflict
	rt.mu.Unlock()

	if cmdActive {
		if rt.probeReady(client) {
			rt.mu.Lock()
			rt.portConflict = conflict
			rt.status = StatusRunning
			rt.ready = true
			if conflict != nil && rt.managedPID == 0 {
				rt.managedPID = conflict.PID
			}
			rt.lastError = ""
			rt.mu.Unlock()
			return
		}
		return
	}

	if statusStarting {
		return
	}

	if rt.probeReady(client) {
		rt.mu.Lock()
		rt.portConflict = conflict
		if rt.cmd == nil && rt.status != StatusRunning {
			rt.status = StatusRunning
			rt.ready = true
			rt.adopted = !(conflict != nil && rt.managedPID != 0 && conflict.PID == rt.managedPID)
			if rt.lastUsedAt == nil {
				now := time.Now()
				rt.lastUsedAt = &now
			}
			if rt.startedAt == nil {
				now := time.Now()
				rt.startedAt = &now
			}
			rt.lastError = ""
		}
		if conflict != nil && rt.managedPID == 0 && !rt.adopted {
			rt.managedPID = conflict.PID
		}
		rt.mu.Unlock()
		return
	}

	rt.mu.Lock()
	rt.portConflict = conflict
	if rt.cmd == nil && rt.adopted {
		rt.status = StatusStopped
		rt.ready = false
		rt.adopted = false
		rt.managedPID = 0
	}
	if rt.cmd == nil && rt.status == StatusRunning && !rt.ready {
		rt.status = StatusStopped
	}
	if rt.cmd == nil && rt.status == StatusError && rt.lastError == "" && probeTCP(rt.cfg.Port) {
		rt.lastError = fmt.Sprintf("port %d is occupied by a process that is not returning healthy HTTP responses", rt.cfg.Port)
	}
	rt.mu.Unlock()
}

func (rt *appRuntime) probeReady(client *http.Client) bool {
	if !probeTCP(rt.cfg.Port) {
		return false
	}
	req, err := http.NewRequest(rt.cfg.HealthMethod, fmt.Sprintf("http://127.0.0.1:%d%s", rt.cfg.Port, rt.cfg.HealthPath), nil)
	if err != nil {
		return true
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("app %s health probe failed after TCP connect on port %d: %v", rt.cfg.ID, rt.cfg.Port, err)
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return true
	}
	log.Printf("app %s health probe returned HTTP %d on port %d", rt.cfg.ID, resp.StatusCode, rt.cfg.Port)
	return false
}

func (rt *appRuntime) isReady() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.status == StatusRunning && rt.ready {
		return true
	}
	return false
}

func (rt *appRuntime) waitForReady(ctx context.Context, client *http.Client, maxWait time.Duration) (bool, error) {
	if maxWait <= 0 {
		maxWait = 250 * time.Millisecond
	}
	deadline := time.Now().Add(maxWait)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	for {
		if rt.probeReady(client) {
			rt.mu.Lock()
			if rt.status == StatusStarting {
				rt.status = StatusRunning
			}
			rt.ready = true
			rt.lastError = ""
			rt.mu.Unlock()
			return true, nil
		}

		rt.mu.Lock()
		status := rt.status
		lastError := rt.lastError
		rt.mu.Unlock()

		switch status {
		case StatusRunning:
			return true, nil
		case StatusError:
			if strings.TrimSpace(lastError) == "" {
				lastError = "app failed to start"
			}
			return false, errors.New(lastError)
		}

		if time.Now().After(deadline) {
			return false, nil
		}

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func backgroundRequestHoldTimeout(app AppConfig) time.Duration {
	startupTimeout, err := appParsedStartupTimeout(app)
	if err != nil {
		return 5 * time.Second
	}
	if startupTimeout < 0 {
		return 250 * time.Millisecond
	}
	if startupTimeout < 5*time.Second {
		if startupTimeout < 250*time.Millisecond {
			return 250 * time.Millisecond
		}
		return startupTimeout
	}
	return 5 * time.Second
}

func (rt *appRuntime) markUsed() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.markUsedLocked()
}

func (rt *appRuntime) markUsedLocked() {
	now := time.Now()
	rt.lastUsedAt = &now
}

func (rt *appRuntime) stopIfIdle() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.status != StatusRunning || rt.lastUsedAt == nil || rt.adopted {
		return
	}
	idleTimeout, _ := appParsedIdleTimeout(rt.cfg)
	if time.Since(*rt.lastUsedAt) < idleTimeout {
		return
	}
	log.Printf("app %s idle timeout reached after %s; stopping", rt.cfg.ID, idleTimeout)
	go rt.stop("idle timeout reached")
}

func (rt *appRuntime) stop(reason string) error {
	rt.mu.Lock()
	if rt.adopted {
		if rt.probeReady(&http.Client{Timeout: 2 * time.Second}) {
			rt.lastError = "app is running outside Teely; stop it from its original process"
			log.Printf("app %s stop requested but process is adopted/outside Teely", rt.cfg.ID)
			rt.mu.Unlock()
			return errors.New("app is running outside Teely")
		}
		rt.adopted = false
		rt.status = StatusStopped
		rt.ready = false
		rt.lastError = ""
		rt.mu.Unlock()
		return errAlreadyStopped
	}
	if rt.cmd == nil || rt.cmd.Process == nil {
		if rt.managedPID != 0 {
			managedPID := rt.managedPID
			rt.stopping = true
			rt.logs.Write([]byte("\n[teely] " + reason + "\n"))
			log.Printf("stopping app %s via tracked listener pid %d: %s", rt.cfg.ID, managedPID, reason)
			rt.mu.Unlock()

			stopManagedListener(managedPID, rt.cfg.Port)

			rt.mu.Lock()
			rt.status = StatusStopped
			rt.ready = false
			rt.managedPID = 0
			rt.stopping = false
			rt.lastError = ""
			rt.mu.Unlock()
			return nil
		}
		rt.status = StatusStopped
		rt.ready = false
		rt.managedPID = 0
		rt.stopping = false
		rt.lastError = ""
		rt.mu.Unlock()
		return errAlreadyStopped
	}
	cmd := rt.cmd
	cancel := rt.cancel
	managedPID := rt.managedPID
	cmdPID := 0
	if cmd.Process != nil {
		cmdPID = cmd.Process.Pid
	}
	rt.stopping = true
	rt.logs.Write([]byte("\n[teely] " + reason + "\n"))
	log.Printf("stopping app %s: %s", rt.cfg.ID, reason)
	rt.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if managedPID != 0 && managedPID != cmdPID {
		log.Printf("also stopping tracked listener pid %d for app %s", managedPID, rt.cfg.ID)
		stopManagedListener(managedPID, rt.cfg.Port)
	}

	select {
	case <-rt.waitDone:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		if managedPID != 0 && managedPID != cmdPID {
			forceKillProcess(managedPID)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !probeTCP(rt.cfg.Port) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	rt.mu.Lock()
	rt.status = StatusStopped
	rt.ready = false
	rt.cmd = nil
	rt.cancel = nil
	rt.managedPID = 0
	rt.stopping = false
	rt.lastError = ""
	rt.mu.Unlock()
	return nil
}

func (rt *appRuntime) snapshot() AppState {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	state := AppState{
		Config:       rt.cfg,
		Status:       rt.status,
		LastError:    rt.lastError,
		Ready:        rt.ready,
		LogTail:      rt.logs.String(),
		ProxyTarget:  fmt.Sprintf("http://127.0.0.1:%d", rt.cfg.Port),
		PortConflict: clonePortConflict(rt.portConflict),
	}
	if rt.cmd != nil && rt.cmd.Process != nil {
		state.PID = rt.cmd.Process.Pid
		if state.PortConflict != nil && state.PortConflict.PID == state.PID {
			state.PortConflict.ManagedByTeely = true
		}
	}
	if state.PortConflict != nil && rt.managedPID != 0 && state.PortConflict.PID == rt.managedPID {
		state.PortConflict.ManagedByTeely = true
	}
	if rt.startedAt != nil {
		t := *rt.startedAt
		state.StartedAt = &t
	}
	if rt.lastUsedAt != nil {
		t := *rt.lastUsedAt
		state.LastUsedAt = &t
	}
	if rt.exitCode != nil {
		code := *rt.exitCode
		state.ExitCode = &code
	}
	return state
}

func clonePortConflict(conflict *PortConflict) *PortConflict {
	if conflict == nil {
		return nil
	}
	copy := *conflict
	return &copy
}

func detectPortConflict(port int) *PortConflict {
	cmd := exec.Command("/usr/sbin/lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-Fpcn")
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		return nil
	}
	var conflict PortConflict
	for _, line := range strings.Split(string(output), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pidText := strings.TrimSpace(line[1:])
			pid, convErr := strconv.Atoi(pidText)
			if convErr == nil {
				conflict.PID = pid
			}
		case 'c':
			conflict.Command = strings.TrimSpace(line[1:])
		case 'n':
			conflict.Address = strings.TrimSpace(line[1:])
		}
	}
	if conflict.PID == 0 {
		return nil
	}
	if conflict.Command == "" {
		conflict.Command = "unknown"
	}
	return &conflict
}

func stopManagedListener(pid int, port int) {
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Signal(syscall.SIGTERM)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !probeTCP(port) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	forceKillProcess(pid)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !probeTCP(port) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func forceKillProcess(pid int) {
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Signal(syscall.SIGKILL)
	}
}

func normalizeNewApp(configPath string, app AppConfig) (AppConfig, error) {
	if app.ID == "" || app.Hostname == "" || app.WorkingDir == "" || app.Command == "" || app.Port <= 0 {
		return AppConfig{}, errors.New("id, hostname, working_dir, command, and port are required")
	}
	baseDir := filepath.Dir(configPath)
	if !filepath.IsAbs(app.WorkingDir) {
		app.WorkingDir = filepath.Clean(filepath.Join(baseDir, app.WorkingDir))
	}
	if app.HealthPath == "" {
		app.HealthPath = "/"
	}
	if app.HealthMethod == "" {
		app.HealthMethod = "GET"
	}
	if app.IdleTimeout == "" {
		app.IdleTimeout = "20m"
	}
	if app.StartupTimeout == "" {
		app.StartupTimeout = "90s"
	}
	if app.Name == "" {
		app.Name = app.ID
	}
	app.CaddyDirectives = strings.TrimSpace(app.CaddyDirectives)
	if _, err := appParsedIdleTimeout(app); err != nil {
		return AppConfig{}, err
	}
	if _, err := appParsedStartupTimeout(app); err != nil {
		return AppConfig{}, err
	}
	return app, nil
}

func flattenEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

func normalizeHost(host string) string {
	if strings.Contains(host, ":") {
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			return strings.ToLower(parsed)
		}
	}
	return strings.ToLower(host)
}

func probeTCP(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func ensureWorkingDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

type logBuffer struct {
	mu    sync.Mutex
	limit int
	buf   bytes.Buffer
}

func newLogBuffer(limit int) *logBuffer {
	return &logBuffer{limit: limit}
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if b.buf.Len() > b.limit {
		trimmed := b.buf.Bytes()[b.buf.Len()-b.limit:]
		b.buf.Reset()
		_, _ = b.buf.Write(trimmed)
	}
	return n, err
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *logBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}
