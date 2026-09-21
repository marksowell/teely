package teely

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// The model may suggest a command, but only a verified host-only rewrite is
// offered for review. No registration or project files are written here.
func validatedLoopbackFix(app AppConfig, snapshot projectSnapshot, suggestion string) (string, error) {
	expected := commandForLoopback(app.Command, snapshot)
	if expected == app.Command {
		return "", errors.New(loopbackFixDiagnostic(app, snapshot))
	}
	if strings.TrimSpace(suggestion) == strings.TrimSpace(app.Command) || commandForLoopback(suggestion, snapshot) != expected {
		return "", errors.New("AI could not suggest a verified host-only change. No fields were changed; review the startup script's bind-address options manually.")
	}
	return expected, nil
}

var nodeListenWithoutHost = regexp.MustCompile(`(?m)^\s*[A-Za-z_$][\w$]*\.listen\(\s*([A-Za-z_$][\w$]*|[0-9]+)\s*,\s*(?:\(\s*\)\s*=>|function\s*\()`)

func loopbackFixDiagnostic(app AppConfig, snapshot projectSnapshot) string {
	if label, _, _ := expectedCommandExposure(app.Command, snapshot); label == "Loopback expected" {
		return "This startup command already requests loopback. Restart the app to apply it, then check the listener warning's process and port if it remains. No fields were changed."
	}
	// This recognizes a narrow, explicit Node pattern, not arbitrary JavaScript.
	// Describe the source evidence rather than inventing an unsupported HOST flag.
	for _, name := range projectContentFiles {
		if !strings.HasSuffix(name, ".js") && !strings.HasSuffix(name, ".mjs") {
			continue
		}
		source := snapshot.Files[name]
		if !strings.Contains(source, "node:http") && !strings.Contains(source, "node:net") && !strings.Contains(source, "node:https") {
			continue
		}
		match := nodeListenWithoutHost.FindStringSubmatchIndex(source)
		if match == nil {
			continue
		}
		port := source[match[2]:match[3]]
		return fmt.Sprintf("The inspected %s contains a Node listen call without a host argument: listen(%s, callback). If this is the active server, it listens on network interfaces by default. Teely cannot verify a command-only fix. Update that call to pass 127.0.0.1 as the host (or read HOST with a loopback default), then restart the app. Setting HOST in the command alone does nothing unless the code reads it. No files or fields were changed.", name, port)
	}
	return "Teely could not verify a supported bind-address option for this startup script. Check whether the server accepts a host flag or environment variable; otherwise its listen call needs a loopback default in project code. No files or fields were changed."
}

func (m *Manager) handleSuggestLoopback(w http.ResponseWriter, r *http.Request) {
	fail := func(message string, status int) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, status, map[string]string{"error": message})
	}
	if err := r.ParseForm(); err != nil {
		fail("Could not read the request.", 400)
		return
	}
	state, ok := m.GetAppByID(r.FormValue("id"))
	if !ok {
		fail("This app is no longer registered.", 404)
		return
	}
	provider, model, apiKey, enabled, err := m.activeAIProvider()
	if err != nil || !enabled {
		fail("Configure AI in Setup before requesting a suggestion.", 400)
		return
	}
	snapshot, err := buildProjectSnapshot(state.Config.WorkingDir)
	if err != nil {
		fail("Could not inspect the app's project folder.", 400)
		return
	}
	if commandForLoopback(state.Config.Command, snapshot) == state.Config.Command {
		fail(loopbackFixDiagnostic(state.Config, snapshot), 422)
		return
	}
	snapshot.LoopbackFix = true
	base := state.Config
	base.Env = nil // Registered environment variables may contain secrets.
	result, err := generateAIDraft(r.Context(), provider.ID, model, apiKey, snapshot, base)
	if err != nil {
		fail(err.Error(), 502)
		return
	}
	command, err := validatedLoopbackFix(state.Config, snapshot, result.Command)
	if err != nil {
		fail(err.Error(), 422)
		return
	}
	current, ok := m.GetAppByID(state.Config.ID)
	if !ok || current.Config.Command != state.Config.Command || current.Config.WorkingDir != state.Config.WorkingDir || current.Config.Port != state.Config.Port {
		fail("The app changed while AI was analyzing it. Reopen Edit App and try again.", 409)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"command": command, "original_command": state.Config.Command})
}
