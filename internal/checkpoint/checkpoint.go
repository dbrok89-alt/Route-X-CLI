// Package checkpoint snapshots the workspace before risky
// edits, OpenCode /undo-style: a checkpoint copies the current
// files into ~/.route-x/checkpoints/<id>/, and restoring copies
// them back and deletes files created after the checkpoint.
// Binary files over 1 MiB and the .git directory are skipped.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Manifest records what a checkpoint contains.
type Manifest struct {
	ID      string   `json:"id"`
	Created string   `json:"created"`
	Files   []string `json:"files"` // relative paths, sorted
}

func rootDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".route-x", "checkpoints")
}

// Create snapshots the workspace. It returns the checkpoint id.
func Create(workdir string) (string, error) {
	root := rootDir()
	if root == "" {
		return "", fmt.Errorf("cannot find home directory")
	}
	id := time.Now().Format("20060102-150405")
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var files []string
	err := filepath.Walk(workdir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == ".route-x" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > 1<<20 {
			return nil // skip large binaries
		}
		rel, err := filepath.Rel(workdir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	manifest, _ := json.Marshal(Manifest{ID: id, Created: time.Now().Format(time.RFC3339), Files: files})
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o644)
	return id, nil
}

// List returns all checkpoints, newest first.
func List() []Manifest {
	root := rootDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name(), "manifest.json"))
		if err != nil {
			continue
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// Restore copies a checkpoint back into the workspace and deletes
// files that did not exist at checkpoint time (tracked files
// only: files inside .git or .route-x are never touched).
func Restore(workdir, id string) error {
	root := rootDir()
	dir := filepath.Join(root, id)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("checkpoint %q not found: %w", id, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	// Delete files created after the checkpoint.
	keep := map[string]bool{}
	for _, f := range m.Files {
		keep[f] = true
	}
	_ = filepath.Walk(workdir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasPrefix(path, filepath.Join(workdir, ".git")+string(os.PathSeparator)) ||
			strings.HasPrefix(path, filepath.Join(workdir, ".route-x")+string(os.PathSeparator)) {
			return nil
		}
		rel, err := filepath.Rel(workdir, path)
		if err != nil || keep[rel] {
			return nil
		}
		_ = os.Remove(path)
		return nil
	})
	// Copy the snapshot back.
	for _, rel := range m.Files {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return err
		}
		dst := filepath.Join(workdir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
