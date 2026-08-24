package teely

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type AISetupState struct {
	Provider     string
	Model        string
	KeyStored    bool
	KeySource    string
	Enabled      bool
	StatusLabel  string
	StatusClass  string
	ConfigError  string
	ModelOptions []AIModelOption
	ModelError   string
}

func supportedAIProviders() []AIProviderOption {
	return []AIProviderOption{
		{ID: "openai", Label: "OpenAI"},
		{ID: "anthropic", Label: "Anthropic"},
		{ID: "google", Label: "Google"},
	}
}

func aiProviderLabel(providerID string) string {
	for _, provider := range supportedAIProviders() {
		if provider.ID == providerID {
			return provider.Label
		}
	}
	switch providerID {
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "google":
		return "Google"
	default:
		return humanizeName(providerID)
	}
}

func normalizeAIConfig(ai AIConfig) (AIConfig, error) {
	ai.Provider = strings.ToLower(strings.TrimSpace(ai.Provider))
	ai.Model = strings.TrimSpace(ai.Model)
	if ai.Provider == "" && ai.Model == "" {
		return AIConfig{}, nil
	}
	if ai.Provider == "" && ai.Model != "" {
		return AIConfig{}, errors.New("AI provider must be set before saving a model")
	}
	valid := false
	for _, provider := range supportedAIProviders() {
		if provider.ID == ai.Provider {
			valid = true
			break
		}
	}
	if !valid {
		return AIConfig{}, fmt.Errorf("unsupported AI provider %q", ai.Provider)
	}
	return ai, nil
}

func (m *Manager) UpdateAIConfig(ai AIConfig) error {
	normalized, err := normalizeAIConfig(ai)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := cloneConfig(m.config)
	next.AI = normalized
	return m.commitConfigLocked(next)
}

func (m *Manager) activeAIProvider() (AIProviderOption, string, string, bool, error) {
	cfg := m.Config()
	ai, err := normalizeAIConfig(cfg.AI)
	if err != nil {
		return AIProviderOption{}, "", "", false, err
	}
	if ai.Provider == "" {
		return AIProviderOption{}, "", "", false, nil
	}
	key, _, ok, err := lookupAIKey(ai.Provider)
	if err != nil {
		return AIProviderOption{}, "", "", false, err
	}
	if !ok {
		return AIProviderOption{}, "", "", false, nil
	}
	return AIProviderOption{ID: ai.Provider, Label: aiProviderLabel(ai.Provider)}, ai.Model, key, true, nil
}

func buildAISetupState(cfg Config) AISetupState {
	ai, err := normalizeAIConfig(cfg.AI)
	if err != nil {
		return AISetupState{
			StatusLabel: "Missing",
			StatusClass: "stopped",
			ConfigError: err.Error(),
		}
	}
	if ai.Provider == "" {
		return AISetupState{
			StatusLabel: "Missing",
			StatusClass: "stopped",
		}
	}
	key, source, ok, keyErr := lookupAIKey(ai.Provider)
	state := AISetupState{
		Provider:  ai.Provider,
		Model:     ai.Model,
		KeyStored: ok && key != "",
		KeySource: source,
	}
	if keyErr != nil {
		state.StatusLabel = "Missing"
		state.StatusClass = "stopped"
		state.ConfigError = keyErr.Error()
		return state
	}
	if ok && key != "" {
		if ai.Model != "" {
			state.Enabled = true
			state.StatusLabel = "Ready"
			state.StatusClass = "running"
		} else {
			state.StatusLabel = "Missing"
			state.StatusClass = "stopped"
		}
		return state
	}
	state.StatusLabel = "Missing"
	state.StatusClass = "stopped"
	return state
}

func (m *Manager) modelOptionsSnapshot(provider string) ([]AIModelOption, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var options []AIModelOption
	for _, option := range m.aiModelOptions[provider] {
		options = append(options, option)
	}
	return options, m.aiModelErrors[provider]
}

func (m *Manager) setModelOptions(provider string, options []AIModelOption, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.aiModelOptions == nil {
		m.aiModelOptions = map[string][]AIModelOption{}
	}
	if m.aiModelErrors == nil {
		m.aiModelErrors = map[string]string{}
	}
	copied := make([]AIModelOption, len(options))
	copy(copied, options)
	m.aiModelOptions[provider] = copied
	m.aiModelErrors[provider] = strings.TrimSpace(errMsg)
}

func aiKeychainService(provider string) string {
	return "com.marksowell.teely.ai." + provider
}

func lookupAIKey(provider string) (string, string, bool, error) {
	if value := lookupAIEnv(provider); value != "" {
		return value, "environment", true, nil
	}
	if runtime.GOOS != "darwin" {
		return "", "", false, nil
	}
	cmd := exec.Command("/usr/bin/security", "find-generic-password", "-a", "teely", "-s", aiKeychainService(provider), "-w")
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.ToLower(strings.TrimSpace(string(output)))
		if strings.Contains(text, "could not be found") || strings.Contains(text, "item could not be found") {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("keychain lookup failed for %s", aiProviderLabel(provider))
	}
	return strings.TrimSpace(string(output)), "keychain", true, nil
}

func saveAIKey(provider, key string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("AI key storage currently requires macOS Keychain")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("API key is required")
	}
	cmd := exec.Command("/usr/bin/security", "add-generic-password", "-U", "-a", "teely", "-s", aiKeychainService(provider), "-w", key)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf(strings.TrimSpace(stderr.String()))
		}
		return err
	}
	return nil
}

func deleteAIKey(provider string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	cmd := exec.Command("/usr/bin/security", "delete-generic-password", "-a", "teely", "-s", aiKeychainService(provider))
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.ToLower(strings.TrimSpace(string(output)))
		if strings.Contains(text, "could not be found") || strings.Contains(text, "item could not be found") {
			return nil
		}
		return err
	}
	return nil
}

func lookupAIEnv(provider string) string {
	switch provider {
	case "openai":
		return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	case "anthropic":
		return strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	case "google":
		if value := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); value != "" {
			return value
		}
		return strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	default:
		return ""
	}
}
