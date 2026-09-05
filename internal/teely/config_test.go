package teely

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsAppUsingTeelyListenPort(t *testing.T) {
	path := writeTempConfig(t, `{
  "listen_address": "127.0.0.1:8417",
  "admin_hostname": "teely.localhost",
  "apps": [
    {
      "id": "bad-app",
      "hostname": "bad-app.localhost",
      "working_dir": ".",
      "command": "npm start",
      "port": 8417
    }
  ]
}`)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig succeeded, want reserved listen port error")
	}
	if !strings.Contains(err.Error(), "port 8417 is reserved by Teely's listen_address") {
		t.Fatalf("LoadConfig error = %q, want reserved listen port error", err)
	}
}

func TestLoadConfigAllowsDefaultPortWhenTeelyListenPortChanged(t *testing.T) {
	path := writeTempConfig(t, `{
  "listen_address": "127.0.0.1:9417",
  "admin_hostname": "teely.localhost",
  "apps": [
    {
      "id": "ok-app",
      "hostname": "ok-app.localhost",
      "working_dir": ".",
      "command": "npm start",
      "port": 8417
    }
  ]
}`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	if cfg.Apps[0].Port != 8417 {
		t.Fatalf("app port = %d, want 8417", cfg.Apps[0].Port)
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "teely.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
