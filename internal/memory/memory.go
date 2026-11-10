// Package memory implements the Hermes-style persistent memory
// loop: bounded MEMORY.md and USER.md files in ~/.route-x, plus
// full-text search over saved sessions. Memory is intentionally
// bounded (about 800 tokens for MEMORY.md, 500 for USER.md in
// Hermes; here approximated as 3200 / 2000 chars) so it stays
// small enough to load into every session.
package memory

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// MaxMemoryChars approximates Hermes' ~800-token MEMORY.md bound.
	MaxMemoryChars = 3200
	// MaxUserChars approximates Hermes' ~500-token USER.md bound.
	MaxUserChars = 2000
)

// Dir returns ~/.route-x/memory, creating it on demand.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".route-x", "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func path(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// Read returns the contents of MEMORY.md or USER.md, or "" if missing.
func Read(name string) string {
	p, err := path(name)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(data)
}

// Remember appends a dated fact to MEMORY.md, keeping it bounded
// by dropping the oldest lines first. Returns the new content.
func Remember(fact string) (string, error) {
	p, err := path("MEMORY.md")
	if err != nil {
		return "", err
	}
	existing := Read("MEMORY.md")
	line := "- " + time.Now().Format("2006-01-02") + ": " + strings.TrimSpace(fact) + "\n"
	content := existing + line
	// Bound the file: drop oldest lines until it fits.
	for len(content) > MaxMemoryChars {
		idx := strings.Index(content, "\n")
		if idx < 0 {
			break
		}
		content = content[idx+1:]
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		return "", err
	}
	return content, nil
}

// RememberUser appends a dated preference to USER.md, bounded.
func RememberUser(fact string) (string, error) {
	p, err := path("USER.md")
	if err != nil {
		return "", err
	}
	existing := Read("USER.md")
	line := "- " + time.Now().Format("2006-01-02") + ": " + strings.TrimSpace(fact) + "\n"
	content := existing + line
	for len(content) > MaxUserChars {
		idx := strings.Index(content, "\n")
		if idx < 0 {
			break
		}
		content = content[idx+1:]
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		return "", err
	}
	return content, nil
}

// Search returns lines from MEMORY.md and USER.md containing q.
func Search(q string) []string {
	var out []string
	for _, name := range []string{"MEMORY.md", "USER.md"} {
		for _, line := range strings.Split(Read(name), "\n") {
			if strings.Contains(strings.ToLower(line), strings.ToLower(q)) {
				out = append(out, name+": "+line)
			}
		}
	}
	return out
}
