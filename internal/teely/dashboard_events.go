package teely

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Coalesce notifications: slow clients need the latest state, not a backlog.
type dashboardEvents struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func (e *dashboardEvents) subscribe() (chan struct{}, func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.clients == nil {
		e.clients = make(map[chan struct{}]struct{})
	}
	ch := make(chan struct{}, 1)
	e.clients[ch] = struct{}{}
	ch <- struct{}{}
	return ch, func() {
		e.mu.Lock()
		delete(e.clients, ch)
		e.mu.Unlock()
	}
}

func (e *dashboardEvents) publish() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

type runtimeEventState struct {
	status   AppStatus
	ready    bool
	pid      int
	error    string
	conflict PortConflict
}

func (rt *appRuntime) publishState() {
	rt.mu.Lock()
	state := runtimeEventState{status: rt.status, ready: rt.ready, pid: rt.managedPID, error: rt.lastError}
	if rt.portConflict != nil {
		state.conflict = *rt.portConflict
	}
	changed := state != rt.lastPublished
	rt.lastPublished = state
	notify := rt.onChange
	rt.mu.Unlock()
	if changed && notify != nil {
		notify()
	}
}

func (m *Manager) serveDashboardEvents(w http.ResponseWriter, r *http.Request) {
	updates, unsubscribe := m.dashboardEvents.subscribe()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprint(w, "data: changed\n\n"); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}
}

func (m *Manager) serveDashboardCards(w http.ResponseWriter, r *http.Request) {
	apps := m.ListApps()
	cfg := m.Config()
	view := dashboardView{Config: cfg, Apps: apps, AIEnabled: buildAISetupState(cfg).Enabled, RunningCount: statusCount(apps, StatusRunning), StartingCount: statusCount(apps, StatusStarting), ErrorCount: statusCount(apps, StatusError)}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = dashboardTemplate.ExecuteTemplate(w, "app-stats", view)
	_ = dashboardTemplate.ExecuteTemplate(w, "app-cards", view)
}
