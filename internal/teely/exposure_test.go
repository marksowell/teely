package teely

import (
	"errors"
	"strings"
	"testing"
)

func TestListenerExposure(t *testing.T) {
	for _, tt := range []struct {
		address, label string
		exposed        bool
	}{
		{"127.0.0.1:3000", "Loopback only", false},
		{"127.1.2.3:3000", "Loopback only", false},
		{"[::1]:3000", "Loopback only", false},
		{"*:3000", "Direct network listener", true},
		{"0.0.0.0:3000", "Direct network listener", true},
		{"[::]:3000", "Direct network listener", true},
		{"192.168.1.2:3000", "Direct network listener", true},
		{"mystery:3000", "Exposure unknown", false},
	} {
		t.Run(tt.address, func(t *testing.T) {
			listeners := parseListeners("p123\ncnode\nn" + tt.address + "\n")
			label, detail, exposed := listenerExposure(listeners[3000], nil)
			if label != tt.label || exposed != tt.exposed || !strings.Contains(detail, tt.address) {
				t.Fatalf("%s %s %v", label, detail, exposed)
			}
		})
	}
	listeners := parseListeners("p1\ncnode\nn127.0.0.1:3000\np2\ncruby\nn*:3000\nn[::1]:4000\n")
	if len(listeners[3000]) != 2 || len(listeners[4000]) != 1 {
		t.Fatal(listeners)
	}
	if label, _, exposed := listenerExposure(listeners[3000], nil); label != "Direct network listener" || !exposed {
		t.Fatal(label)
	}
	if label, _, _ := listenerExposure(nil, nil); label != "" {
		t.Fatal(label)
	}
	if label, _, _ := listenerExposure(nil, errors.New("failed")); label != "Exposure unknown" {
		t.Fatal(label)
	}
}

func TestExpectedCommandExposure(t *testing.T) {
	for _, tt := range []struct {
		script, command, label string
		exposed                bool
	}{
		{"next dev", "npm run dev -- -p 3001 --hostname 127.0.0.1", "Loopback expected", false},
		{"next dev --hostname 0.0.0.0", "npm run dev", "Network listener expected", true},
		{"next dev --hostname 0.0.0.0", "npm run dev -- --hostname=127.0.0.1", "Loopback expected", false},
		{"vite", "npm run dev -- --host ::1", "Loopback expected", false},
		{"vite", "npm run dev -- --host", "Network listener expected", true},
		{"vite", "npm run dev -- --host localhost", "Loopback expected", false},
		{"vite", "npm run dev", "", false},
		{"next dev", "npm run dev", "", false},
		{"node custom.js", "npm run dev -- --host 127.0.0.1", "", false},
		{"next dev && node worker.js", "npm run dev -- --hostname 127.0.0.1", "", false},
	} {
		t.Run(tt.script+tt.command, func(t *testing.T) {
			snapshot := projectSnapshot{Files: map[string]string{"package.json": `{"scripts":{"dev":"` + tt.script + `"}}`}}
			label, detail, exposed := expectedCommandExposure(tt.command, snapshot)
			if label != tt.label || exposed != tt.exposed || (label != "" && !strings.Contains(detail, "estimate")) {
				t.Fatalf("got %q %q %v", label, detail, exposed)
			}
		})
	}
}
