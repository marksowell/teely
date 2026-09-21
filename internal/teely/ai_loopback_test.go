package teely

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type loopbackTestTransport func(*http.Request) (*http.Response, error)

func (f loopbackTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSuggestLoopbackIsDraftOnly(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key-not-real")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"dev":"next dev"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	app := AppConfig{ID: "example", Name: "Example", Hostname: "example.localhost", WorkingDir: dir, Command: "npm run dev -- -p 3001", Port: 3001, ShareLAN: true, Env: map[string]string{"PRIVATE_VALUE": "do-not-send-this"}}
	cfg := &Config{AI: AIConfig{Provider: "openai", Model: "gpt-test"}, Apps: []AppConfig{app}}
	m := &Manager{config: cfg, runtimes: map[string]*appRuntime{app.ID: {cfg: app, status: StatusStopped, logs: newLogBuffer(1024)}}}
	old := aiHTTPClient
	defer func() { aiHTTPClient = old }()
	calls := 0
	aiHTTPClient = &http.Client{Transport: loopbackTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "do-not-send-this") || !strings.Contains(string(body), "Fix only the existing startup command") {
			t.Fatal("unsafe or wrong AI prompt")
		}
		response, _ := json.Marshal(map[string]string{"output_text": `{"command":"npm run dev -- -p 3001 --hostname 127.0.0.1"}`})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(response)))}, nil
	})}
	r := httptest.NewRequest("POST", "/__teely/ai/suggest-loopback", strings.NewReader(url.Values{"id": {app.ID}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	m.handleSuggestLoopback(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "--hostname 127.0.0.1") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if cfg.Apps[0].Command != app.Command || cfg.Apps[0].Port != 3001 || !cfg.Apps[0].ShareLAN || m.runtimes[app.ID].status != StatusStopped {
		t.Fatal("suggestion modified registered app")
	}
	if cfg.Apps[0].Env["PRIVATE_VALUE"] != "do-not-send-this" {
		t.Fatal("lost registered environment")
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"dev":"node server.mjs"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("POST", "/__teely/ai/suggest-loopback", strings.NewReader(url.Values{"id": {app.ID}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	m.handleSuggestLoopback(w, r)
	if w.Code != 422 || calls != 1 {
		t.Fatal("unsupported script should return a diagnostic without another AI request")
	}
}

func TestValidatedLoopbackFix(t *testing.T) {
	snapshot := projectSnapshot{Files: map[string]string{"package.json": `{"scripts":{"dev":"next dev","start":"next start"}}`}}
	app := AppConfig{ID: "original", Command: "npm run dev -- -p 3001", Port: 3001}
	for _, tt := range []struct {
		command string
		valid   bool
	}{
		{"npm run dev -- -p 3001 --hostname 127.0.0.1", true},
		{"npm run dev -- -p 3001 -H localhost", true},
		{"npm run dev -- -p 3000 --hostname 127.0.0.1", false},
		{"npm run start -- -p 3001 --hostname 127.0.0.1", false},
		{"npm run dev -- -p 3001 --hostname 127.0.0.1; touch file", false},
		{"npm run dev -- -p 3001", false},
	} {
		command, err := validatedLoopbackFix(app, snapshot, tt.command)
		if (err == nil) != tt.valid {
			t.Fatalf("%s: %v", tt.command, err)
		}
		if tt.valid && command != "npm run dev -- -p 3001 --hostname 127.0.0.1" {
			t.Fatal(command)
		}
		if app.Command != "npm run dev -- -p 3001" || app.Port != 3001 {
			t.Fatal("mutated registration")
		}
	}
	snapshot.Files["package.json"] = `{"scripts":{"dev":"node custom.js"},"dependencies":{"next":"14.2.5"}}`
	if _, err := validatedLoopbackFix(app, snapshot, "npm run dev -- --hostname 127.0.0.1"); err == nil {
		t.Fatal("guessed flag for custom wrapper")
	}
}

func TestSharingLabelsAndSuggestion(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		w := httptest.NewRecorder()
		renderDashboard(w, dashboardView{AIEnabled: enabled, Apps: []AppState{{Config: AppConfig{ID: "example", Name: "Example"}, NetworkExposed: true, NetworkLabel: "Direct network listener", NetworkDetail: "Network warning"}}})
		body := w.Body.String()
		if !strings.Contains(body, "LAN sharing off") || strings.Contains(body, ">Local route<") || strings.Contains(body, ">Not listening<") {
			t.Fatal("sharing labels regressed")
		}
		if strings.Contains(body, ">Suggest fix</a>") != enabled {
			t.Fatal("AI action visibility does not match configuration")
		}
	}
}

func TestLoopbackFixDiagnostic(t *testing.T) {
	app := AppConfig{Command: "PORT=3002 npm start"}
	snapshot := projectSnapshot{Files: map[string]string{
		"package.json": `{"scripts":{"start":"node server.mjs"}}`,
		"server.mjs":   "import { createServer } from 'node:http';\nconst port = Number(process.env.PORT || 3000);\nserver.listen(port, () => {});",
	}}
	message := loopbackFixDiagnostic(app, snapshot)
	for _, evidence := range []string{"server.mjs", "without a host argument", "Setting HOST", "No files or fields were changed"} {
		if !strings.Contains(message, evidence) {
			t.Fatalf("missing %q: %s", evidence, message)
		}
	}
	for _, listen := range []string{"server.listen(port, host, () => {});", "server.listen(port, '127.0.0.1', () => {});", "// server.listen(port, () => {});"} {
		snapshot.Files["server.mjs"] = "import { createServer } from 'node:http';\n" + listen
		if strings.Contains(loopbackFixDiagnostic(app, snapshot), "without a host argument") {
			t.Fatalf("misdiagnosed %s", listen)
		}
	}
	snapshot.Files["package.json"] = `{"scripts":{"start":"next start"}}`
	app.Command = "npm start -- --hostname 127.0.0.1"
	if !strings.Contains(loopbackFixDiagnostic(app, snapshot), "already requests loopback") {
		t.Fatal("missing already-loopback diagnostic")
	}
}
