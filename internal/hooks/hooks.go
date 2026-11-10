// Package hooks implements Claude-Code-style lifecycle hooks:
// shell commands that fire automatically at defined points, with
// no prompting. Config lives in .route-x/hooks.json in the workdir
// (project hooks) or ~/.route-x/hooks.json (global hooks).
//
// Events: SessionStart, PreToolUse, PostToolUse, PostEdit, Stop.
// A hook is a shell command run with a 10s timeout. Its stdout is
// shown to the user. Hooks are NOT run in plan mode.
// Warning, as in Claude Code: a hook from an untrusted repo can do
// anything you can do. Read hooks.json before trusting a project.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Hooks maps event names to shell commands.
type Hooks struct {
	SessionStart string `json:"SessionStart,omitempty"`
	PreToolUse   string `json:"PreToolUse,omitempty"`
	PostToolUse  string `json:"PostToolUse,omitempty"`
	PostEdit     string `json:"PostEdit,omitempty"`
	Stop         string `json:"Stop,omitempty"`
}

// Load reads project hooks first, then global hooks. Missing files
// are not an error. Project hooks win for an event they define.
func Load(workdir string) Hooks {
	var out Hooks
	// Global first, so project can override per event.
	for _, path := range []string{globalPath(), filepath.Join(workdir, ".route-x", "hooks.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var h Hooks
		if err := json.Unmarshal(data, &h); err != nil {
			continue
		}
		merge(&out, h)
	}
	return out
}

func globalPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".route-x", "hooks.json")
}

func merge(dst *Hooks, src Hooks) {
	if src.SessionStart != "" {
		dst.SessionStart = src.SessionStart
	}
	if src.PreToolUse != "" {
		dst.PreToolUse = src.PreToolUse
	}
	if src.PostToolUse != "" {
		dst.PostToolUse = src.PostToolUse
	}
	if src.PostEdit != "" {
		dst.PostEdit = src.PostEdit
	}
	if src.Stop != "" {
		dst.Stop = src.Stop
	}
}

// Get returns the command for an event, or "" when none is set.
func (h Hooks) Get(event string) string {
	switch event {
	case "SessionStart":
		return h.SessionStart
	case "PreToolUse":
		return h.PreToolUse
	case "PostToolUse":
		return h.PostToolUse
	case "PostEdit":
		return h.PostEdit
	case "Stop":
		return h.Stop
	}
	return ""
}

// List returns the events that have a hook set.
func (h Hooks) List() []string {
	var out []string
	for _, ev := range []string{"SessionStart", "PreToolUse", "PostToolUse", "PostEdit", "Stop"} {
		if h.Get(ev) != "" {
			out = append(out, ev+": "+h.Get(ev))
		}
	}
	return out
}

// Fire runs the hook for an event. It returns its combined output.
// An empty command returns "" with no error.
func (h Hooks) Fire(event string) (string, error) {
	cmd := h.Get(event)
	if strings.TrimSpace(cmd) == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	out, err := c.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("hook %s timed out after 10s", event)
	}
	return string(out), err
}
