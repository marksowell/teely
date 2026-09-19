package teely

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestProbeReadyDiscoversIPv6Loopback(t *testing.T) {
	listener, err := net.Listen("tcp", net.JoinHostPort("::1", "0"))
	if err != nil {
		t.Skipf("IPv6 loopback is not available: %v", err)
	}
	defer listener.Close()

	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	rt := newAppRuntime(AppConfig{
		ID:           "ipv6-app",
		Name:         "IPv6 App",
		Port:         port,
		HealthPath:   "/",
		HealthMethod: "GET",
	})
	ready, target := rt.probeReady(&http.Client{Timeout: time.Second})
	if !ready {
		t.Fatal("probeReady returned false for an app listening on IPv6 loopback")
	}
	want := "http://" + net.JoinHostPort("::1", portText)
	if target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
	if !probeTCP(port) {
		t.Fatal("probeTCP did not detect IPv6 loopback listener")
	}

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			t.Fatalf("server exited early: %v", err)
		}
	default:
	}
}
