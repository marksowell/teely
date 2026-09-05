package teely

import "testing"

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
