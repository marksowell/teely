package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/marksowell/teely/internal/teely"
)

func main() {
	var (
		configPath = flag.String("config", "teely.json", "path to Teely config file")
		printCaddy = flag.Bool("print-caddyfile", false, "print the generated Caddyfile snippet and exit")
	)
	flag.Parse()

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
