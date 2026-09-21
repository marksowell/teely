package teely

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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

func TestWatchProcessIgnoresStaleCommand(t *testing.T) {
	rt := newAppRuntime(AppConfig{ID: "stale-command", Port: 1})
	oldCmd := exec.Command("/bin/sh", "-c", "sleep 0.1; exit 7")
	if err := oldCmd.Start(); err != nil {
		t.Fatal(err)
	}

	waitDone := make(chan struct{})
	rt.mu.Lock()
	rt.cmd = oldCmd
	rt.waitDone = waitDone
	rt.status = StatusRunning
	rt.ready = true
	rt.mu.Unlock()

	go rt.watchProcess(oldCmd, waitDone)

	rt.mu.Lock()
	rt.cmd = exec.Command("/bin/sh", "-c", "sleep 1")
	rt.mu.Unlock()

	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stale process watcher did not finish")
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.status != StatusRunning {
		t.Fatalf("status = %s, want %s", rt.status, StatusRunning)
	}
	if !rt.ready {
		t.Fatal("stale watcher cleared readiness")
	}
	if rt.exitCode != nil {
		t.Fatalf("stale watcher set exit code to %d", *rt.exitCode)
	}
	if rt.lastError != "" {
		t.Fatalf("stale watcher set error %q", rt.lastError)
	}
}

func TestStopCleansTeelyOwnedListenerWithoutCommandHandle(t *testing.T) {
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
	port := mustAtoi(t, portText)

	cmd := exec.Command("/bin/sh", "-c", "python3 -m http.server "+portText+" --bind ::1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
		conflict = detectPortConflict(port)
		if conflict != nil && teelyOwnedListener(conflict) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if conflict == nil {
		t.Fatalf("listener on port %s was not detected", portText)
	}

	rt := newAppRuntime(AppConfig{ID: "orphan-listener", Port: port})
	rt.status = StatusError
	rt.portConflict = conflict
	if err := rt.stop("test stop"); err != nil {
		t.Fatal(err)
	}
	if conflict := detectPortConflict(port); conflict != nil {
		t.Fatalf("listener remained after stop: pid=%d command=%s address=%s", conflict.PID, conflict.Command, conflict.Address)
	}
	if rt.status != StatusStopped {
		t.Fatalf("status = %s, want %s", rt.status, StatusStopped)
	}
}

func TestStopDoesNotCleanStaleListenerForStoppedApp(t *testing.T) {
	rt := newAppRuntime(AppConfig{ID: "stopped-app", Port: 1, WorkingDir: "."})
	rt.status = StatusStopped
	if err := rt.stop("test stop"); !errors.Is(err, errAlreadyStopped) {
		t.Fatalf("stop error = %v, want %v", err, errAlreadyStopped)
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
