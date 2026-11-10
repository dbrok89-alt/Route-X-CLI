package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".route-x"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".route-x", "mcp.json"),
		[]byte(`{"servers": {"echo": {"command": "python3", "args": ["server.py"]}}}`), 0o644)
	t.Setenv("HOME", t.TempDir())
	cfg := Load(dir)
	if cfg.Servers["echo"].Command != "python3" {
		t.Fatalf("servers = %+v", cfg.Servers)
	}
}

func TestConnectListAndCall(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script, err := filepath.Abs("testdata/mcp-echo.py")
	if err != nil {
		t.Fatal(err)
	}
	client, err := Connect(ServerConfig{Command: python, Args: []string{script}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools()
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, err=%v", tools, err)
	}
	out, err := client.CallTool("echo", map[string]any{"x": "1"})
	if err != nil || !strings.Contains(out, "echo:") {
		t.Fatalf("call = %q, err=%v", out, err)
	}
}

func TestConnectMissingCommandFails(t *testing.T) {
	if _, err := Connect(ServerConfig{Command: "definitely-not-a-real-command-xyz"}); err == nil {
		t.Fatal("connect to missing command should fail")
	}
}

func TestCallToolErrorSurfaces(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script, err := filepath.Abs("testdata/mcp-echo.py")
	if err != nil {
		t.Fatal(err)
	}
	client, err := Connect(ServerConfig{Command: python, Args: []string{script}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.CallTool("boom", map[string]any{}); err == nil {
		t.Fatal("server error must surface as an error, not an empty reply")
	}
}
