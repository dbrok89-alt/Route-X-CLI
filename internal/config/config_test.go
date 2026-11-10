package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Provider != "local" || cfg.Mode != "default" {
		t.Fatalf("default = %+v", cfg)
	}
	if !ValidMode(cfg.Mode) || !ValidProvider(cfg.Provider) {
		t.Fatal("default config is not valid")
	}
}

func TestValidModeAndProvider(t *testing.T) {
	for _, m := range []string{"default", "accept-edits", "auto", "plan"} {
		if !ValidMode(m) {
			t.Fatalf("ValidMode(%q) = false", m)
		}
	}
	if ValidMode("yolo") || ValidProvider("nope") {
		t.Fatal("unknown mode/provider accepted")
	}
	for _, p := range []string{"local", "gemini", "openai", "anthropic", "ollama"} {
		if !ValidProvider(p) {
			t.Fatalf("ValidProvider(%q) = false", p)
		}
	}
}

func TestLoadFallsBackOnInvalidSandbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, ".route-x"), 0o700)
	bad := `{"model":"m","mode":"default","provider":"local","sandbox":"yolo","approval":"always"}`
	if err := os.WriteFile(filepath.Join(home, ".route-x", "config.json"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox != "workspace-write" || cfg.Approval != "on-request" {
		t.Fatalf("invalid sandbox/approval not reset: %q / %q", cfg.Sandbox, cfg.Approval)
	}
}

func TestValidSandboxAndApproval(t *testing.T) {
	for _, s := range []string{"read-only", "workspace-write", "danger-full-access"} {
		if !ValidSandbox(s) {
			t.Fatalf("ValidSandbox(%q) = false", s)
		}
	}
	if ValidSandbox("yolo") || ValidApproval("always") {
		t.Fatal("unknown sandbox/approval accepted")
	}
	if EnvAPIKey("gemini") != "" {
		t.Fatal("EnvAPIKey should be empty without env var")
	}
	t.Setenv("OPENAI_API_KEY", "sk-test")
	if EnvAPIKey("openai") != "sk-test" {
		t.Fatal("EnvAPIKey(openai) did not read env var")
	}
}

func TestMaskedAPIKey(t *testing.T) {
	if c := (Config{}).MaskedAPIKey(); c == "" || c == "secret" {
		t.Fatalf("empty key masked = %q", c)
	}
	if got := (Config{APIKey: "abcdefgh"}).MaskedAPIKey(); got != "****efgh" {
		t.Fatalf("masked = %q, want ****efgh", got)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := Default()
	cfg.Model = "test-model"
	cfg.Mode = "plan"
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".route-x", "config.json")); err != nil {
		t.Fatal("config file not written:", err)
	}
	loaded, err := Load()
	if err != nil || loaded.Model != "test-model" || loaded.Mode != "plan" {
		t.Fatalf("loaded = %+v, err=%v", loaded, err)
	}
}

func TestLoadInvalidModeFallsBackToDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".route-x")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"model":"","mode":"yolo","provider":"nope"}`), 0o600)
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Mode != "default" || loaded.Provider != "local" || loaded.Model == "" {
		t.Fatalf("invalid config not sanitised: %+v", loaded)
	}
}
