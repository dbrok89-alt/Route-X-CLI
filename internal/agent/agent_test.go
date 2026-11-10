package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"route-x/internal/config"
	"route-x/internal/tools"
)

func newTestAgent(t *testing.T, mode string) *Agent {
	t.Helper()
	dir := t.TempDir()
	// t.Chdir needs Go 1.24; route-x targets Go 1.23, so chdir manually.
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	cfg := config.Default()
	cfg.Mode = mode
	return New(cfg, dir, tools.AutoApprove)
}

func TestLocalProviderPlan(t *testing.T) {
	ag := newTestAgent(t, "default")
	reply, err := ag.Run(context.Background(), "jelaskan proyek ini")
	if err != nil || !strings.Contains(reply, "Plan") {
		t.Fatalf("reply = %q, err=%v", reply, err)
	}
}

func TestRunSearch(t *testing.T) {
	ag := newTestAgent(t, "default")
	_ = os.WriteFile(filepath.Join(ag.Workdir, "a.go"), []byte("package main // TODO\n"), 0o644)
	reply, err := ag.Run(context.Background(), "cari TODO")
	if err != nil || !strings.Contains(reply, "a.go") {
		t.Fatalf("reply = %q, err=%v", reply, err)
	}
}

func TestRunCreateFilePlanDoesNotWrite(t *testing.T) {
	ag := newTestAgent(t, "plan")
	ag.Approve = tools.NeverApprove
	reply, err := ag.Run(context.Background(), "buat file hello.go")
	if err != nil || !strings.Contains(reply, "Plan mode") {
		t.Fatalf("reply = %q, err=%v", reply, err)
	}
	if _, err := os.Stat(filepath.Join(ag.Workdir, "hello.go")); !os.IsNotExist(err) {
		t.Fatal("plan mode created a file")
	}
}

func TestRunBlockedCommand(t *testing.T) {
	ag := newTestAgent(t, "auto")
	if _, err := ag.Run(context.Background(), "jalankan rm -rf /tmp/x"); err == nil {
		t.Fatal("blocked command should error")
	}
}

func TestSandboxReadOnlyBlocksWrite(t *testing.T) {
	ag := newTestAgent(t, "default")
	ag.Config.Sandbox = "read-only"
	if _, err := ag.Run(context.Background(), "buat file hello.go"); err == nil {
		t.Fatal("read-only sandbox should block writes")
	}
	if _, err := ag.Run(context.Background(), "jalankan echo hi"); err == nil {
		t.Fatal("read-only sandbox should block shell commands")
	}
}

func TestSandboxWorkspaceWriteBlocksOutside(t *testing.T) {
	ag := newTestAgent(t, "default")
	ag.Config.Sandbox = "workspace-write"
	if ok, _ := ag.CanWrite("../outside.go"); ok {
		t.Fatal("workspace-write should block writes outside the workdir")
	}
	if ok, _ := ag.CanWrite("inside.go"); !ok {
		t.Fatal("workspace-write should allow writes inside the workdir")
	}
}

func TestDenyListBlocksCommand(t *testing.T) {
	ag := newTestAgent(t, "default")
	ag.Config.Deny = []string{"git push --force"}
	if ok, _ := ag.CanRun("git push --force origin main"); ok {
		t.Fatal("deny list should block the command")
	}
	if !ag.IsAllowed("go vet ./...") {
		t.Fatal("allow list should contain go vet")
	}
}

func TestOpenAICompatibleProviderWithMock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"choices": [{"message": {"content": "mock reply"}}]}`)
	}))
	defer srv.Close()
	p := OpenAICompatibleProvider{APIKey: "k", Model: "mock-model", BaseURL: srv.URL}
	reply, err := p.Complete(context.Background(), "hi", "ctx")
	if err != nil || reply != "mock reply" {
		t.Fatalf("reply = %q, err=%v", reply, err)
	}
}

func TestAnthropicProviderWithMockNeedsKey(t *testing.T) {
	p := AnthropicProvider{APIKey: "", Model: "mock"}
	if _, err := p.Complete(context.Background(), "hi", "ctx"); err == nil {
		t.Fatal("anthropic without key should error")
	}
}

func TestLoadInstructions(t *testing.T) {
	ag := newTestAgent(t, "default")
	_ = os.WriteFile(filepath.Join(ag.Workdir, "AGENTS.md"), []byte("Build: go test ./...\n"), 0o644)
	if got := ag.LoadInstructions(); !strings.Contains(got, "go test") {
		t.Fatalf("instructions = %q", got)
	}
}

func TestVerifyOnGoProject(t *testing.T) {
	ag := newTestAgent(t, "default")
	_ = os.WriteFile(filepath.Join(ag.Workdir, "go.mod"), []byte("module example.com/x\n\ngo 1.23\n"), 0o644)
	_ = os.WriteFile(filepath.Join(ag.Workdir, "x.go"), []byte("package main\n"), 0o644)
	reply, err := ag.Verify()
	if err != nil || !strings.Contains(reply, "go vet: PASS") {
		t.Fatalf("verify = %q, err=%v", reply, err)
	}
}

func TestLearnCreatesSkill(t *testing.T) {
	ag := newTestAgent(t, "default")
	reply, err := ag.Learn("demo", "Do the demo thing.")
	if err != nil || !strings.Contains(reply, "demo") {
		t.Fatalf("learn = %q, err=%v", reply, err)
	}
	if _, err := os.Stat(filepath.Join(ag.Workdir, ".route-x", "skills", "demo.md")); err != nil {
		t.Fatal("skill file not created:", err)
	}
}

func TestSpawnCompletes(t *testing.T) {
	ag := newTestAgent(t, "default")
	_ = os.WriteFile(filepath.Join(ag.Workdir, "a.go"), []byte("package main // TODO\n"), 0o644)
	sa := ag.Spawn("cari TODO")
	done := ag.WaitSubagent(sa.ID)
	if done == nil || done.Status != "done" || !strings.Contains(done.Result, "TODO") {
		t.Fatalf("subagent = %+v", done)
	}
}
