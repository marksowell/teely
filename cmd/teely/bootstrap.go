package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marksowell/teely/internal/teely"
)

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func ensureCaddyInstalled(cfg *teely.Config, version string) error {
	if info, err := os.Stat(cfg.Caddy.BinaryPath); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
		return nil
	}
	if runtime.GOOS != "darwin" {
		return errors.New("native Caddy install currently supports macOS only")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Caddy.BinaryPath), 0o755); err != nil {
		return err
	}

	osName := "mac"
	var archName string
	switch runtime.GOARCH {
	case "arm64":
		archName = "arm64"
	case "amd64":
		archName = "amd64"
	default:
		return fmt.Errorf("unsupported CPU architecture: %s", runtime.GOARCH)
	}

	tmpDir, err := os.MkdirTemp("", "teely-caddy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	releaseURL := "https://api.github.com/repos/caddyserver/caddy/releases/latest"
	if version != "" && version != "latest" {
		releaseURL = fmt.Sprintf("https://api.github.com/repos/caddyserver/caddy/releases/tags/v%s", version)
	}
	resp, err := http.Get(releaseURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch caddy release metadata: %s", resp.Status)
	}

	var release struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return err
	}

	var assetURL string
	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, osName) && strings.Contains(asset.Name, archName) && strings.HasSuffix(asset.Name, ".tar.gz") {
			assetURL = asset.URL
			break
		}
	}
	if assetURL == "" {
		return errors.New("no matching Caddy release asset found")
	}

	archivePath := filepath.Join(tmpDir, "caddy.tar.gz")
	if err := downloadFile(assetURL, archivePath); err != nil {
		return err
	}
	extractedPath, err := extractCaddyBinary(archivePath, tmpDir)
	if err != nil {
		return err
	}
	if err := os.Rename(extractedPath, cfg.Caddy.BinaryPath); err != nil {
		return err
	}
	if err := os.Chmod(cfg.Caddy.BinaryPath, 0o755); err != nil {
		return err
	}
	_ = execCommand("xattr", "-d", "com.apple.quarantine", cfg.Caddy.BinaryPath).Run()
	fmt.Printf("Installed Caddy for Teely:\n  %s\n", cfg.Caddy.BinaryPath)
	return nil
}

func downloadFile(url, outputPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, resp.Body)
	return err
}

func extractCaddyBinary(archivePath, targetDir string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(header.Name)
		if name != "caddy" {
			continue
		}
		outputPath := filepath.Join(targetDir, "caddy")
		out, err := os.Create(outputPath)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			return "", err
		}
		return outputPath, nil
	}
	return "", errors.New("downloaded Caddy archive did not contain the expected binary")
}

func writeCaddyfile(configPath string, cfg *teely.Config) error {
	manager, err := teely.NewManager(configPath)
	if err != nil {
		return err
	}
	defer manager.Close()
	if err := os.MkdirAll(filepath.Dir(cfg.Caddy.CaddyfilePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(cfg.Caddy.CaddyfilePath, []byte(manager.CaddySnippet()), 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote Caddyfile:\n  %s\n", cfg.Caddy.CaddyfilePath)
	return nil
}

func startTeelyProcess(configPath string, cfg *teely.Config) error {
	pidFile := filepath.Join(cfg.RuntimeDir, "run", "teely.pid")
	logPath := filepath.Join(cfg.RuntimeDir, "logs", "teely.log")
	if pid, ok := runningPID(pidFile); ok {
		fmt.Printf("Teely already running with pid %d\n", pid)
		return nil
	}
	port := listenPort(cfg.ListenAddress)
	if pid := listenerPID(port); pid != 0 {
		return fmt.Errorf("Teely port %s is already in use by pid %d", port, pid)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := execCommand(exe, "serve", "-config", configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return err
	}
	logFile.Close()
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		return err
	}
	if err := waitForPID(pidFile, 5); err != nil {
		return err
	}
	if err := waitForHTTP("http://"+cfg.ListenAddress+"/", 10); err != nil {
		return err
	}
	return nil
}

func startCaddyProcess(cfg *teely.Config) error {
	pidFile := filepath.Join(cfg.RuntimeDir, "run", "caddy.pid")
	logPath := filepath.Join(cfg.RuntimeDir, "logs", "caddy.log")
	if pid, ok := runningPID(pidFile); ok {
		fmt.Printf("Caddy already running with pid %d\n", pid)
		return nil
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := execCommand(cfg.Caddy.BinaryPath, "run", "--config", cfg.Caddy.CaddyfilePath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return err
	}
	logFile.Close()
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		return err
	}
	return waitForPID(pidFile, 5)
}

func runningPID(pidFile string) (int, bool) {
	pid, err := pidFromFile(pidFile)
	if err != nil || pid <= 0 {
		_ = os.Remove(pidFile)
		return 0, false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(pidFile)
		return 0, false
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		_ = os.Remove(pidFile)
		return 0, false
	}
	return pid, true
}

func pidFromFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func waitForPID(pidFile string, tries int) error {
	for i := 0; i < tries; i++ {
		if _, ok := runningPID(pidFile); ok {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("process did not stay running: %s", pidFile)
}

func waitForHTTP(url string, tries int) error {
	client := &http.Client{Timeout: 1 * time.Second}
	for i := 0; i < tries; i++ {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("Teely did not start cleanly at %s", url)
}

func listenerPID(port string) int {
	cmd := execCommand("lsof", "-tiTCP:"+port, "-sTCP:LISTEN")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0
	}
	return pid
}

func listenPort(address string) string {
	parts := strings.Split(address, ":")
	return parts[len(parts)-1]
}

func stopFromPIDFile(pidFile, name string) {
	pid, ok := runningPID(pidFile)
	if !ok {
		_ = os.Remove(pidFile)
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Signal(syscall.SIGTERM)
		for i := 0; i < 10; i++ {
			if err := process.Signal(syscall.Signal(0)); err != nil {
				break
			}
			time.Sleep(time.Second)
		}
		_ = process.Kill()
		fmt.Printf("Stopped %s (pid %d)\n", name, pid)
	}
	_ = os.Remove(pidFile)
}

func reportStatus(pidFile, name string) {
	if pid, ok := runningPID(pidFile); ok {
		fmt.Printf("%s: running (pid %d)\n", name, pid)
		return
	}
	fmt.Printf("%s: stopped\n", name)
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

func trustReady(certPath string) bool {
	fingerprintCmd := execCommand("/usr/bin/openssl", "x509", "-noout", "-fingerprint", "-sha256", "-in", certPath)
	fingerprintOut, err := fingerprintCmd.Output()
	if err != nil {
		return false
	}
	parts := strings.Split(strings.TrimSpace(string(fingerprintOut)), "=")
	if len(parts) != 2 {
		return false
	}
	fingerprint := strings.TrimSpace(parts[1])
	findCmd := execCommand("/usr/bin/security", "find-certificate", "-Z", "-c", "Caddy Local Authority", "/Library/Keychains/System.keychain")
	findOut, err := findCmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(findOut)), strings.ToLower(fingerprint))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
