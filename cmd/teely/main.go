package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/marksowell/teely/internal/teely"
)

func main() {
	if handleCommand(os.Args[1:]) {
		return
	}
	serveMain(os.Args[1:], "teely.json")
}

func handleCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "serve":
		serveMain(args[1:], "teely.json")
		return true
	case "init":
		runInit(args[1:])
		return true
	case "up":
		runUp(args[1:])
		return true
	case "down":
		runDown(args[1:])
		return true
	case "restart":
		runRestart(args[1:])
		return true
	case "status":
		runStatus(args[1:])
		return true
	case "trust":
		runTrust(args[1:])
		return true
	case "print-caddyfile":
		runPrintCaddy(args[1:])
		return true
	default:
		return false
	}
}

func serveMain(args []string, defaultConfig string) {
	fs := flag.NewFlagSet("teely", flag.ExitOnError)
	var (
		configPath = fs.String("config", defaultConfig, "path to Teely config file")
		printCaddy = fs.Bool("print-caddyfile", false, "print the generated Caddyfile snippet and exit")
	)
	_ = fs.Parse(args)

	manager, err := teely.NewManager(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	defer manager.Close()

	if *printCaddy {
		fmt.Fprint(os.Stdout, manager.CaddySnippet())
		return
	}

	server := teely.NewHTTPServer(manager)
	log.Printf("teely listening on %s", manager.Config().ListenAddress)
	log.Printf("admin UI hostname: %s", manager.Config().AdminHostname)
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()

	select {
	case <-signalCtx.Done():
		log.Printf("teely shutting down")
		manager.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && err != http.ErrServerClosed {
			log.Printf("http shutdown error: %v", err)
		}
		if err := <-serverErr; err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	case err := <-serverErr:
		if err == nil || err == http.ErrServerClosed {
			return
		}
		log.Fatal(err)
	}
}

func runPrintCaddy(args []string) {
	fs := flag.NewFlagSet("print-caddyfile", flag.ExitOnError)
	configPath := fs.String("config", defaultBootstrapConfigPath(), "path to Teely config file")
	_ = fs.Parse(args)

	manager, err := teely.NewManager(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	defer manager.Close()
	fmt.Fprint(os.Stdout, manager.CaddySnippet())
}

func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configPath := fs.String("config", defaultBootstrapConfigPath(), "path to Teely config file")
	force := fs.Bool("force", false, "overwrite an existing config")
	_ = fs.Parse(args)

	if !*force {
		if _, err := os.Stat(*configPath); err == nil {
			fmt.Printf("Teely config already exists:\n  %s\n", *configPath)
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(*configPath), 0o755); err != nil {
		log.Fatalf("create config dir: %v", err)
	}
	sample, err := sampleConfigBytes(*configPath)
	if err != nil {
		log.Fatalf("build sample config: %v", err)
	}
	if err := os.WriteFile(*configPath, sample, 0o644); err != nil {
		log.Fatalf("write config: %v", err)
	}
	fmt.Printf("Created Teely config:\n  %s\n\n", *configPath)
	fmt.Printf("Edit that file with your app paths and commands, then run:\n")
	fmt.Printf("  teely up -config %s\n", *configPath)
}

func runUp(args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	configPath := fs.String("config", defaultBootstrapConfigPath(), "path to Teely config file")
	caddyVersion := fs.String("caddy-version", "latest", "Caddy version to install")
	_ = fs.Parse(args)

	if _, err := os.Stat(*configPath); os.IsNotExist(err) {
		sample, sampleErr := sampleConfigBytes(*configPath)
		if sampleErr != nil {
			log.Fatalf("build sample config: %v", sampleErr)
		}
		if err := os.MkdirAll(filepath.Dir(*configPath), 0o755); err != nil {
			log.Fatalf("create config dir: %v", err)
		}
		if err := os.WriteFile(*configPath, sample, 0o644); err != nil {
			log.Fatalf("write config: %v", err)
		}
		fmt.Printf("Created Teely config from sample:\n  %s\n\n", *configPath)
		fmt.Printf("Edit that file with your real app paths and commands, then run this command again.\n")
		return
	}

	cfg, err := teely.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.RuntimeDir, "logs"), 0o755); err != nil {
		log.Fatalf("create log dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.RuntimeDir, "run"), 0o755); err != nil {
		log.Fatalf("create run dir: %v", err)
	}
	if err := ensureCaddyInstalled(cfg, *caddyVersion); err != nil {
		log.Fatalf("install caddy: %v", err)
	}
	if err := writeCaddyfile(*configPath, cfg); err != nil {
		log.Fatalf("write caddyfile: %v", err)
	}
	if err := startTeelyProcess(*configPath, cfg); err != nil {
		log.Fatalf("start teely: %v", err)
	}
	if err := startCaddyProcess(cfg); err != nil {
		log.Fatalf("start caddy: %v", err)
	}

	fmt.Printf("\nTeely is starting.\n")
	fmt.Printf("Manager UI: https://%s\n", cfg.AdminHostname)
	for _, app := range cfg.Apps {
		if strings.TrimSpace(app.Hostname) != "" {
			fmt.Printf("App URL: https://%s\n", app.Hostname)
		}
	}
	fmt.Printf("\nIf this is your first HTTPS run on this Mac, trust Caddy's local CA with:\n")
	fmt.Printf("  teely trust -config %s\n", *configPath)
}

func runDown(args []string) {
	fs := flag.NewFlagSet("down", flag.ExitOnError)
	configPath := fs.String("config", defaultBootstrapConfigPath(), "path to Teely config file")
	_ = fs.Parse(args)

	cfg, err := teely.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	stopManagedProcess(filepath.Join(cfg.RuntimeDir, "run", "teely.pid"), "Teely", listenPort(cfg.ListenAddress))
	stopFromPIDFile(filepath.Join(cfg.RuntimeDir, "run", "caddy.pid"), "Caddy")
}

func runRestart(args []string) {
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	configPath := fs.String("config", defaultBootstrapConfigPath(), "path to Teely config file")
	caddyVersion := fs.String("caddy-version", "latest", "Caddy version to install")
	_ = fs.Parse(args)

	runDown([]string{"-config", *configPath})
	fmt.Printf("\nRestarting Teely and Caddy...\n\n")
	runUp([]string{"-config", *configPath, "-caddy-version", *caddyVersion})
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	configPath := fs.String("config", defaultBootstrapConfigPath(), "path to Teely config file")
	_ = fs.Parse(args)

	cfg, err := teely.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	teelyPIDFile := filepath.Join(cfg.RuntimeDir, "run", "teely.pid")
	caddyPIDFile := filepath.Join(cfg.RuntimeDir, "run", "caddy.pid")
	reportStatus(teelyPIDFile, "Teely")
	reportStatus(caddyPIDFile, "Caddy")

	if pid := listenerPID(listenPort(cfg.ListenAddress)); pid != 0 {
		recorded, _ := pidFromFile(teelyPIDFile)
		if recorded != pid {
			fmt.Printf("Teely listener detected outside managed pid file: pid %d on port %s\n", pid, listenPort(cfg.ListenAddress))
		}
	}
}

func runTrust(args []string) {
	fs := flag.NewFlagSet("trust", flag.ExitOnError)
	configPath := fs.String("config", defaultBootstrapConfigPath(), "path to Teely config file")
	_ = fs.Parse(args)

	cfg, err := teely.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if _, err := os.Stat(cfg.Caddy.CaddyfilePath); os.IsNotExist(err) {
		if err := writeCaddyfile(*configPath, cfg); err != nil {
			log.Fatalf("write caddyfile: %v", err)
		}
	}
	rootCertPath, err := findCaddyRootCertPath()
	if err != nil {
		log.Fatalf("find caddy root cert: %v", err)
	}
	if trustReady(rootCertPath) {
		fmt.Printf("Caddy local CA is already trusted:\n  %s\n", rootCertPath)
		return
	}
	fmt.Printf("Trusting Caddy local CA in the macOS System keychain:\n  %s\n", rootCertPath)
	cmd := execCommand("sudo", "/usr/bin/security", "add-trusted-cert", "-d", "-r", "trustRoot", "-k", "/Library/Keychains/System.keychain", rootCertPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("trust caddy cert: %v", err)
	}
	fmt.Printf("Caddy local CA trusted successfully.\n")
}

func defaultBootstrapConfigPath() string {
	cwd, err := os.Getwd()
	if err == nil {
		localConfig := filepath.Join(cwd, "teely.local.json")
		if _, statErr := os.Stat(localConfig); statErr == nil {
			return localConfig
		}
		sampleConfig := filepath.Join(cwd, "teely.json")
		if _, statErr := os.Stat(sampleConfig); statErr == nil {
			return localConfig
		}
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		usr, userErr := user.Current()
		if userErr != nil {
			return "teely.local.json"
		}
		configDir = filepath.Join(usr.HomeDir, "Library", "Application Support")
	}
	return filepath.Join(configDir, "Teely", "teely.json")
}

func sampleConfigBytes(configPath string) ([]byte, error) {
	cfg := teely.Config{
		ListenAddress: "127.0.0.1:8417",
		AdminHostname: "teely.localhost",
		RuntimeDir:    ".teely",
		Caddy: teely.CaddyConfig{
			BinaryPath:    ".teely/bin/caddy",
			CaddyfilePath: ".teely/caddy/Caddyfile",
		},
		Apps: []teely.AppConfig{
			{
				ID:             "sample-app",
				Name:           "Sample App",
				Hostname:       "sample-app.localhost",
				WorkingDir:     "/absolute/path/to/your-app",
				Command:        "./start.sh",
				Port:           3000,
				HealthPath:     "/",
				HealthMethod:   "GET",
				IdleTimeout:    "10m",
				StartupTimeout: "90s",
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
