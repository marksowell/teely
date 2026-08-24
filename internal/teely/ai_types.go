package teely

type appImportDraft struct {
	App     AppConfig
	Message string
	Path    string
}

type projectSnapshot struct {
	Path          string
	ProjectName   string
	TopLevelFiles []string
	Files         map[string]string
}

type packageJSONSnapshot struct {
	Name            string            `json:"name"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

type aiDraftResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Hostname       string `json:"hostname"`
	Command        string `json:"command"`
	Port           int    `json:"port"`
	HealthPath     string `json:"health_path"`
	HealthMethod   string `json:"health_method"`
	IdleTimeout    string `json:"idle_timeout"`
	StartupTimeout string `json:"startup_timeout"`
}

type AIProviderOption struct {
	ID    string
	Label string
}

type AIModelOption struct {
	ID string
}
