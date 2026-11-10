package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"route-x/internal/agent"
	"route-x/internal/config"
	"route-x/internal/session"
	"route-x/internal/tools"
)

// captureStdout runs f while os.Stdout points at a pipe and returns
// everything f printed.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	f()
	_ = w.Close()
	os.Stdout = old
	buf := make([]byte, 1<<20)
	n, _ := r.Read(buf)
	_ = r.Close()
	return string(buf[:n])
}

// Regression test for the bug the v0.2.0 smoke test caught:
// handleSlash used to receive config by value, so /sandbox and
// /mode changed the agent and the saved file but the REPL's own
// copy stayed stale — the next /permissions printed the old value.
func TestSlashSandboxPersistsAcrossCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workdir := t.TempDir()
	cfg := config.Default()
	ag := agent.New(cfg, workdir, tools.AutoApprove)
	sess := &session.Session{ID: "test", Model: cfg.Model, Mode: cfg.Mode}
	_ = os.MkdirAll(filepath.Join(workdir, ".route-x"), 0o755)

	handleSlash(ag, &cfg, sess, "/sandbox read-only", ".")
	if ag.Config.Sandbox != "read-only" {
		t.Fatalf("agent sandbox = %q", ag.Config.Sandbox)
	}
	if cfg.Sandbox != "read-only" {
		t.Fatalf("REPL config sandbox = %q (stale copy bug)", cfg.Sandbox)
	}
	out := captureStdout(t, func() {
		handleSlash(ag, &cfg, sess, "/permissions", ".")
	})
	if !strings.Contains(out, "sandbox: read-only") {
		t.Fatalf("/permissions printed stale value:\n%s", out)
	}
	// And switching back must be visible too.
	handleSlash(ag, &cfg, sess, "/sandbox workspace-write", ".")
	out = captureStdout(t, func() {
		handleSlash(ag, &cfg, sess, "/permissions", ".")
	})
	if !strings.Contains(out, "sandbox: workspace-write") {
		t.Fatalf("/permissions printed stale value:\n%s", out)
	}
}

func TestSlashModePersistsAcrossCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workdir := t.TempDir()
	cfg := config.Default()
	ag := agent.New(cfg, workdir, tools.AutoApprove)
	sess := &session.Session{ID: "test", Model: cfg.Model, Mode: cfg.Mode}

	handleSlash(ag, &cfg, sess, "/mode auto", ".")
	if cfg.Mode != "auto" || ag.Config.Mode != "auto" {
		t.Fatalf("mode = cfg %q, agent %q", cfg.Mode, ag.Config.Mode)
	}
	out := captureStdout(t, func() {
		handleSlash(ag, &cfg, sess, "/usage", ".")
	})
	if !strings.Contains(out, "mode: auto") {
		t.Fatalf("/usage printed stale mode:\n%s", out)
	}
}
