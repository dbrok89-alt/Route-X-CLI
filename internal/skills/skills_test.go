package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFindsProjectSkill(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".route-x", "skills")
	_ = os.MkdirAll(skillDir, 0o755)
	_ = os.WriteFile(filepath.Join(skillDir, "lint.md"), []byte("# lint\n"), 0o644)
	_ = os.WriteFile(filepath.Join(skillDir, "notes.txt"), []byte("not a skill"), 0o644)
	t.Setenv("HOME", t.TempDir()) // no global skills
	found := Load(dir)
	if len(found) != 1 || found[0].Name != "lint" {
		t.Fatalf("skills = %+v", found)
	}
}

func TestLoadMissingDirIsNotError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := Load(t.TempDir()); len(got) != 0 {
		t.Fatalf("expected no skills, got %v", got)
	}
}

func TestLoadPlugins(t *testing.T) {
	dir := t.TempDir()
	plugDir := filepath.Join(dir, ".route-x", "plugins")
	_ = os.MkdirAll(plugDir, 0o755)
	_ = os.WriteFile(filepath.Join(plugDir, "deploy.md"), []byte("# deploy\nDeploy the app.\n"), 0o644)
	t.Setenv("HOME", t.TempDir()) // no global plugins
	found := LoadPlugins(dir)
	if len(found) != 1 || found[0].Name != "deploy" {
		t.Fatalf("plugins = %+v", found)
	}
	if len(Load(dir)) != 0 {
		t.Fatal("plugins must not leak into the skills list")
	}
}
