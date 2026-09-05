package teely

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleRegisterRejectsDuplicateCreateID(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "teely.local.json")
	appDir := filepath.Join(dir, "app")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{
  "listen_address": "127.0.0.1:8417",
  "admin_hostname": "teely.localhost",
  "apps": [
    {
      "id": "sample-app",
      "name": "Sample App",
      "hostname": "sample-app.localhost",
      "working_dir": "` + appDir + `",
      "command": "npm start",
      "port": 3000
    }
  ]
}`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(configPath)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"form_mode":       {"create"},
		"id":              {"sample-app"},
		"name":            {"Second Sample App"},
		"hostname":        {"sample-app-2.localhost"},
		"working_dir":     {appDir},
		"command":         {"PORT=3001 npm start"},
		"port":            {"3001"},
		"health_path":     {"/"},
		"health_method":   {"GET"},
		"idle_timeout":    {"10m"},
		"startup_timeout": {"90s"},
	}
	req := httptest.NewRequest(http.MethodPost, "/__teely/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	manager.handleRegister(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want form error response", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `App ID &#34;sample-app&#34; already exists`) {
		t.Fatalf("response did not include duplicate ID error: %s", rec.Body.String())
	}
	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Apps) != 1 {
		t.Fatalf("apps = %d, want 1", len(reloaded.Apps))
	}
	if reloaded.Apps[0].Name != "Sample App" || reloaded.Apps[0].Port != 3000 {
		t.Fatalf("existing app was overwritten: %+v", reloaded.Apps[0])
	}
}
