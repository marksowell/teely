package teely

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDashboardEventsCoalesceAndUnsubscribe(t *testing.T) {
	var events dashboardEvents
	a, cancelA := events.subscribe()
	b, cancelB := events.subscribe()
	defer cancelB()
	<-a
	<-b
	for i := 0; i < 100; i++ {
		events.publish()
	}
	if len(a) != 1 || len(b) != 1 {
		t.Fatal("updates should coalesce for each client")
	}
	cancelA()
	<-a
	events.publish()
	if len(a) != 0 {
		t.Fatal("unsubscribed client received an update")
	}
}

func TestDashboardRuntimeNotifications(t *testing.T) {
	var events dashboardEvents
	updates, cancel := events.subscribe()
	defer cancel()
	<-updates
	rt := newAppRuntime(AppConfig{ID: "example", Port: 0})
	rt.onChange = events.publish
	for _, status := range []AppStatus{StatusStarting, StatusRunning, StatusError, StatusStopped} {
		rt.mu.Lock()
		rt.status = status
		rt.mu.Unlock()
		rt.publishState()
		select {
		case <-updates:
		default:
			t.Fatalf("missing %s notification", status)
		}
		rt.publishState()
		if len(updates) != 0 {
			t.Fatal("unchanged state must not generate traffic")
		}
	}
	// Exercise the actual process watcher and Stop paths, not only the publisher.
	cmd := exec.Command("sh", "-c", "exit 1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	rt.cmd = cmd
	rt.status = StatusRunning
	done := make(chan struct{})
	rt.watchProcess(cmd, done)
	select {
	case <-updates:
	default:
		t.Fatal("process exit was not published")
	}
	_ = rt.stop("test")
	select {
	case <-updates:
	default:
		t.Fatal("stop was not published")
	}
}

func TestDashboardEventStreamReconnect(t *testing.T) {
	m := &Manager{}
	server := httptest.NewServer(http.HandlerFunc(m.serveDashboardEvents))
	defer server.Close()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if resp.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatal("wrong content type")
		}
		reader := bufio.NewReader(resp.Body)
		for j := 0; j < 2; j++ {
			line, err := reader.ReadString('\n')
			if err != nil || line != "data: changed\n" {
				t.Fatalf("stream event: %q %v", line, err)
			}
			_, _ = reader.ReadString('\n')
			if j == 0 {
				m.dashboardEvents.publish()
			}
		}
		resp.Body.Close()
		cancel()
	}
}

func TestDashboardIdleStopNotification(t *testing.T) {
	var events dashboardEvents
	updates, cancel := events.subscribe()
	defer cancel()
	<-updates
	rt := newAppRuntime(AppConfig{ID: "idle", IdleTimeout: "1s"})
	rt.onChange = events.publish
	rt.status = StatusRunning
	lastUsed := time.Now().Add(-time.Minute)
	rt.lastUsedAt = &lastUsed
	rt.stopIfIdle()
	select {
	case <-updates:
		if state := rt.snapshot(); state.Status != StatusStopped {
			t.Fatalf("idle app did not stop: %s", state.Status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle stop did not notify dashboard")
	}
}

func TestDashboardFragmentsAndNoPolling(t *testing.T) {
	cfg := fixture()
	m := &Manager{config: cfg}
	m.rebuildFromConfigLocked()
	w := httptest.NewRecorder()
	m.serveDashboardCards(w, httptest.NewRequest("GET", "/__teely/cards", nil))
	for _, id := range []string{`id="app-stats"`, `id="app-cards"`} {
		if !strings.Contains(w.Body.String(), id) {
			t.Fatalf("missing %s", id)
		}
	}
	if strings.Contains(w.Body.String(), "<script>") {
		t.Fatal("fragments must not reinitialize scripts")
	}
	w = httptest.NewRecorder()
	renderDashboard(w, dashboardView{})
	if !strings.Contains(w.Body.String(), `new EventSource("/__teely/events")`) || strings.Contains(w.Body.String(), "setTimeout(poll") {
		t.Fatal("dashboard should subscribe, not poll")
	}
	for _, path := range []string{"/__teely/events", "/__teely/cards"} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "192.168.1.20:1234"
		w := httptest.NewRecorder()
		m.handleAdmin(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("remote access to %s: %d", path, w.Code)
		}
	}
}
