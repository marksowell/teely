package teely

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	readmeFiles = []string{
		"README.md",
		"readme.md",
		"README.txt",
	}
	composeFiles = []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}
	projectSnapshotFiles = []string{
		"README.md",
		"README.txt",
		"readme.md",
		"package.json",
		"pyproject.toml",
		"requirements.txt",
		"Procfile",
		"Makefile",
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
		".env.example",
		"scripts/run-webapp.sh",
		"scripts/start.sh",
		"start.sh",
		"run.sh",
		"bin/dev",
		"bin/start",
	}
	projectContentFiles = []string{
		"README.md",
		"readme.md",
		"README.txt",
		"package.json",
		"pyproject.toml",
		"requirements.txt",
		"Procfile",
		"Makefile",
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
		".env.example",
	}
	importPromptFiles = []string{
		"README.md",
		"readme.md",
		"README.txt",
		"package.json",
		"pyproject.toml",
		"requirements.txt",
		"Procfile",
		"Makefile",
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
		"scripts/run-webapp.sh",
		"scripts/start.sh",
		"start.sh",
		"bin/dev",
	}
	directCommandFiles = []string{
		"scripts/run-webapp.sh",
		"scripts/start.sh",
		"start.sh",
		"run.sh",
		"bin/dev",
		"bin/start",
	}
	aiHTTPClient = &http.Client{Timeout: 30 * time.Second}
)

func (m *Manager) DraftAppFromProject(ctx context.Context, projectPath string) (appImportDraft, error) {
	provider, model, apiKey, ok, err := m.activeAIProvider()
	if err != nil {
		return appImportDraft{}, err
	}
	if !ok {
		return appImportDraft{}, errors.New("AI import is not configured")
	}

	absPath, err := filepath.Abs(strings.TrimSpace(projectPath))
	if err != nil {
		return appImportDraft{}, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return appImportDraft{}, err
	}
	if !info.IsDir() {
		return appImportDraft{}, fmt.Errorf("%s is not a directory", absPath)
	}

	snapshot, err := buildProjectSnapshot(absPath)
	if err != nil {
		return appImportDraft{}, err
	}

	base := heuristicAppDraft(snapshot, m.Config().ListenAddress)
	aiResult, err := generateAIDraft(ctx, provider.ID, model, apiKey, snapshot, base)
	if err != nil {
		return appImportDraft{}, err
	}

	draft := appImportDraft{
		App:     mergeAIDraft(base, aiResult),
		Message: fmt.Sprintf("Drafted from %s using %s.", absPath, provider.Label),
		Path:    absPath,
	}

	normalized, err := normalizeNewApp(m.configPath, draft.App)
	if err != nil {
		if strings.TrimSpace(draft.App.Command) == "" {
			draft.App.Command = ""
		}
		if draft.App.Port <= 0 {
			draft.App.Port = base.Port
		}
		normalized, err = normalizeNewApp(m.configPath, draft.App)
		if err != nil {
			return appImportDraft{}, err
		}
	}
	draft.App = normalized
	return draft, nil
}

func buildProjectSnapshot(projectPath string) (projectSnapshot, error) {
	entries, err := os.ReadDir(projectPath)
	if err != nil {
		return projectSnapshot{}, err
	}
	var topLevel []string
	for _, entry := range entries {
		topLevel = append(topLevel, entry.Name())
	}
	sort.Strings(topLevel)

	files := make(map[string]string, len(projectSnapshotFiles))
	for _, rel := range projectSnapshotFiles {
		path := filepath.Join(projectPath, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		files[rel] = clampString(string(data), fileContentLimit(rel))
	}

	return projectSnapshot{
		Path:          projectPath,
		ProjectName:   filepath.Base(projectPath),
		TopLevelFiles: topLevel,
		Files:         files,
	}, nil
}

func fileContentLimit(name string) int {
	switch strings.ToLower(filepath.Base(name)) {
	case "readme.md", "readme.txt":
		return 18000
	case "package.json", "pyproject.toml", "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml":
		return 12000
	default:
		return 6000
	}
}

func clampString(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func heuristicAppDraft(snapshot projectSnapshot, listenAddress string) AppConfig {
	name := humanizeName(snapshot.ProjectName)
	id := slugify(snapshot.ProjectName)
	command := detectCommand(snapshot)
	port := detectPort(snapshot, command)
	if port <= 0 {
		port = 3000
	}

	if pkg := parsePackageJSON(snapshot.Files["package.json"]); pkg.Name != "" {
		name = humanizeName(trimPackageName(pkg.Name))
		if slug := slugify(trimPackageName(pkg.Name)); slug != "" {
			id = slug
		}
	}

	if readmeName := detectNameFromReadme(snapshot.Files); readmeName != "" && isLikelyAppName(readmeName, id) {
		name = readmeName
	}

	if id == "" {
		id = "sample-app"
	}
	hostname := id + ".localhost"
	return AppConfig{
		ID:              id,
		Name:            name,
		Hostname:        hostname,
		WorkingDir:      snapshot.Path,
		Command:         command,
		Port:            port,
		HealthPath:      detectHealthPath(snapshot),
		HealthMethod:    "GET",
		IdleTimeout:     "10m",
		StartupTimeout:  "90s",
		CaddyDirectives: defaultCaddyDirectives(listenAddress),
	}
}

func detectCommand(snapshot projectSnapshot) string {
	for _, rel := range directCommandFiles {
		if _, ok := snapshot.Files[rel]; ok {
			return "./" + rel
		}
	}

	if procfile := strings.TrimSpace(snapshot.Files["Procfile"]); procfile != "" {
		for _, line := range strings.Split(procfile, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "web:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "web:"))
			}
		}
	}

	if pkg := parsePackageJSON(snapshot.Files["package.json"]); len(pkg.Scripts) > 0 {
		pm := detectPackageManager(snapshot)
		switch {
		case pkg.Scripts["start"] != "":
			if pm == "yarn" || pm == "bun" {
				return pm + " start"
			}
			return pm + " start"
		case pkg.Scripts["dev"] != "":
			if pm == "yarn" {
				return "yarn dev"
			}
			if pm == "bun" {
				return "bun run dev"
			}
			return pm + " run dev"
		}
	}

	if content := combinedFileText(snapshot); content != "" {
		for _, candidate := range []string{
			"./scripts/run-webapp.sh",
			"./scripts/start.sh",
			"./start.sh",
			"./bin/dev",
			"npm start",
			"npm run dev",
			"pnpm dev",
			"pnpm start",
			"yarn dev",
			"yarn start",
			"python app.py",
			"python server.py",
			"uv run python app.py",
			"docker compose up",
		} {
			if strings.Contains(content, candidate) {
				return candidate
			}
		}
	}

	if _, ok := snapshot.Files["docker-compose.yml"]; ok {
		return "docker compose up"
	}
	if _, ok := snapshot.Files["docker-compose.yaml"]; ok {
		return "docker compose up"
	}
	if _, ok := snapshot.Files["compose.yml"]; ok {
		return "docker compose up"
	}
	if _, ok := snapshot.Files["compose.yaml"]; ok {
		return "docker compose up"
	}

	if _, ok := snapshot.Files["pyproject.toml"]; ok {
		if _, hasReq := snapshot.Files["requirements.txt"]; hasReq {
			return "python app.py"
		}
		return "python server.py"
	}
	return ""
}

func detectPackageManager(snapshot projectSnapshot) string {
	switch {
	case containsName(snapshot.TopLevelFiles, "pnpm-lock.yaml"):
		return "pnpm"
	case containsName(snapshot.TopLevelFiles, "yarn.lock"):
		return "yarn"
	case containsPrefix(snapshot.TopLevelFiles, "bun.lock"):
		return "bun"
	default:
		return "npm"
	}
}

func detectPort(snapshot projectSnapshot, command string) int {
	content := combinedFileText(snapshot)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`localhost:(\d{2,5})`),
		regexp.MustCompile(`127\.0\.0\.1:(\d{2,5})`),
		regexp.MustCompile(`(?i)\bport["'\s:=]+(\d{2,5})\b`),
		regexp.MustCompile(`(?i)\bPORT=(\d{2,5})\b`),
	}
	for _, re := range patterns {
		if matches := re.FindAllStringSubmatch(content, -1); len(matches) > 0 {
			for _, match := range matches {
				if port, err := strconv.Atoi(match[1]); err == nil && port > 0 {
					return port
				}
			}
		}
	}

	pkg := parsePackageJSON(snapshot.Files["package.json"])
	for dep := range mergedDeps(pkg) {
		switch dep {
		case "vite":
			return 5173
		case "next", "react-scripts", "express", "fastify", "nuxt":
			return 3000
		}
	}

	if strings.Contains(command, "docker compose") {
		if port := firstComposePort(snapshot); port > 0 {
			return port
		}
	}
	if _, ok := snapshot.Files["pyproject.toml"]; ok {
		return 8000
	}
	return 0
}

func detectHealthPath(snapshot projectSnapshot) string {
	content := combinedFileText(snapshot)
	for _, candidate := range []string{"/healthz", "/health", "/readyz", "/ready", "/status"} {
		if strings.Contains(content, candidate) {
			return candidate
		}
	}
	return "/"
}

func detectNameFromReadme(files map[string]string) string {
	for _, key := range readmeFiles {
		content := strings.TrimSpace(files[key])
		if content == "" {
			continue
		}
		for _, line := range strings.Split(content, "\n") {
			line = normalizeAppNameCandidate(line)
			if line == "" {
				continue
			}
			if len(line) > 80 {
				continue
			}
			return line
		}
	}
	return ""
}

func isLikelyAppName(value, appID string) bool {
	value = normalizeAppNameCandidate(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "how to ") ||
		strings.HasPrefix(lower, "create ") ||
		strings.HasPrefix(lower, "build ") ||
		strings.HasPrefix(lower, "learn ") ||
		strings.HasPrefix(lower, "getting started") ||
		strings.HasPrefix(lower, "introduction") ||
		strings.HasPrefix(lower, "quick start") ||
		strings.HasPrefix(lower, "create a new ") ||
		strings.HasPrefix(lower, "step ") ||
		strings.Contains(lower, "tutorial") {
		return false
	}
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "*") {
		return false
	}
	if len(value) > 48 && !strings.EqualFold(slugify(value), strings.TrimSpace(appID)) {
		return false
	}
	if strings.Contains(value, ".") && !strings.Contains(value, " ") {
		return false
	}
	return true
}

func normalizeAppNameCandidate(value string) string {
	value = strings.TrimSpace(strings.Trim(value, `"'`))
	value = strings.TrimSpace(strings.TrimLeft(value, "#"))
	value = regexp.MustCompile(`^\d+\.\s+`).ReplaceAllString(value, "")
	value = regexp.MustCompile(`^[-*+]\s+`).ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "**", "")
	value = strings.ReplaceAll(value, "__", "")
	value = strings.ReplaceAll(value, "`", "")
	return strings.TrimSpace(value)
}

func parsePackageJSON(content string) packageJSONSnapshot {
	if strings.TrimSpace(content) == "" {
		return packageJSONSnapshot{}
	}
	var pkg packageJSONSnapshot
	_ = json.Unmarshal([]byte(content), &pkg)
	return pkg
}

func mergedDeps(pkg packageJSONSnapshot) map[string]struct{} {
	out := map[string]struct{}{}
	for dep := range pkg.Dependencies {
		out[dep] = struct{}{}
	}
	for dep := range pkg.DevDependencies {
		out[dep] = struct{}{}
	}
	return out
}

func firstComposePort(snapshot projectSnapshot) int {
	for _, key := range composeFiles {
		content := snapshot.Files[key]
		if content == "" {
			continue
		}
		re := regexp.MustCompile(`(?m)\b(\d{2,5}):\d{2,5}\b`)
		if match := re.FindStringSubmatch(content); len(match) == 2 {
			port, _ := strconv.Atoi(match[1])
			return port
		}
	}
	return 0
}

func combinedFileText(snapshot projectSnapshot) string {
	var b strings.Builder
	for _, key := range projectContentFiles {
		if text := strings.TrimSpace(snapshot.Files[key]); text != "" {
			b.WriteString(text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func mergeAIDraft(base AppConfig, ai aiDraftResponse) AppConfig {
	out := base
	if value := strings.TrimSpace(ai.ID); value != "" {
		out.ID = slugify(value)
	}
	if value := strings.TrimSpace(ai.Name); value != "" && isLikelyAppName(value, out.ID) {
		out.Name = value
	}
	if value := strings.TrimSpace(ai.Hostname); value != "" {
		out.Hostname = strings.ToLower(value)
	}
	if value := strings.TrimSpace(ai.Command); value != "" {
		out.Command = value
	}
	if ai.Port > 0 {
		out.Port = ai.Port
	}
	if value := strings.TrimSpace(ai.HealthPath); value != "" {
		out.HealthPath = value
	}
	if value := strings.TrimSpace(ai.HealthMethod); value != "" {
		out.HealthMethod = strings.ToUpper(value)
	}
	if value := strings.TrimSpace(ai.IdleTimeout); value != "" {
		out.IdleTimeout = value
	}
	if value := strings.TrimSpace(ai.StartupTimeout); value != "" {
		out.StartupTimeout = value
	}
	if out.ID == "" {
		out.ID = slugify(out.Name)
	}
	if strings.TrimSpace(out.Hostname) == "" && out.ID != "" {
		out.Hostname = out.ID + ".localhost"
	}
	return out
}

var errAIProviderUnavailable = errors.New("ai provider unavailable")

func fetchModelOptions(ctx context.Context, providerID, apiKey string) ([]AIModelOption, error) {
	switch providerID {
	case "openai":
		return fetchOpenAIModelOptions(ctx, apiKey)
	case "anthropic":
		return fetchAnthropicModelOptions(ctx, apiKey)
	case "google":
		return fetchGoogleModelOptions(ctx, apiKey)
	default:
		return nil, errAIProviderUnavailable
	}
}

func generateAIDraft(ctx context.Context, providerID, model, apiKey string, snapshot projectSnapshot, base AppConfig) (aiDraftResponse, error) {
	switch providerID {
	case "openai":
		return generateWithOpenAI(ctx, model, apiKey, snapshot, base)
	case "anthropic":
		return generateWithAnthropic(ctx, model, apiKey, snapshot, base)
	case "google":
		return generateWithGoogle(ctx, model, apiKey, snapshot, base)
	default:
		return aiDraftResponse{}, errAIProviderUnavailable
	}
}

func generateWithOpenAI(ctx context.Context, model, apiKey string, snapshot projectSnapshot, base AppConfig) (aiDraftResponse, error) {
	if strings.TrimSpace(apiKey) == "" {
		return aiDraftResponse{}, errAIProviderUnavailable
	}
	textFormat := map[string]any{
		"type": "json_object",
	}
	if openAIShouldUseJSONSchema(model) {
		textFormat = map[string]any{
			"type":   "json_schema",
			"name":   "teely_app_import",
			"strict": true,
			"schema": appDraftJSONSchema(),
		}
	}
	body := map[string]any{
		"model":        model,
		"instructions": importSystemPrompt(),
		"input":        importUserPrompt(snapshot, base),
		"text": map[string]any{
			"format": textFormat,
		},
	}
	var response openAIResponse
	if err := postJSON(ctx, "https://api.openai.com/v1/responses", map[string]string{
		"Authorization": "Bearer " + apiKey,
	}, body, &response); err != nil {
		if !openAIStructuredOutputUnsupported(err) {
			return aiDraftResponse{}, err
		}
		fallbackBody := map[string]any{
			"model":        model,
			"instructions": importSystemPrompt() + "\n\nReturn only one JSON object matching the requested schema. Do not include markdown fences or any surrounding explanation.",
			"input":        importUserPrompt(snapshot, base),
		}
		if fallbackErr := postJSON(ctx, "https://api.openai.com/v1/responses", map[string]string{
			"Authorization": "Bearer " + apiKey,
		}, fallbackBody, &response); fallbackErr != nil {
			return aiDraftResponse{}, fallbackErr
		}
	}
	return parseAIDraftJSON(response.text())
}

type openAIResponse struct {
	OutputText string `json:"output_text"`
	Output     []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func (r openAIResponse) text() string {
	if strings.TrimSpace(r.OutputText) != "" {
		return r.OutputText
	}
	var parts []string
	for _, item := range r.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, content.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func openAIShouldUseJSONSchema(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "gpt-5"),
		strings.HasPrefix(model, "gpt-4.1"),
		strings.HasPrefix(model, "gpt-4o"),
		strings.HasPrefix(model, "o1"),
		strings.HasPrefix(model, "o3"),
		strings.HasPrefix(model, "o4"):
		return true
	default:
		return false
	}
}

func openAIStructuredOutputUnsupported(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "json_schema") &&
		(strings.Contains(message, "not supported") || strings.Contains(message, "invalid parameter")) &&
		(strings.Contains(message, "text.format") || strings.Contains(message, "response_format"))
}

func generateWithAnthropic(ctx context.Context, model, apiKey string, snapshot projectSnapshot, base AppConfig) (aiDraftResponse, error) {
	if strings.TrimSpace(apiKey) == "" {
		return aiDraftResponse{}, errAIProviderUnavailable
	}
	body := map[string]any{
		"model":       model,
		"max_tokens":  900,
		"temperature": 0,
		"system":      importSystemPrompt(),
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": importUserPrompt(snapshot, base)},
				},
			},
		},
	}
	var response struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := postJSON(ctx, "https://api.anthropic.com/v1/messages", map[string]string{
		"x-api-key":         apiKey,
		"anthropic-version": "2023-06-01",
	}, body, &response); err != nil {
		return aiDraftResponse{}, err
	}
	var textParts []string
	for _, part := range response.Content {
		if strings.TrimSpace(part.Text) != "" {
			textParts = append(textParts, part.Text)
		}
	}
	return parseAIDraftJSON(strings.Join(textParts, "\n"))
}

func generateWithGoogle(ctx context.Context, model, apiKey string, snapshot projectSnapshot, base AppConfig) (aiDraftResponse, error) {
	if strings.TrimSpace(apiKey) == "" {
		return aiDraftResponse{}, errAIProviderUnavailable
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, apiKey)
	body := map[string]any{
		"system_instruction": map[string]any{
			"parts": []map[string]any{{"text": importSystemPrompt()}},
		},
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": []map[string]any{{"text": importUserPrompt(snapshot, base)}},
			},
		},
		"generationConfig": map[string]any{
			"temperature":      0,
			"responseMimeType": "application/json",
		},
	}
	var response struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := postJSON(ctx, url, nil, body, &response); err != nil {
		return aiDraftResponse{}, err
	}
	if len(response.Candidates) == 0 || len(response.Candidates[0].Content.Parts) == 0 {
		return aiDraftResponse{}, errors.New("google returned no candidates")
	}
	return parseAIDraftJSON(response.Candidates[0].Content.Parts[0].Text)
}

func fetchOpenAIModelOptions(ctx context.Context, apiKey string) ([]AIModelOption, error) {
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, "https://api.openai.com/v1/models", map[string]string{
		"Authorization": "Bearer " + apiKey,
	}, &response); err != nil {
		return nil, err
	}
	return normalizeModelOptions(response.Data, func(item struct {
		ID string `json:"id"`
	}) string {
		return item.ID
	}), nil
}

func fetchAnthropicModelOptions(ctx context.Context, apiKey string) ([]AIModelOption, error) {
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, "https://api.anthropic.com/v1/models", map[string]string{
		"x-api-key":         apiKey,
		"anthropic-version": "2023-06-01",
	}, &response); err != nil {
		return nil, err
	}
	return normalizeModelOptions(response.Data, func(item struct {
		ID string `json:"id"`
	}) string {
		return item.ID
	}), nil
}

func fetchGoogleModelOptions(ctx context.Context, apiKey string) ([]AIModelOption, error) {
	var response struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", apiKey)
	if err := getJSON(ctx, url, nil, &response); err != nil {
		return nil, err
	}
	options := make([]AIModelOption, 0, len(response.Models))
	seen := map[string]bool{}
	for _, model := range response.Models {
		id := strings.TrimPrefix(strings.TrimSpace(model.Name), "models/")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		options = append(options, AIModelOption{ID: id})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
	return options, nil
}

func normalizeModelOptions[T any](items []T, getID func(T) string) []AIModelOption {
	options := make([]AIModelOption, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		id := strings.TrimSpace(getID(item))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		options = append(options, AIModelOption{ID: id})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
	return options
}

func postJSON(ctx context.Context, url string, headers map[string]string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := aiHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ai request failed: %s", strings.TrimSpace(string(payload)))
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return err
	}
	return nil
}

func getJSON(ctx context.Context, url string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := aiHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ai request failed: %s", strings.TrimSpace(string(payload)))
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return err
	}
	return nil
}

func parseAIDraftJSON(raw string) (aiDraftResponse, error) {
	raw = extractJSONObject(raw)
	if strings.TrimSpace(raw) == "" {
		return aiDraftResponse{}, errors.New("ai response did not contain JSON")
	}
	var draft aiDraftResponse
	if err := json.Unmarshal([]byte(raw), &draft); err != nil {
		return aiDraftResponse{}, err
	}
	return draft, nil
}

func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(raw, "```")
		raw = strings.TrimSpace(raw)
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end < start {
		return raw
	}
	return raw[start : end+1]
}

func importSystemPrompt() string {
	return strings.TrimSpace(`
You draft Teely app registrations for working local web apps.

Only infer how Teely should launch an app that already works outside Teely.
Use the project directory name, README, package.json, Procfile, pyproject.toml, compose files, and scripts as hints.
Prefer the directory name or package name for the app name unless the README clearly states a real product name.
Do not use tutorial headings, numbered steps, or setup instructions as the app name.

Return exactly one JSON object.
Do not explain your answer.
Do not include markdown fences.
Prefer:
- .localhost hostnames
- GET for health_method
- / for health_path unless a clearer health path exists
- 10m idle_timeout
- 90s startup_timeout

The command must be something Teely can run directly from the app's working directory.
`)
}

func importUserPrompt(snapshot projectSnapshot, base AppConfig) string {
	var b strings.Builder
	b.WriteString("Draft a Teely app registration for this project.\n\n")
	fmt.Fprintf(&b, "Project path: %s\n", snapshot.Path)
	fmt.Fprintf(&b, "Directory name: %s\n", snapshot.ProjectName)
	fmt.Fprintf(&b, "Top-level files: %s\n\n", strings.Join(snapshot.TopLevelFiles, ", "))
	b.WriteString("Current heuristic draft:\n")
	data, _ := json.MarshalIndent(base, "", "  ")
	b.Write(data)
	b.WriteString("\n\nRelevant file excerpts:\n")
	for _, key := range importPromptFiles {
		value := strings.TrimSpace(snapshot.Files[key])
		if value == "" {
			continue
		}
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", key, value)
	}
	return b.String()
}

func appDraftJSONSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id":              map[string]any{"type": "string"},
			"name":            map[string]any{"type": "string"},
			"hostname":        map[string]any{"type": "string"},
			"command":         map[string]any{"type": "string"},
			"port":            map[string]any{"type": "integer"},
			"health_path":     map[string]any{"type": "string"},
			"health_method":   map[string]any{"type": "string"},
			"idle_timeout":    map[string]any{"type": "string"},
			"startup_timeout": map[string]any{"type": "string"},
		},
		"required": []string{"id", "name", "hostname", "command", "port", "health_path", "health_method", "idle_timeout", "startup_timeout"},
	}
}

func slugify(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return ""
	}
	return out
}

func humanizeName(value string) string {
	value = trimPackageName(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, "-", " ")
	fields := strings.Fields(value)
	for i, field := range fields {
		fields[i] = strings.ToUpper(field[:1]) + field[1:]
	}
	if len(fields) == 0 {
		return "Sample App"
	}
	return strings.Join(fields, " ")
}

func trimPackageName(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "@") && strings.Contains(value, "/") {
		parts := strings.Split(value, "/")
		return parts[len(parts)-1]
	}
	return value
}

func containsName(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
