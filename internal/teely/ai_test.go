package teely

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssignAvailablePortAddsNextPortFlag(t *testing.T) {
	snapshot := projectSnapshot{
		Files: map[string]string{
			"package.json": `{"scripts":{"dev":"next dev"},"dependencies":{"next":"14.2.5"}}`,
		},
	}
	app := AppConfig{
		Command: "npm run dev",
		Port:    3000,
	}

	got := assignAvailablePort(app, snapshot, nil)

	if got.Port != 3000 {
		t.Fatalf("Port = %d, want 3000", got.Port)
	}
	if got.Command != "npm run dev -- -p 3000" {
		t.Fatalf("Command = %q, want explicit Next port flag", got.Command)
	}
}

func TestAssignAvailablePortUsesNextFreePortForNextApp(t *testing.T) {
	snapshot := projectSnapshot{
		Files: map[string]string{
			"package.json": `{"scripts":{"dev":"next dev"},"dependencies":{"next":"14.2.5"}}`,
		},
	}
	app := AppConfig{
		Command: "npm run dev",
		Port:    3000,
	}

	got := assignAvailablePort(app, snapshot, map[int]bool{3000: true, 3001: true})

	if got.Port != 3002 {
		t.Fatalf("Port = %d, want 3002", got.Port)
	}
	if got.Command != "npm run dev -- -p 3002" {
		t.Fatalf("Command = %q, want matching Next port flag", got.Command)
	}
}

func TestAssignAvailablePortDoesNotMoveUnknownCommands(t *testing.T) {
	snapshot := projectSnapshot{
		Files: map[string]string{
			"package.json": `{"scripts":{"serve":"custom-serve"},"dependencies":{"some-server":"1.0.0"}}`,
		},
	}
	app := AppConfig{
		Command: "npm run serve",
		Port:    3000,
	}

	got := assignAvailablePort(app, snapshot, map[int]bool{3000: true})

	if got.Port != 3000 {
		t.Fatalf("Port = %d, want original port for unknown command", got.Port)
	}
	if got.Command != "npm run serve" {
		t.Fatalf("Command = %q, want original command", got.Command)
	}
}

func TestAssignAvailablePortUpdatesExistingPortEnv(t *testing.T) {
	app := AppConfig{
		Command: "PORT=3000 npm start",
		Port:    3000,
	}

	got := assignAvailablePort(app, projectSnapshot{}, map[int]bool{3000: true})

	if got.Port != 3001 {
		t.Fatalf("Port = %d, want 3001", got.Port)
	}
	if got.Command != "PORT=3001 npm start" {
		t.Fatalf("Command = %q, want updated PORT env", got.Command)
	}
}

func TestAssignAvailablePortAddsPortEnvWhenProjectSupportsPortEnv(t *testing.T) {
	snapshot := projectSnapshot{
		Files: map[string]string{
			"package.json": `{"scripts":{"start":"node server.mjs"}}`,
			"server.mjs":   `const port = Number(process.env.PORT || 3000); server.listen(port);`,
		},
	}
	app := AppConfig{
		Command: "npm start",
		Port:    3000,
	}

	got := assignAvailablePort(app, snapshot, map[int]bool{3000: true})

	if got.Port != 3001 {
		t.Fatalf("Port = %d, want 3001", got.Port)
	}
	if got.Command != "PORT=3001 npm start" {
		t.Fatalf("Command = %q, want command prefixed with PORT", got.Command)
	}
}

func TestAssignAvailableIdentityAvoidsExistingAppID(t *testing.T) {
	app := AppConfig{
		ID:       "headers-app",
		Name:     "Headers App",
		Hostname: "headers-app.localhost",
	}
	existing := []AppConfig{{
		ID:       "headers-app",
		Name:     "Headers App",
		Hostname: "headers-app.localhost",
	}}

	got := assignAvailableIdentity(app, existing)

	if got.ID != "headers-app-2" {
		t.Fatalf("ID = %q, want headers-app-2", got.ID)
	}
	if got.Hostname != "headers-app-2.localhost" {
		t.Fatalf("Hostname = %q, want headers-app-2.localhost", got.Hostname)
	}
}

func TestAssignAvailableIdentityPreservesAvailableHostname(t *testing.T) {
	app := AppConfig{
		ID:       "zoom-oauth-playground",
		Name:     "Zoom OAuth Playground",
		Hostname: "zoom-oauth.localhost",
	}
	existing := []AppConfig{{
		ID:       "zoom-oauth-playground",
		Name:     "Zoom OAuth Playground",
		Hostname: "zoom-oauth-playground.localhost",
	}}

	got := assignAvailableIdentity(app, existing)

	if got.ID != "zoom-oauth-playground-2" {
		t.Fatalf("ID = %q, want zoom-oauth-playground-2", got.ID)
	}
	if got.Hostname != "zoom-oauth.localhost" {
		t.Fatalf("Hostname = %q, want zoom-oauth.localhost", got.Hostname)
	}
}

func TestMergeAIDraftHumanizesSlugName(t *testing.T) {
	base := AppConfig{
		ID:       "headers-app",
		Name:     "Headers App",
		Hostname: "headers-app.localhost",
	}

	got := mergeAIDraft(base, aiDraftResponse{
		ID:   "headers-app",
		Name: "headers-app",
	})

	if got.ID != "headers-app" {
		t.Fatalf("ID = %q, want headers-app", got.ID)
	}
	if got.Name != "Headers App" {
		t.Fatalf("Name = %q, want Headers App", got.Name)
	}
}

func TestHumanizeNameFormatsCommonAcronyms(t *testing.T) {
	tests := map[string]string{
		"jira-ai-ops":           "Jira AI Ops",
		"zoom-oauth-playground": "Zoom OAuth Playground",
		"local-api-proxy":       "Local API Proxy",
		"headers-app":           "Headers App",
		"teely-https-dashboard": "Teely HTTPS Dashboard",
		"macos-url-helper":      "macOS URL Helper",
	}

	for input, want := range tests {
		if got := humanizeName(input); got != want {
			t.Fatalf("humanizeName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestProjectSnapshotIncludesCommonServerEntryFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.mjs"), []byte("process.env.PORT"), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot, err := buildProjectSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}

	if snapshot.Files["server.mjs"] == "" {
		t.Fatal("server.mjs was not included in project snapshot")
	}
}
