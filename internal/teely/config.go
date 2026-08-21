package teely

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Config struct {
	ListenAddress string      `json:"listen_address"`
	AdminHostname string      `json:"admin_hostname"`
	RuntimeDir    string      `json:"runtime_dir"`
	Caddy         CaddyConfig `json:"caddy"`
	Apps          []AppConfig `json:"apps"`
}

type CaddyConfig struct {
	BinaryPath    string `json:"binary_path"`
	CaddyfilePath string `json:"caddyfile_path"`
}

type AppConfig struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Hostname       string            `json:"hostname"`
	WorkingDir     string            `json:"working_dir"`
	Command        string            `json:"command"`
	Port           int               `json:"port"`
	HealthPath     string            `json:"health_path"`
	HealthMethod   string            `json:"health_method"`
	IdleTimeout    string            `json:"idle_timeout"`
	StartupTimeout string            `json:"startup_timeout"`
	CaddyDirectives string           `json:"caddy_directives,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = "127.0.0.1:8417"
	}
	if cfg.AdminHostname == "" {
		cfg.AdminHostname = "teely.localhost"
	}

	baseDir := filepath.Dir(path)
	if cfg.RuntimeDir == "" {
		cfg.RuntimeDir = filepath.Join(baseDir, ".teely")
	} else if !filepath.IsAbs(cfg.RuntimeDir) {
		cfg.RuntimeDir = filepath.Clean(filepath.Join(baseDir, cfg.RuntimeDir))
	}
	if cfg.Caddy.BinaryPath == "" {
		cfg.Caddy.BinaryPath = filepath.Join(cfg.RuntimeDir, "bin", "caddy")
	} else if !filepath.IsAbs(cfg.Caddy.BinaryPath) {
		cfg.Caddy.BinaryPath = filepath.Clean(filepath.Join(baseDir, cfg.Caddy.BinaryPath))
	}
	if cfg.Caddy.CaddyfilePath == "" {
		cfg.Caddy.CaddyfilePath = filepath.Join(cfg.RuntimeDir, "caddy", "Caddyfile")
	} else if !filepath.IsAbs(cfg.Caddy.CaddyfilePath) {
		cfg.Caddy.CaddyfilePath = filepath.Clean(filepath.Join(baseDir, cfg.Caddy.CaddyfilePath))
	}
	seenIDs := map[string]struct{}{}
	seenHosts := map[string]struct{}{}
	for i := range cfg.Apps {
		app := &cfg.Apps[i]
		if app.ID == "" {
			return nil, errors.New("app id is required")
		}
		if app.Name == "" {
			app.Name = app.ID
		}
		if app.Hostname == "" {
			return nil, fmt.Errorf("app %q: hostname is required", app.ID)
		}
		if app.WorkingDir == "" {
			return nil, fmt.Errorf("app %q: working_dir is required", app.ID)
		}
		if !filepath.IsAbs(app.WorkingDir) {
			app.WorkingDir = filepath.Clean(filepath.Join(baseDir, app.WorkingDir))
		}
		if app.Command == "" {
			return nil, fmt.Errorf("app %q: command is required", app.ID)
		}
		if app.Port <= 0 {
			return nil, fmt.Errorf("app %q: port must be greater than 0", app.ID)
		}
		if app.HealthPath == "" {
			app.HealthPath = "/"
		}
		if !strings.HasPrefix(app.HealthPath, "/") {
			app.HealthPath = "/" + app.HealthPath
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
		if _, err := appParsedIdleTimeout(*app); err != nil {
			return nil, err
		}
		if _, err := appParsedStartupTimeout(*app); err != nil {
			return nil, err
		}
		if _, ok := seenIDs[app.ID]; ok {
			return nil, fmt.Errorf("duplicate app id %q", app.ID)
		}
		if _, ok := seenHosts[strings.ToLower(app.Hostname)]; ok {
			return nil, fmt.Errorf("duplicate hostname %q", app.Hostname)
		}
		seenIDs[app.ID] = struct{}{}
		seenHosts[strings.ToLower(app.Hostname)] = struct{}{}
	}
	slices.SortFunc(cfg.Apps, func(a, b AppConfig) int {
		return strings.Compare(a.Name, b.Name)
	})

	return &cfg, nil
}

func SaveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func appParsedIdleTimeout(app AppConfig) (time.Duration, error) {
	d, err := time.ParseDuration(app.IdleTimeout)
	if err != nil {
		return 0, fmt.Errorf("app %q idle_timeout: %w", app.ID, err)
	}
	return d, nil
}

func appParsedStartupTimeout(app AppConfig) (time.Duration, error) {
	d, err := time.ParseDuration(app.StartupTimeout)
	if err != nil {
		return 0, fmt.Errorf("app %q startup_timeout: %w", app.ID, err)
	}
	return d, nil
}
