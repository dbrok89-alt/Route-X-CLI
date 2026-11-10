// Package config loads and saves Route-X settings.
// Stdlib only, no external dependencies. Config lives in
// ~/.route-x/config.json so the single Go binary stays portable.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config mirrors the settings agy keeps in its own config:
// model, execution mode and provider. API keys are NOT stored
// here by default — prefer the GEMINI_API_KEY environment variable.
//
// v0.2.0 adds the Codex split that v0.1.x folded into one Mode:
// Sandbox (what a command may touch) is separate from Approval
// (when Route-X has to ask). It also adds Claude-style allow/deny
// permission lists and a BaseURL so the same OpenAI-compatible
// client can talk to OpenAI, Ollama and any OpenAI-compatible
// endpoint (Hermes-style model freedom).
type Config struct {
	Model    string `json:"model"`
	Mode     string `json:"mode"`     // default | accept-edits | auto | plan
	Provider string `json:"provider"` // local | gemini | openai | anthropic | ollama | intern
	APIKey   string `json:"api_key,omitempty"`
	// Codex-style safety knobs (v0.2.0).
	Sandbox  string   `json:"sandbox"`            // read-only | workspace-write | danger-full-access
	Approval string   `json:"approval"`           // on-request | never
	Allow    []string `json:"allow,omitempty"`    // shell patterns auto-approved
	Deny     []string `json:"deny,omitempty"`     // shell/file patterns always blocked
	BaseURL  string   `json:"base_url,omitempty"` // OpenAI-compatible endpoint
}

// Default returns sane, offline-first defaults.
func Default() Config {
	return Config{
		Model:    "gemini-2.0-flash",
		Mode:     "default",
		Provider: "local",
		Sandbox:  "workspace-write",
		Approval: "on-request",
		Allow:    []string{"go vet ./...", "go test ./..."},
		Deny:     []string{"rm -rf", "git push --force"},
	}
}

// Dir returns ~/.route-x, creating it on demand.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".route-x")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// Path returns the config file path.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config file, returning Default() when it is missing.
func Load() (Config, error) {
	cfg := Default()
	path, err := Path()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	if cfg.Model == "" {
		cfg.Model = Default().Model
	}
	if !ValidMode(cfg.Mode) {
		cfg.Mode = Default().Mode
	}
	if !ValidProvider(cfg.Provider) {
		cfg.Provider = Default().Provider
	}
	if !ValidSandbox(cfg.Sandbox) {
		cfg.Sandbox = Default().Sandbox
	}
	if !ValidApproval(cfg.Approval) {
		cfg.Approval = Default().Approval
	}
	return cfg, nil
}

// Save writes the config file with 0600 permissions.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ValidMode reports whether m is a known execution mode.
func ValidMode(m string) bool {
	switch m {
	case "default", "accept-edits", "auto", "plan":
		return true
	}
	return false
}

// ValidProvider reports whether p is a known provider.
func ValidProvider(p string) bool {
	switch p {
	case "local", "gemini", "openai", "anthropic", "ollama", "intern":
		return true
	}
	return false
}

// DefaultModelFor returns the default model for a provider.
// Atria-Dawn is Intern's flagship agentic model (512K context,
// text-only) — the natural default for a coding agent on the
// Intern discovery API.
func DefaultModelFor(provider string) string {
	switch provider {
	case "openai":
		return "gpt-4o"
	case "anthropic":
		return "claude-sonnet-4"
	case "ollama":
		return "llama3.1"
	case "intern":
		return "Atria-Dawn"
	}
	return "gemini-2.0-flash"
}

// InternBaseURL is the OpenAI-compatible endpoint of Shanghai AI
// Lab's Intern discovery API. The same host without /v1 serves
// Anthropic-compatible clients.
const InternBaseURL = "https://discovery-api.intern-ai.org.cn/v1"

// ValidSandbox reports whether s is a known Codex-style sandbox.
func ValidSandbox(s string) bool {
	switch s {
	case "read-only", "workspace-write", "danger-full-access":
		return true
	}
	return false
}

// ValidApproval reports whether a is a known approval policy.
func ValidApproval(a string) bool {
	switch a {
	case "on-request", "never":
		return true
	}
	return false
}

// EnvAPIKey returns the API key env var for a provider, if set.
// Hermes-style: every provider reads its own env var first.
func EnvAPIKey(provider string) string {
	switch provider {
	case "gemini":
		return os.Getenv("GEMINI_API_KEY")
	case "openai":
		return os.Getenv("OPENAI_API_KEY")
	case "anthropic":
		return os.Getenv("ANTHROPIC_API_KEY")
	case "ollama":
		return os.Getenv("OLLAMA_API_KEY")
	case "intern":
		return os.Getenv("INTERN_API_KEY")
	}
	return ""
}

// MaskedAPIKey returns the API key with all but the last 4 chars hidden.
func (c Config) MaskedAPIKey() string {
	if c.APIKey == "" {
		return "(not set — use GEMINI_API_KEY)"
	}
	if len(c.APIKey) <= 4 {
		return "****"
	}
	return "****" + c.APIKey[len(c.APIKey)-4:]
}
