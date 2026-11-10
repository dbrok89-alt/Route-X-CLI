package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndFire(t *testing.T) {
	dir := t.TempDir()
	hookDir := filepath.Join(dir, ".route-x")
	_ = os.MkdirAll(hookDir, 0o755)
	_ = os.WriteFile(filepath.Join(hookDir, "hooks.json"),
		[]byte(`{"PostEdit": "echo edited", "Stop": "echo bye"}`), 0o644)
	t.Setenv("HOME", t.TempDir())
	h := Load(dir)
	if h.Get("PostEdit") != "echo edited" {
		t.Fatalf("PostEdit = %q", h.Get("PostEdit"))
	}
	out, err := h.Fire("PostEdit")
	if err != nil || out != "edited\n" {
		t.Fatalf("fire = %q, %v", out, err)
	}
	if out, err := h.Fire("SessionStart"); err != nil || out != "" {
		t.Fatalf("unset hook should be silent: %q, %v", out, err)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := Load(t.TempDir())
	if len(h.List()) != 0 {
		t.Fatalf("expected no hooks, got %v", h.List())
	}
}
