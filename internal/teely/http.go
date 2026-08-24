package teely

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	urlpkg "net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed teely-icon.png
var teelyIconPNG []byte

type HTTPServer struct {
	manager *Manager
	server  *http.Server
}

func NewHTTPServer(manager *Manager) *HTTPServer {
	s := &HTTPServer{manager: manager}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	s.server = &http.Server{
		Addr:    manager.Config().ListenAddress,
		Handler: mux,
	}
	return s
}

func (s *HTTPServer) ListenAndServe() error {
	return s.server.ListenAndServe()
}

func (s *HTTPServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *HTTPServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	s.manager.HandleAppRequest(w, r)
}

func (m *Manager) handleAdmin(w http.ResponseWriter, r *http.Request) {
	switch {
	case (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Path == "/":
		apps := m.ListApps()
		cfg := m.Config()
		var editing *AppState
		var deleting *AppState
		showCreate := len(apps) == 0 || strings.TrimSpace(r.URL.Query().Get("add")) != ""
		if editID := strings.TrimSpace(r.URL.Query().Get("edit")); editID != "" {
			if app, ok := m.GetAppByID(editID); ok {
				editing = &app
				showCreate = false
			}
		}
		if deleteID := strings.TrimSpace(r.URL.Query().Get("delete")); deleteID != "" {
			if app, ok := m.GetAppByID(deleteID); ok {
				deleting = &app
				showCreate = false
				editing = nil
			}
		}
		formState := newDraftAppState(cfg)
		showModal := showCreate || editing != nil || deleting != nil
		isEditing := editing != nil
		if editing != nil {
			formState = *editing
			if strings.TrimSpace(formState.Config.CaddyDirectives) == "" {
				formState.Config.CaddyDirectives = defaultCaddyDirectives(cfg.ListenAddress)
			}
		}
		renderDashboard(w, dashboardView{
			Config:          cfg,
			Apps:            apps,
			CaddySnippet:    m.CaddySnippet(),
			Editing:         editing,
			Deleting:        deleting,
			Setup:           m.SetupState(),
			Notice:          strings.TrimSpace(r.URL.Query().Get("notice")),
			ErrorMessage:    strings.TrimSpace(r.URL.Query().Get("error")),
			ShowModal:       showModal,
			IsEditing:       isEditing,
			FormState:       formState,
			NeedsOnboarding: len(apps) == 0,
			RunningCount:    statusCount(apps, StatusRunning),
			StartingCount:   statusCount(apps, StatusStarting),
			ErrorCount:      statusCount(apps, StatusError),
		})
		return
	case r.Method == http.MethodGet && r.URL.Path == "/__teely/apps":
		writeJSON(w, http.StatusOK, m.ListApps())
		return
	case (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Path == "/__teely/icon.png":
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(teelyIconPNG)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/__teely/caddyfile":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, m.CaddySnippet())
		return
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/__teely/apps/"):
		m.handleAppAction(w, r)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/__teely/register":
		m.handleRegister(w, r)
		return
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/__teely/setup/"):
		m.handleSetupAction(w, r)
		return
	default:
		http.NotFound(w, r)
		return
	}
}

func isDirectAdminHost(host string) bool {
	switch normalizeHost(host) {
	case "127.0.0.1", "localhost":
		return true
	default:
		return false
	}
}

func (m *Manager) handleAppAction(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/__teely/apps/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, action := parts[0], parts[1]
	var err error
	switch action {
	case "start":
		err = m.StartApp(id)
	case "stop":
		err = m.StopApp(id)
	case "restart":
		err = m.RestartApp(id)
	case "delete":
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/?error="+urlpkg.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		confirm := strings.TrimSpace(r.FormValue("confirm_id"))
		app, ok := m.GetAppByID(id)
		if !ok {
			http.Redirect(w, r, "/?error="+urlpkg.QueryEscape(fmt.Sprintf("unknown app %q", id)), http.StatusSeeOther)
			return
		}
		if confirm != app.Config.ID {
			http.Redirect(w, r, "/?delete="+urlpkg.QueryEscape(id)+"&error="+urlpkg.QueryEscape("Type the app ID exactly to confirm deletion."), http.StatusSeeOther)
			return
		}
		err = m.DeleteApp(id)
	case "terminate-port-owner":
		err = m.TerminatePortOwner(id)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil && err != errAlreadyStopped {
		http.Redirect(w, r, "/?error="+urlpkg.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (m *Manager) handleSetupAction(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/__teely/setup/")
	action = strings.Trim(action, "/")
	if action == "" {
		http.NotFound(w, r)
		return
	}
	output, err := m.RunSetupAction(action)
	if err != nil {
		notice := strings.TrimSpace(output)
		if notice == "" {
			notice = err.Error()
		} else {
			notice = notice + "\n" + err.Error()
		}
		http.Redirect(w, r, "/?error="+urlpkg.QueryEscape(notice), http.StatusSeeOther)
		return
	}
	notice := strings.TrimSpace(output)
	if notice == "" {
		notice = "Setup action completed."
	}
	http.Redirect(w, r, "/?notice="+urlpkg.QueryEscape(notice), http.StatusSeeOther)
}

func (m *Manager) handleRegister(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(r.FormValue("port"))
	if err != nil {
		http.Error(w, "port must be a number", http.StatusBadRequest)
		return
	}
	app := AppConfig{
		ID:              strings.TrimSpace(r.FormValue("id")),
		Name:            strings.TrimSpace(r.FormValue("name")),
		Hostname:        strings.TrimSpace(r.FormValue("hostname")),
		WorkingDir:      strings.TrimSpace(r.FormValue("working_dir")),
		Command:         strings.TrimSpace(r.FormValue("command")),
		Port:            port,
		HealthPath:      strings.TrimSpace(r.FormValue("health_path")),
		HealthMethod:    strings.TrimSpace(r.FormValue("health_method")),
		IdleTimeout:     strings.TrimSpace(r.FormValue("idle_timeout")),
		StartupTimeout:  strings.TrimSpace(r.FormValue("startup_timeout")),
		CaddyDirectives: strings.TrimSpace(r.FormValue("caddy_directives")),
	}
	existing, exists := m.GetAppByID(app.ID)
	wasActive := false
	needsRestart := false
	if exists {
		wasActive = existing.Status == StatusRunning || existing.Status == StatusStarting
		needsRestart = appRequiresRestart(existing.Config, app)
	}
	if err := m.UpsertApp(app); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if exists && wasActive && needsRestart {
		if err := m.StopApp(app.ID); err != nil && err != errAlreadyStopped {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := m.StartApp(app.ID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func appRequiresRestart(before AppConfig, after AppConfig) bool {
	return strings.TrimSpace(before.WorkingDir) != strings.TrimSpace(after.WorkingDir) ||
		strings.TrimSpace(before.Command) != strings.TrimSpace(after.Command) ||
		before.Port != after.Port ||
		strings.TrimSpace(before.CaddyDirectives) != strings.TrimSpace(after.CaddyDirectives) ||
		strings.TrimSpace(before.HealthMethod) != strings.TrimSpace(after.HealthMethod) ||
		strings.TrimSpace(before.HealthPath) != strings.TrimSpace(after.HealthPath)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func statusCount(apps []AppState, status AppStatus) int {
	count := 0
	for _, app := range apps {
		if app.Status == status {
			count++
		}
	}
	return count
}

func newDraftAppState(cfg Config) AppState {
	return AppState{
		Config: AppConfig{
			HealthMethod:    "GET",
			HealthPath:      "/",
			IdleTimeout:     "10m",
			StartupTimeout:  "90s",
			CaddyDirectives: defaultCaddyDirectives(cfg.ListenAddress),
		},
	}
}

func defaultCaddyDirectives(listenAddress string) string {
	return "reverse_proxy " + listenAddress
}

func renderAppCaddyBlock(app AppConfig, listenAddress string) string {
	directives := strings.TrimSpace(app.CaddyDirectives)
	if directives == "" {
		directives = defaultCaddyDirectives(listenAddress)
	}
	hostname := strings.TrimSpace(app.Hostname)
	if hostname == "" {
		hostname = "<hostname>"
	}
	return hostname + " {\n\t" + strings.ReplaceAll(directives, "\n", "\n\t") + "\n}"
}

type dashboardView struct {
	Config          Config
	Apps            []AppState
	CaddySnippet    string
	Editing         *AppState
	Deleting        *AppState
	Setup           SetupState
	Notice          string
	ErrorMessage    string
	ShowModal       bool
	IsEditing       bool
	FormState       AppState
	NeedsOnboarding bool
	RunningCount    int
	StartingCount   int
	ErrorCount      int
}

func restartRequiredForView(app AppState) bool {
	return app.Status == StatusRunning || app.Status == StatusStarting
}

func renderDashboard(w http.ResponseWriter, view dashboardView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashboardTemplate.Execute(w, view)
}

func renderStartupPage(w http.ResponseWriter, state AppState) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Refresh", "2")
	_ = startupTemplate.Execute(w, state)
}

func isDocumentRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Dest")), "document") {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "text/html")
}

func isAPIRequest(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "application/json") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Requested-With")), "XMLHttpRequest") {
		return true
	}
	mode := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Mode")))
	return mode == "cors" || mode == "same-origin"
}

func renderAppRequestError(w http.ResponseWriter, r *http.Request, state AppState, err error) {
	if isDocumentRequest(r) {
		renderErrorPage(w, state, err)
		return
	}
	if isAPIRequest(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":    "app_unavailable",
			"message":  err.Error(),
			"app":      state.Config.ID,
			"hostname": state.Config.Hostname,
		})
		return
	}
	http.Error(w, err.Error(), http.StatusBadGateway)
}

func renderStartingAPI(w http.ResponseWriter, state AppState) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "2")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":       "app_starting",
		"message":     "The app is starting. Retry shortly.",
		"app":         state.Config.ID,
		"hostname":    state.Config.Hostname,
		"retry_after": 2,
	})
}

func renderStartingUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "2")
	http.Error(w, "app starting", http.StatusServiceUnavailable)
}

func renderErrorPage(w http.ResponseWriter, state AppState, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_ = errorTemplate.Execute(w, struct {
		AppState
		Error string
	}{
		AppState: state,
		Error:    err.Error(),
	})
}

var dashboardTemplate = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"formatTime": func(value *time.Time) string {
		if value == nil {
			return "-"
		}
		return value.Format("2006-01-02 15:04:05")
	},
	"restartRequiredForView": restartRequiredForView,
	"renderAppCaddyBlock":    renderAppCaddyBlock,
	"toJSON": func(value any) template.JS {
		data, err := json.Marshal(value)
		if err != nil {
			return template.JS("null")
		}
		return template.JS(data)
	},
}).Parse(`<!doctype html>
<html lang="en" data-theme="system">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Teely</title>
  <link rel="icon" type="image/png" href="/__teely/icon.png">
  <style>
    :root {
      color-scheme: light;
      --bg: #f4f1e8;
      --bg-elevated: rgba(255,251,244,0.76);
      --panel: rgba(255,252,247,0.90);
      --panel-strong: rgba(255,253,249,0.95);
      --panel-muted: rgba(242,239,231,0.92);
      --text: #182119;
      --muted: #647264;
      --line: rgba(31,44,34,0.10);
      --line-strong: rgba(31,44,34,0.14);
      --accent: #547d60;
      --accent-strong: #244135;
      --accent-soft: rgba(84,125,96,0.12);
      --green: #2f7a4b;
      --green-soft: rgba(47,122,75,0.12);
      --amber: #9b6a13;
      --amber-soft: rgba(206,154,57,0.16);
      --red: #b42318;
      --red-soft: rgba(180,35,24,0.12);
      --shadow: 0 22px 54px rgba(36,49,38,0.12);
      --radius-xl: 28px;
      --radius-lg: 20px;
      --radius-md: 14px;
      --radius-sm: 10px;
      --font-ui: "SF Pro Text", "SF Pro Display", "Helvetica Neue", -apple-system, BlinkMacSystemFont, sans-serif;
      --font-mono: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      --shell-top-space: 68px;
    }
    :root[data-theme="dark"] {
      color-scheme: dark;
      --bg: #0d1310;
      --bg-elevated: rgba(21,27,22,0.80);
      --panel: rgba(20,26,22,0.90);
      --panel-strong: rgba(24,31,26,0.94);
      --panel-muted: rgba(31,38,33,0.90);
      --text: #edf3ed;
      --muted: #a3b0a4;
      --line: rgba(232,240,231,0.10);
      --line-strong: rgba(232,240,231,0.14);
      --accent: #7db38b;
      --accent-strong: #c8dccd;
      --accent-soft: rgba(125,179,139,0.16);
      --green: #7fd8a1;
      --green-soft: rgba(127,216,161,0.18);
      --amber: #ebc777;
      --amber-soft: rgba(235,199,119,0.18);
      --red: #ff8f87;
      --red-soft: rgba(255,143,135,0.14);
      --shadow: 0 26px 68px rgba(0,0,0,0.34);
    }
    @media (prefers-color-scheme: dark) {
      :root[data-theme="system"] {
        color-scheme: dark;
        --bg: #0d1310;
        --bg-elevated: rgba(21,27,22,0.80);
        --panel: rgba(20,26,22,0.90);
        --panel-strong: rgba(24,31,26,0.94);
        --panel-muted: rgba(31,38,33,0.90);
        --text: #edf3ed;
        --muted: #a3b0a4;
        --line: rgba(232,240,231,0.10);
        --line-strong: rgba(232,240,231,0.14);
        --accent: #7db38b;
        --accent-strong: #c8dccd;
        --accent-soft: rgba(125,179,139,0.16);
        --green: #7fd8a1;
        --green-soft: rgba(127,216,161,0.18);
        --amber: #ebc777;
        --amber-soft: rgba(235,199,119,0.18);
        --red: #ff8f87;
        --red-soft: rgba(255,143,135,0.14);
        --shadow: 0 26px 68px rgba(0,0,0,0.34);
      }
    }
    * { box-sizing: border-box; }
    html, body { margin: 0; min-height: 100%; }
    body {
      font-family: var(--font-ui);
      color: var(--text);
      background:
        radial-gradient(circle at top left, rgba(120,156,124,0.16), transparent 24%),
        radial-gradient(circle at 88% 12%, rgba(186,208,188,0.26), transparent 18%),
        radial-gradient(circle at 10% 88%, rgba(195,217,192,0.22), transparent 20%),
        linear-gradient(180deg, var(--bg), color-mix(in srgb, var(--bg) 90%, #d6dfd1 10%));
      letter-spacing: -0.01em;
    }
    a { color: var(--accent); text-decoration: none; }
    a:hover { text-decoration: underline; }
    .shell {
      max-width: 1320px;
      min-height: 100vh;
      margin: 0 auto;
      padding: var(--shell-top-space) 20px 20px;
      display: flex;
      flex-direction: column;
    }
    .topbar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      padding: 14px 24px;
      border-bottom: 1px solid var(--line);
      background: color-mix(in srgb, var(--bg) 78%, rgba(255,251,244,0.72) 22%);
      backdrop-filter: blur(18px);
      box-shadow: 0 6px 18px rgba(36,49,38,0.04);
      position: fixed;
      top: 0;
      left: 0;
      right: 0;
      z-index: 20;
    }
    .brand {
      display: flex;
      align-items: center;
      gap: 10px;
      min-width: 0;
    }
    .brand-mark {
      width: 32px;
      height: 32px;
      border-radius: 9px;
      flex: 0 0 auto;
      display: block;
      box-shadow: 0 8px 18px rgba(21,49,31,0.12);
    }
    .brand-copy h1 { margin: 0; font-size: 24px; line-height: 1.05; font-weight: 700; }
    .brand-copy p { margin: 4px 0 0; color: var(--muted); font-size: 13px; }
    .toolbar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; justify-content: flex-end; }
    .chip {
      display: inline-flex; align-items: center; gap: 8px; padding: 0; color: var(--muted);
      font-size: 12px; font-weight: 600;
    }
    .theme-toggle {
      display: inline-flex; align-items: center; gap: 8px; padding: 0;
      color: var(--muted); font-size: 12px; font-weight: 600;
    }
    .theme-label {
      color: var(--muted);
      font-size: 12px;
      font-weight: 600;
    }
    .theme-select {
      appearance: none; -webkit-appearance: none; border: 1px solid var(--line); background: var(--panel-strong);
      color: var(--text); border-radius: 6px; padding: 7px 28px 7px 10px; font: inherit; font-size: 12px; font-weight: 700;
      line-height: 1; cursor: pointer;
      background-image:
        linear-gradient(45deg, transparent 50%, var(--muted) 50%),
        linear-gradient(135deg, var(--muted) 50%, transparent 50%);
      background-position:
        calc(100% - 14px) calc(50% - 2px),
        calc(100% - 9px) calc(50% - 2px);
      background-size: 5px 5px, 5px 5px;
      background-repeat: no-repeat;
    }
    .theme-select:focus {
      outline: none;
      border-color: color-mix(in srgb, var(--accent) 42%, var(--line));
      box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 12%, transparent);
    }
    .main { display: grid; gap: 16px; margin-top: 8px; }
    .footer {
      display: flex;
      justify-content: center;
      margin-top: auto;
      padding: 18px 0 10px;
    }
    .footer-link {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      color: var(--muted);
      font-size: 12px;
      font-weight: 600;
      text-decoration: none;
      transition: color 120ms ease;
    }
    .footer-link:hover {
      color: var(--text);
      text-decoration: none;
    }
    .footer-link svg {
      width: 14px;
      height: 14px;
      fill: currentColor;
      flex: 0 0 auto;
    }
    .stats {
      display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 0;
      border: 1px solid var(--line); background: color-mix(in srgb, var(--panel) 86%, transparent);
      box-shadow: var(--shadow); overflow: hidden;
    }
    .panel {
      border: 1px solid var(--line); border-radius: var(--radius-lg); background: var(--panel);
      backdrop-filter: blur(18px); box-shadow: var(--shadow);
    }
    .stat { padding: 16px; border-right: 1px solid var(--line); }
    .stat:last-child { border-right: 0; }
    .stat-label { font-size: 12px; color: var(--muted); margin-bottom: 8px; }
    .stat-value { font-size: 28px; font-weight: 700; line-height: 1; }
    .panel-head {
      display: flex; align-items: center; justify-content: space-between; gap: 12px;
      padding: 18px 18px 14px; border-bottom: 1px solid var(--line);
      background: linear-gradient(180deg, color-mix(in srgb, var(--panel-strong) 86%, transparent), transparent);
      border-top-left-radius: var(--radius-lg);
      border-top-right-radius: var(--radius-lg);
    }
    .panel-head h2 { margin: 0; font-size: 16px; line-height: 1.2; }
    .panel-head p { margin: 4px 0 0; color: var(--muted); font-size: 12px; }
    .stack { display: grid; gap: 10px; padding: 14px; }
    .summary-grid { display: grid; grid-template-columns: minmax(0, 1.8fr) minmax(300px, 0.9fr); gap: 16px; align-items: start; }
    .app-card {
      border: 1px solid var(--line); border-radius: 16px; background: var(--panel-strong); overflow: hidden;
    }
    .app-row {
      display: grid;
      grid-template-columns: minmax(0, 1.25fr) minmax(240px, 0.85fr);
      grid-template-areas:
        "identity runtime"
        "conflict conflict"
        "actions actions";
      gap: 14px 18px;
      align-items: start;
      padding: 14px 16px;
    }
    .app-identity { grid-area: identity; min-width: 0; }
    .app-row h3 { margin: 0 0 4px; font-size: 15px; }
    .subtle, .empty, .notice, .code-note { color: var(--muted); font-size: 12px; }
    .meta-line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
    .mono {
      font-family: var(--font-mono); font-size: 12px; background: var(--panel-muted); padding: 3px 7px;
      border-radius: 8px; color: var(--text); min-width: 0; display: inline-block; overflow-wrap: anywhere;
    }
    .status-pill {
      display: inline-flex; align-items: center; gap: 7px; padding: 5px 9px; border-radius: 999px;
      font-size: 11px; font-weight: 700; text-transform: capitalize; border: 1px solid transparent;
    }
    .status-pill::before {
      content: ""; width: 7px; height: 7px; border-radius: 999px; background: currentColor; opacity: 0.85;
    }
    .running { color: var(--green); background: var(--green-soft); border-color: color-mix(in srgb, var(--green) 20%, transparent); }
    .starting { color: var(--amber); background: var(--amber-soft); border-color: color-mix(in srgb, var(--amber) 24%, transparent); }
    .stopped { color: var(--muted); background: var(--panel-muted); border-color: var(--line); }
    .error { color: var(--red); background: var(--red-soft); border-color: color-mix(in srgb, var(--red) 20%, transparent); }
    .runtime-meta { grid-area: runtime; display: grid; gap: 8px; justify-items: start; min-width: 0; align-content: start; }
    .runtime-line {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
      align-items: baseline;
      min-width: 0;
      font-size: 13px;
      line-height: 1.35;
    }
    .runtime-label {
      color: var(--muted);
      font-size: 11px;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.04em;
    }
    .runtime-value {
      color: color-mix(in srgb, var(--text) 90%, var(--muted) 10%);
      font-size: 13px;
      font-weight: 600;
      overflow-wrap: anywhere;
    }
    .runtime-error {
      color: var(--red);
      font-size: 12px;
      line-height: 1.45;
      overflow-wrap: anywhere;
    }
    .app-conflict {
      grid-area: conflict;
      border: 1px solid var(--line);
      border-radius: 10px;
      background: color-mix(in srgb, var(--amber-soft) 42%, var(--panel-strong));
      padding: 11px 12px;
      overflow-wrap: anywhere;
    }
    .actions, .setup-actions, .form-actions { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
    .actions { grid-area: actions; }
    button, .button-link {
      appearance: none; border: 0; border-radius: 6px; padding: 8px 10px; font: inherit; font-size: 12px;
      font-weight: 700; line-height: 1; cursor: pointer; text-decoration: none; background: var(--accent);
      color: white; box-shadow: inset 0 1px 0 rgba(255,255,255,0.14);
    }
    button:hover, .button-link:hover { background: var(--accent-strong); text-decoration: none; }
    button:disabled {
      cursor: not-allowed;
      background: color-mix(in srgb, var(--panel-muted) 88%, transparent);
      color: color-mix(in srgb, var(--muted) 90%, white 10%);
      border: 1px solid var(--line);
      box-shadow: none;
      opacity: 0.9;
    }
    button.secondary, .button-link.secondary {
      background: var(--panel-muted); color: var(--text); border: 1px solid var(--line); box-shadow: none;
    }
    button.danger, .button-link.danger {
      background: color-mix(in srgb, var(--red) 88%, #8e1f18 12%);
      color: white;
      box-shadow: inset 0 1px 0 rgba(255,255,255,0.12);
    }
    button.danger:hover, .button-link.danger:hover {
      background: color-mix(in srgb, var(--red) 94%, #6f160f 6%);
      text-decoration: none;
    }
    form.inline { margin: 0; }
    .notice-wrap {
      display: flex; align-items: flex-start; justify-content: space-between; gap: 12px;
      border: 1px solid var(--line); border-radius: 10px; background: var(--accent-soft); color: var(--text); padding: 12px;
    }
    .notice { line-height: 1.45; }
    .notice.error-banner { background: var(--red-soft); }
    .notice.danger-banner {
      background: color-mix(in srgb, var(--red-soft) 78%, var(--panel-strong));
      border: 1px solid color-mix(in srgb, var(--red) 18%, transparent);
      color: color-mix(in srgb, var(--text) 80%, var(--red) 20%);
    }
    .danger-panel {
      display: grid;
      gap: 14px;
      padding: 16px 18px;
      border: 1px solid color-mix(in srgb, var(--red) 14%, transparent);
      border-radius: 14px;
      background:
        linear-gradient(180deg, color-mix(in srgb, var(--panel-strong) 88%, transparent), color-mix(in srgb, var(--red-soft) 18%, transparent)),
        color-mix(in srgb, var(--panel-strong) 92%, var(--red-soft) 8%);
      box-shadow: inset 0 1px 0 rgba(255,255,255,0.06);
    }
    .danger-kicker {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      font-size: 11px;
      font-weight: 800;
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: var(--red);
    }
    .danger-kicker::before {
      content: "";
      width: 8px;
      height: 8px;
      border-radius: 999px;
      background: currentColor;
      box-shadow: 0 0 0 4px color-mix(in srgb, currentColor 14%, transparent);
    }
    .danger-copy {
      display: grid;
      gap: 8px;
    }
    .danger-copy h3 {
      margin: 0;
      font-size: 16px;
      line-height: 1.2;
      color: var(--text);
    }
    .danger-copy p {
      margin: 0;
      color: var(--muted);
      font-size: 13px;
      line-height: 1.55;
    }
    .danger-confirm {
      display: grid;
      gap: 10px;
    }
    .danger-confirm .mono {
      width: fit-content;
      max-width: 100%;
    }
    .setup-list { display: grid; gap: 10px; }
    .setup-item {
      display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 14px; align-items: start; padding: 14px;
      border: 1px solid var(--line); border-radius: 10px; background: transparent;
    }
    .setup-item h3 { margin: 0 0 3px; font-size: 13px; }
    .setup-item .subtle {
      max-width: 100%;
      overflow-wrap: anywhere;
      line-height: 1.45;
      padding-right: 8px;
    }
    details.setup-detail {
      margin-top: 6px;
    }
    details.setup-detail summary {
      list-style: none;
      cursor: pointer;
      color: var(--muted);
      font-size: 11px;
      font-weight: 700;
      letter-spacing: 0.03em;
      text-transform: uppercase;
    }
    details.setup-detail summary::-webkit-details-marker { display: none; }
    .setup-detail-body {
      margin-top: 8px;
      font-family: var(--font-mono);
      font-size: 11px;
      line-height: 1.55;
      color: color-mix(in srgb, var(--muted) 88%, var(--text) 12%);
      white-space: pre-wrap;
      overflow-wrap: anywhere;
    }
    .setup-actions { justify-content: flex-end; flex-shrink: 0; }
    .empty { padding: 32px 18px; text-align: center; }
    .modal-shell {
      position: fixed; inset: 0; display: flex; align-items: center; justify-content: center; padding: 24px;
      background: rgba(14,18,15,0.38); backdrop-filter: blur(10px); z-index: 60;
    }
    .modal {
      width: min(860px, 100%); max-height: calc(100vh - 48px); overflow: auto; border: 1px solid var(--line);
      border-radius: 16px; background: var(--panel-strong); box-shadow: 0 30px 80px rgba(0,0,0,0.22);
    }
    .modal-head {
      display: flex; align-items: flex-start; justify-content: space-between; gap: 12px;
      padding: 20px 20px 16px; border-bottom: 1px solid var(--line);
    }
    .modal-head h2 { margin: 0; font-size: 18px; }
    .modal-head p { margin: 6px 0 0; font-size: 13px; color: var(--muted); line-height: 1.5; }
    .modal-body { padding: 22px 24px 24px; display: grid; gap: 18px; }
    .modal-form {
      display: grid;
      gap: 16px;
      align-content: start;
    }
    details.advanced-options {
      border: 1px solid var(--line);
      border-radius: 10px;
      overflow: hidden;
    }
    details.advanced-options {
      background: color-mix(in srgb, var(--green-soft) 44%, var(--panel-strong));
    }
    details.advanced-options summary {
      list-style: none;
      cursor: pointer;
      padding: 11px 14px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 10px;
      font-size: 12px;
      font-weight: 700;
      color: var(--muted);
    }
    details.advanced-options summary::-webkit-details-marker { display: none; }
    details.advanced-options summary::after {
      content: "+";
      font-size: 16px;
      color: var(--muted);
      line-height: 1;
    }
    details.advanced-options[open] summary::after { content: "−"; }
    .advanced-body {
      display: grid;
      gap: 14px;
      padding: 0 14px 14px;
    }
    .advanced-section {
      display: grid;
      gap: 8px;
      padding: 2px 0;
    }
    .advanced-section h3 {
      margin: 0;
      font-size: 12px;
      line-height: 1.2;
      color: var(--muted);
    }
    .advanced-section + .advanced-section {
      padding-top: 4px;
    }
    .import-json-actions {
      display: flex;
      align-items: center;
      gap: 10px;
    }
    .import-json-feedback[data-state="error"] {
      color: var(--red);
    }
    .import-json-feedback[data-state="success"] {
      color: var(--green);
    }
    .field-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px 22px; }
    label { display: grid; gap: 8px; font-size: 12px; font-weight: 600; color: var(--muted); line-height: 1.2; }
    input, textarea {
      width: 100%; border-radius: 8px; border: 1px solid var(--line-strong); background: var(--panel-muted);
      color: var(--text); padding: 10px 12px; font: inherit; font-size: 13px; outline: none;
    }
    textarea { min-height: 180px; resize: vertical; font-family: var(--font-mono); line-height: 1.45; }
    input:focus, textarea:focus {
      border-color: color-mix(in srgb, var(--accent) 42%, var(--line));
      box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 12%, transparent);
    }
    input[readonly] { opacity: 0.72; }
    .code-note { line-height: 1.5; }
    pre {
      margin: 0; white-space: pre-wrap; overflow: auto; border-radius: 8px; border: 1px solid var(--line);
      background: var(--panel-muted); color: var(--text); padding: 12px; font-family: var(--font-mono);
      font-size: 12px; line-height: 1.45; max-height: 240px;
    }
    details.app-detail {
      border-top: 1px solid var(--line);
      background: color-mix(in srgb, var(--panel-muted) 78%, transparent);
    }
    details.app-detail summary {
      list-style: none; cursor: pointer; padding: 11px 16px; font-size: 12px; font-weight: 700;
      color: var(--muted); display: flex; align-items: center; justify-content: space-between;
    }
    details.app-detail summary::-webkit-details-marker { display: none; }
    details.app-detail summary::after { content: "+"; font-size: 16px; color: var(--muted); }
    details.app-detail[open] summary::after { content: "−"; }
    .detail-body { padding: 0 16px 16px; display: grid; gap: 12px; }
    .detail-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px 12px; }
    .detail-label { display: block; margin-bottom: 3px; font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--muted); }
    @media (max-width: 1080px) {
      .summary-grid { grid-template-columns: 1fr; }
      .stats { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .app-row {
        grid-template-columns: 1fr;
        grid-template-areas:
          "identity"
          "runtime"
          "conflict"
          "actions";
      }
      .topbar { padding: 16px 18px; }
      .setup-item { grid-template-columns: 1fr; }
      .setup-actions { justify-content: flex-start; }
    }
    @media (max-width: 720px) {
      .shell { padding: var(--shell-top-space) 14px 14px; }
      .topbar {
        padding: 12px 16px;
        flex-direction: column;
        align-items: flex-start;
        gap: 10px;
      }
      .brand { width: 100%; }
      .toolbar {
        width: 100%;
        justify-content: flex-start;
        gap: 8px;
      }
      .chip, .theme-toggle { font-size: 11px; }
      .field-grid, .detail-grid { grid-template-columns: 1fr; }
      .stats { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .modal-shell { padding: 14px; }
    }
  </style>
</head>
<body>
  <div class="shell">
    <header class="topbar">
      <div class="brand">
        <img class="brand-mark" src="/__teely/icon.png" alt="Teely icon">
        <div class="brand-copy">
          <h1>Teely</h1>
        </div>
      </div>
      <div class="toolbar">
        <span class="chip">UI <span class="mono">{{ .Config.AdminHostname }}</span></span>
        <span class="chip">Proxy <span class="mono">{{ .Config.ListenAddress }}</span></span>
        <div class="theme-toggle">
          <span class="theme-label">Theme</span>
          <label class="subtle" for="theme-select" style="display:none;">Theme</label>
          <select id="theme-select" class="theme-select" aria-label="Theme">
            <option value="system">System</option>
            <option value="light">Light</option>
            <option value="dark">Dark</option>
          </select>
        </div>
      </div>
    </header>
    <main class="main">
      {{ if .Notice }}<div class="notice-wrap"><div class="notice">{{ .Notice }}</div><a class="button-link secondary" href="/">Dismiss</a></div>{{ end }}
      {{ if .ErrorMessage }}<div class="notice-wrap notice error-banner"><pre style="flex:1;">{{ .ErrorMessage }}</pre><a class="button-link secondary" href="/">Dismiss</a></div>{{ end }}
      <section class="stats">
        <div class="stat"><div class="stat-label">Registered apps</div><div class="stat-value">{{ len .Apps }}</div></div>
        <div class="stat"><div class="stat-label">Running</div><div class="stat-value">{{ .RunningCount }}</div></div>
        <div class="stat"><div class="stat-label">Starting</div><div class="stat-value">{{ .StartingCount }}</div></div>
        <div class="stat"><div class="stat-label">Needs attention</div><div class="stat-value">{{ .ErrorCount }}</div></div>
      </section>

      <section class="summary-grid">
        <section class="panel">
          <div class="panel-head">
            <div>
              <h2>Registered Apps</h2>
            </div>
            <a class="button-link" href="/?add=1">Add App</a>
          </div>
          <div class="stack">
            {{ if .Apps }}
            {{ range .Apps }}
            <article class="app-card">
              <div class="app-row">
                <div class="app-identity">
                  <h3>{{ .Config.Name }}</h3>
                  <div class="meta-line">
                    <span class="mono">{{ .Config.ID }}</span>
                    <a class="mono" href="https://{{ .Config.Hostname }}" target="_blank" rel="noreferrer">https://{{ .Config.Hostname }}</a>
                    <span class="mono">port {{ .Config.Port }}</span>
                  </div>
                  <div class="subtle" style="margin-top:6px;">{{ .Config.WorkingDir }}</div>
                </div>
                <div class="runtime-meta">
                  <span class="status-pill {{ .Status }}">{{ .Status }}</span>
                  <div class="runtime-line">
                    <span class="runtime-label">Last Used</span>
                    <span class="runtime-value">{{ formatTime .LastUsedAt }}</span>
                  </div>
                  <div class="runtime-line">
                    <span class="runtime-label">Idle Timeout</span>
                    <span class="runtime-value">{{ .Config.IdleTimeout }}</span>
                  </div>
                  {{ if .LastError }}<div class="runtime-error">{{ .LastError }}</div>{{ end }}
                </div>
                {{ if and .PortConflict (not .PortConflict.ManagedByTeely) }}
                <div class="notice app-conflict">
                  Port {{ .Config.Port }} is currently held by
                  <strong>{{ .PortConflict.Command }}</strong>
                  (pid {{ .PortConflict.PID }}{{ if .PortConflict.Address }}, {{ .PortConflict.Address }}{{ end }}).
                  You can terminate it here before starting the app.
                </div>
                {{ end }}
                <div class="actions">
                  <form class="inline" method="post" action="/__teely/apps/{{ .Config.ID }}/start"><button {{ if or (eq .Status "running") (eq .Status "starting") }}disabled aria-disabled="true"{{ end }}>Start</button></form>
                  <form class="inline" method="post" action="/__teely/apps/{{ .Config.ID }}/restart"><button class="secondary">Restart</button></form>
                  <form class="inline" method="post" action="/__teely/apps/{{ .Config.ID }}/stop"><button class="secondary" {{ if and (ne .Status "running") (ne .Status "starting") }}disabled aria-disabled="true"{{ end }}>Stop</button></form>
                  {{ if and .PortConflict (not .PortConflict.ManagedByTeely) }}
                  <form class="inline" method="post" action="/__teely/apps/{{ .Config.ID }}/terminate-port-owner"><button class="secondary">Terminate Port Owner</button></form>
                  {{ end }}
                  <a class="button-link secondary" href="/?edit={{ .Config.ID }}">Edit</a>
                  <a class="button-link secondary" href="https://{{ .Config.Hostname }}" target="_blank" rel="noreferrer">Open</a>
                </div>
              </div>
              <details class="app-detail">
                <summary>Health check, command, logs, and Caddy block</summary>
                <div class="detail-body">
                  <div class="detail-grid">
                    <div>
                      <span class="detail-label">Command</span>
                      <span class="mono">{{ .Config.Command }}</span>
                    </div>
                    <div>
                      <span class="detail-label">Health check</span>
                      <span class="mono">{{ .Config.HealthMethod }} {{ .ProxyTarget }}{{ .Config.HealthPath }}</span>
                    </div>
                    <div>
                      <span class="detail-label">Startup timeout</span>
                      <span class="mono">{{ .Config.StartupTimeout }}</span>
                    </div>
                    <div>
                      <span class="detail-label">Caddy block</span>
                      <span class="mono">{{ .Config.Hostname }}</span>
                    </div>
                  </div>
                  <pre>{{ renderAppCaddyBlock .Config $.Config.ListenAddress }}</pre>
                  <pre>{{ if .LogTail }}{{ .LogTail }}{{ else }}No logs captured yet.{{ end }}</pre>
                </div>
              </details>
            </article>
            {{ end }}
            {{ else }}
            <div class="empty">No apps registered yet. Teely will open an add-app walkthrough automatically on first launch.</div>
            {{ end }}
          </div>
        </section>

        <aside class="panel">
          <div class="panel-head">
            <div>
              <h2>Setup</h2>
            </div>
          </div>
          <div class="stack">
            <div class="setup-list">
              {{ range .Setup.Checks }}
              <div class="setup-item">
                <div>
                  <h3>{{ .Label }}</h3>
                  {{ if .Detail }}
                  <details class="setup-detail">
                    <summary>Details</summary>
                    <div class="setup-detail-body">{{ .Detail }}</div>
                  </details>
                  {{ end }}
                </div>
                <div class="setup-actions">
                  <span class="status-pill {{ .StatusClass }}">{{ .StatusLabel }}</span>
                  {{ if .Action }}
                  <form class="inline" method="post" action="/__teely/setup/{{ .Action }}"><button class="secondary">{{ .ActionLabel }}</button></form>
                  {{ end }}
                </div>
              </div>
              {{ end }}
            </div>
          </div>
        </aside>
      </section>
    </main>
    <footer class="footer">
      <a class="footer-link" href="https://github.com/marksowell/teely" target="_blank" rel="noreferrer">
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M8 0C3.58 0 0 3.67 0 8.2c0 3.63 2.29 6.7 5.47 7.79.4.08.55-.18.55-.39 0-.19-.01-.82-.01-1.49-2.01.38-2.53-.5-2.69-.96-.09-.24-.48-.96-.82-1.15-.28-.16-.68-.55-.01-.56.63-.01 1.08.59 1.23.84.72 1.24 1.87.89 2.33.68.07-.53.28-.89.5-1.1-1.78-.21-3.64-.92-3.64-4.08 0-.9.31-1.64.82-2.22-.08-.2-.36-1.04.08-2.17 0 0 .67-.22 2.2.85A7.4 7.4 0 0 1 8 3.86c.68 0 1.37.09 2.01.27 1.53-1.07 2.2-.85 2.2-.85.44 1.13.16 1.97.08 2.17.51.58.82 1.31.82 2.22 0 3.17-1.87 3.87-3.65 4.08.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .22.14.48.55.39A8.19 8.19 0 0 0 16 8.2C16 3.67 12.42 0 8 0Z"/>
        </svg>
        <span>View Teely on GitHub</span>
      </a>
    </footer>
  </div>

  {{ if .ShowModal }}
  <div class="modal-shell">
    <div class="modal">
      <div class="modal-head">
        <div>
          <h2>{{ if .Deleting }}Delete App{{ else if .IsEditing }}Edit App{{ else if .NeedsOnboarding }}Register Your First App{{ else }}Add App{{ end }}</h2>
          <p>{{ if .Deleting }}Remove this app from Teely after confirming the app ID.{{ else if .IsEditing }}Update the app, route, and custom Caddy directives in one place.{{ else if .NeedsOnboarding }}Teely starts here. Add one app and the dashboard will collapse down to status and controls after setup.{{ else }}Add a new local app with its startup command, hostname, and Caddy routing directives.{{ end }}</p>
        </div>
        <div class="actions">
          {{ if not .NeedsOnboarding }}<a class="button-link secondary" href="/">Close</a>{{ end }}
        </div>
      </div>
      <div class="modal-body">
        {{ if .Deleting }}
        <form class="modal-form" method="post" action="/__teely/apps/{{ .Deleting.Config.ID }}/delete">
          <div class="danger-panel">
            <span class="danger-kicker">Permanent Action</span>
            <div class="danger-copy">
              <h3>{{ .Deleting.Config.Name }}</h3>
              <p>This removes the app from Teely. Your project files stay on disk.</p>
            </div>
            <div class="danger-confirm">
              <div class="subtle">Type this app ID to confirm deletion.</div>
              <span class="mono">{{ .Deleting.Config.ID }}</span>
            </div>
          </div>
          <label>App ID
            <input name="confirm_id" placeholder="{{ .Deleting.Config.ID }}" autocomplete="off" required>
          </label>
          <div class="form-actions">
            <button type="submit" class="danger">Delete App</button>
            <a class="button-link secondary" href="/">Cancel</a>
          </div>
        </form>
        {{ else }}
        {{ if .IsEditing }}
        <div class="notice">Editing <strong>{{ .FormState.Config.Name }}</strong>. Changing the hostname updates routing to the new URL after save.</div>
        {{ end }}
        <form class="modal-form" method="post" action="/__teely/register" {{ if .IsEditing }}data-original-app='{{ toJSON .FormState.Config }}' data-app-active="{{ if restartRequiredForView .FormState }}true{{ else }}false{{ end }}"{{ end }}>
          <div class="field-grid">
            <label>ID<input name="id" placeholder="sample-app" value="{{ .FormState.Config.ID }}" {{ if .IsEditing }}readonly{{ end }} required></label>
            <label>Name<input name="name" placeholder="Sample App" value="{{ .FormState.Config.Name }}"></label>
          </div>
          <div class="field-grid">
            <label>Hostname<input name="hostname" placeholder="sample-app.localhost" value="{{ .FormState.Config.Hostname }}" required></label>
            <label>Port<input name="port" placeholder="3000" value="{{ .FormState.Config.Port }}" required></label>
          </div>
          <label>Working Directory<input name="working_dir" placeholder="/absolute/path/to/your-app" value="{{ .FormState.Config.WorkingDir }}" required></label>
          <label>Command<input name="command" placeholder="./start.sh" value="{{ .FormState.Config.Command }}" required></label>
          <div class="field-grid">
            <label>Idle Timeout<input name="idle_timeout" placeholder="10m" value="{{ .FormState.Config.IdleTimeout }}"></label>
            <label>Startup Timeout<input name="startup_timeout" placeholder="90s" value="{{ .FormState.Config.StartupTimeout }}"></label>
          </div>
          <div class="field-grid">
            <label>Health Method<input name="health_method" value="{{ .FormState.Config.HealthMethod }}"></label>
            <label>Health Path<input name="health_path" value="{{ .FormState.Config.HealthPath }}"></label>
          </div>
          <details class="advanced-options">
            <summary>Advanced (Optional)</summary>
            <div class="advanced-body">
              <div class="advanced-section">
                <h3>Teely App JSON</h3>
                <div class="code-note">Paste a Teely app JSON object here to populate this app's registration fields.</div>
                <textarea data-app-json-source spellcheck="false" placeholder='{"id":"sample-app","name":"Sample App","hostname":"sample-app.localhost","working_dir":"/absolute/path/to/your-app","command":"./start.sh","port":3000}' aria-label="App JSON"></textarea>
                <div class="import-json-actions">
                  <div class="code-note import-json-feedback" data-app-json-feedback></div>
                </div>
              </div>
              <div class="advanced-section">
                <h3>Custom Caddy Directives</h3>
                <div class="code-note">Add advanced Caddy directives for this app. Leave the default reverse proxy to {{ .Config.ListenAddress }} to keep Teely's on-demand startup flow in front of the app.</div>
                <textarea name="caddy_directives" spellcheck="false" aria-label="Custom Caddy Directives">{{ .FormState.Config.CaddyDirectives }}</textarea>
              </div>
            </div>
          </details>
          {{ if .IsEditing }}
          <div class="notice" data-restart-warning hidden>Changing the port, command, working directory, health check, or custom Caddy directives will stop and restart this app.</div>
          {{ end }}
          <div class="form-actions">
            <button type="submit" data-submit-label>{{ if .IsEditing }}Save Changes{{ else if .NeedsOnboarding }}Save First App{{ else }}Save App{{ end }}</button>
            {{ if not .NeedsOnboarding }}<a class="button-link secondary" href="/">Cancel</a>{{ end }}
            {{ if .IsEditing }}<a class="button-link danger" href="/?delete={{ .FormState.Config.ID }}">Delete</a>{{ end }}
          </div>
        </form>
        {{ end }}
      </div>
    </div>
  </div>
  {{ end }}

  <script>
    (() => {
      const initialApps = {{ toJSON .Apps }};
      const storageKey = "teely.theme";
      const root = document.documentElement;
      const select = document.getElementById("theme-select");
      const topbar = document.querySelector(".topbar");
      const syncHeaderOffset = () => {
        if (!topbar) {
          return;
        }
        const extra = 8;
        const height = Math.ceil(topbar.getBoundingClientRect().height);
        root.style.setProperty("--shell-top-space", String(height + extra) + "px");
      };
      const applyTheme = (theme) => {
        root.dataset.theme = theme;
        if (select) {
          select.value = theme;
        }
      };
      syncHeaderOffset();
      const saved = window.localStorage.getItem(storageKey) || "system";
      applyTheme(saved);
      window.addEventListener("resize", syncHeaderOffset);
      if (select) {
        select.addEventListener("change", () => {
          const theme = select.value;
          window.localStorage.setItem(storageKey, theme);
          applyTheme(theme);
        });
      }

      const url = new URL(window.location.href);
      let changed = false;
      ["notice", "error"].forEach((key) => {
        if (url.searchParams.has(key)) {
          url.searchParams.delete(key);
          changed = true;
        }
      });
      if (changed) {
        const next = url.pathname + (url.searchParams.toString() ? "?" + url.searchParams.toString() : "") + url.hash;
        window.history.replaceState({}, "", next);
      }

      const normalizeApps = (apps) => JSON.stringify(
        (Array.isArray(apps) ? apps : []).map((app) => ({
          id: app.config?.id || "",
          status: app.status || "",
          ready: Boolean(app.ready),
          pid: app.pid || 0,
          lastError: app.last_error || "",
          conflictPid: app.port_conflict?.pid || 0,
          conflictManaged: Boolean(app.port_conflict?.managed_by_teely),
        }))
      );
      let baselineApps = normalizeApps(initialApps);
      let refreshTimer = null;
      const poll = async () => {
        try {
          const response = await fetch("/__teely/apps", { cache: "no-store" });
          if (!response.ok) {
            refreshTimer = window.setTimeout(poll, 2500);
            return;
          }
          const apps = await response.json();
          const nextApps = normalizeApps(apps);
          if (nextApps !== baselineApps) {
            window.location.reload();
            return;
          }
          refreshTimer = window.setTimeout(poll, 2500);
        } catch (_) {
          refreshTimer = window.setTimeout(poll, 3000);
        }
      };
      if (Array.isArray(initialApps) && initialApps.length > 0) {
        refreshTimer = window.setTimeout(poll, 1500);
        window.addEventListener("beforeunload", () => {
          if (refreshTimer !== null) {
            window.clearTimeout(refreshTimer);
          }
        });
      }

      const appFieldNames = [
        "id",
        "name",
        "working_dir",
        "command",
        "port",
        "health_path",
        "health_method",
        "idle_timeout",
        "startup_timeout",
        "caddy_directives",
      ];
      const pickAppObject = (value) => {
        if (!value || typeof value !== "object" || Array.isArray(value)) {
          return null;
        }
        return value;
      };
      const applyAppJSON = (form) => {
        if (!form) {
          return { ok: false, message: "Form not found." };
        }
        const source = form.querySelector("[data-app-json-source]");
        const feedback = form.querySelector("[data-app-json-feedback]");
        if (!source || !feedback) {
          return { ok: false, message: "Importer not available." };
        }
        const raw = source.value.trim();
        if (!raw) {
          return { ok: false, message: "Paste JSON first." };
        }
        let parsed;
        try {
          parsed = JSON.parse(raw);
        } catch (error) {
          return { ok: false, message: "Invalid JSON: " + (error && error.message ? error.message : "parse failed") };
        }
        const app = pickAppObject(parsed);
          if (!app) {
            return { ok: false, message: "Expected one app JSON object." };
          }
        appFieldNames.forEach((name) => {
          const input = form.querySelector("[name=\"" + name + "\"]");
          if (!input || input.hasAttribute("readonly") || !(name in app)) {
            return;
          }
          const value = app[name];
          input.value = value === null || typeof value === "undefined" ? "" : String(value);
        });
        return { ok: true, message: "Applied JSON to the form." };
      };
      document.querySelectorAll("[data-app-json-source]").forEach((source) => {
        const form = source.closest("form");
        const feedback = form && form.querySelector("[data-app-json-feedback]");
        if (!form || !feedback) {
          return;
        }
        let applyTimer = null;
        const syncFromJSON = () => {
          const raw = source.value.trim();
          if (!raw) {
            feedback.textContent = "";
            delete feedback.dataset.state;
            return;
          }
          const result = applyAppJSON(form);
          feedback.textContent = result.message;
          feedback.dataset.state = result.ok ? "success" : "error";
        };
        source.addEventListener("input", () => {
          if (applyTimer !== null) {
            window.clearTimeout(applyTimer);
          }
          applyTimer = window.setTimeout(syncFromJSON, 250);
        });
        source.addEventListener("blur", syncFromJSON);
        source.addEventListener("paste", () => {
          window.setTimeout(syncFromJSON, 0);
        });
      });

      const restartFields = [
        "working_dir",
        "command",
        "port",
        "health_method",
        "health_path",
        "caddy_directives",
      ];
      document.querySelectorAll("form[data-original-app]").forEach((form) => {
        const originalJSON = form.getAttribute("data-original-app");
        const submit = form.querySelector("[data-submit-label]");
        const warning = form.querySelector("[data-restart-warning]");
        const appActive = form.getAttribute("data-app-active") === "true";
        if (!originalJSON || !submit || !warning) {
          return;
        }
        let original = null;
        try {
          original = JSON.parse(originalJSON);
        } catch (_) {
          return;
        }
        const syncRestartState = () => {
          const needsRestart = restartFields.some((name) => {
            const input = form.querySelector("[name=\"" + name + "\"]");
            if (!input) {
              return false;
            }
            const current = String(input.value || "").trim();
            const baseValue = original && Object.prototype.hasOwnProperty.call(original, name) ? original[name] : "";
            const base = String(baseValue || "").trim();
            return current !== base;
          });
          submit.textContent = needsRestart && appActive ? "Save and Restart" : "Save Changes";
          warning.hidden = !(needsRestart && appActive);
        };
        restartFields.forEach((name) => {
          const input = form.querySelector("[name=\"" + name + "\"]");
          if (!input) {
            return;
          }
          input.addEventListener("input", syncRestartState);
          input.addEventListener("change", syncRestartState);
        });
        syncRestartState();
      });
    })();
  </script>
</body>
</html>`))

var startupTemplate = template.Must(template.New("startup").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Starting {{ .Config.Name }}</title>
  <style>
    body { margin:0; min-height:100vh; display:grid; place-items:center; background:linear-gradient(180deg,#f8f5ee,#efe7d8); color:#1f2e28; font-family: ui-rounded, "SF Pro Text", sans-serif; }
    .card { width:min(560px, calc(100vw - 32px)); padding:32px; background:rgba(255,255,255,0.75); border:1px solid rgba(73,87,72,0.12); border-radius:24px; box-shadow:0 20px 50px rgba(31,46,40,0.08); }
    h1 { display:flex; align-items:center; gap:10px; margin:0 0 18px; line-height:1; }
    .pulse { width:12px; height:12px; border-radius:999px; background:#5b8a67; box-shadow:0 0 0 0 rgba(91,138,103,.6); animation:pulse 1.5s infinite; display:block; flex:0 0 auto; }
    @keyframes pulse { 0% { box-shadow:0 0 0 0 rgba(91,138,103,.6);} 70% { box-shadow:0 0 0 14px rgba(91,138,103,0);} 100% { box-shadow:0 0 0 0 rgba(91,138,103,0);} }
    code { font-family: ui-monospace, SFMono-Regular, monospace; background:#f2eee4; padding:2px 6px; border-radius:6px; }
    p { color:#59655d; }
  </style>
</head>
<body>
  <div class="card">
    <h1><span class="pulse"></span>Starting {{ .Config.Name }}</h1>
    <p>Teely saw traffic for <code>{{ .Config.Hostname }}</code> and is starting the app on demand.</p>
    <p>The browser will refresh automatically once the app is ready on <code>localhost:{{ .Config.Port }}</code>.</p>
    {{ if .LastError }}<p>Last error: <code>{{ .LastError }}</code></p>{{ end }}
  </div>
</body>
</html>`))

var errorTemplate = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{ .Config.Name }} error</title></head>
<body style="font-family: ui-rounded, sans-serif; background:#fbf3f1; color:#3c211c; padding:32px;">
  <h1>{{ .Config.Name }} could not start</h1>
  <p>{{ .Error }}</p>
  {{ if .LastError }}<pre>{{ .LastError }}</pre>{{ end }}
  <p>Open <a href="https://teely.localhost">Teely</a> for logs and controls.</p>
</body>
</html>`))
