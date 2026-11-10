// Package skills loads reusable Agent Skills from Markdown files,
// mirroring agy: drop a file at .route-x/skills/lint.md and it
// becomes a skill the agent can list and invoke.
package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// Skill is one Markdown skill file.
type Skill struct {
	Name    string
	Path    string
	Content string
}

// LoadPlugins reads *.md files from <root>/.route-x/plugins and
// ~/.route-x/plugins. A plugin is a skill with a command of its
// own: its first heading is the command name, the body is the
// instruction the agent follows. Missing directories are not an
// error.
func LoadPlugins(root string) []Skill {
	var dirs []string
	if root != "" {
		dirs = append(dirs, filepath.Join(root, ".route-x", "plugins"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".route-x", "plugins"))
	}
	var out []Skill
	seen := map[string]bool{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if seen[name] {
				continue
			}
			seen[name] = true
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			out = append(out, Skill{Name: name, Path: filepath.Join(dir, e.Name()), Content: string(data)})
		}
	}
	return out
}

// Load reads *.md files from <root>/.route-x/skills and
// ~/.route-x/skills. Missing directories are not an error.
func Load(root string) []Skill {
	var dirs []string
	if root != "" {
		dirs = append(dirs, filepath.Join(root, ".route-x", "skills"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".route-x", "skills"))
	}
	var out []Skill
	seen := map[string]bool{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if seen[name] {
				continue
			}
			seen[name] = true
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			out = append(out, Skill{Name: name, Path: filepath.Join(dir, e.Name()), Content: string(data)})
		}
	}
	return out
}
