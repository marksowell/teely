package teely

import (
	"context"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
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

func TestListenerOwnedByCommandRecognizesChildListener(t *testing.T) {
	listener, err := net.Listen("tcp", net.JoinHostPort("::1", "0"))
	if err != nil {
		t.Skipf("IPv6 loopback is not available: %v", err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	listener.Close()

	cmd := exec.Command("/bin/sh", "-c", "python3 -m http.server "+portText+" --bind ::1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	var conflict *PortConflict
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conflict = detectPortConflict(mustAtoi(t, portText))
		if conflict != nil && strings.Contains(conflict.Address, portText) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if conflict == nil {
		t.Fatalf("listener on port %s was not detected", portText)
	}
	if !listenerOwnedByCommand(conflict, cmd.Process.Pid, 0) {
		t.Fatalf("listener pid %d was not recognized as owned by command pid %d", conflict.PID, cmd.Process.Pid)
	}
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	out, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
