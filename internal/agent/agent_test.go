package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		fmt.Fprint(w, `{"choices": [{"message": {"content": "mock reply"}}]}`)
	}))
	defer srv.Close()
	p := OpenAICompatibleProvider{APIKey: "sk-test", Model: "mock-model", BaseURL: srv.URL}
	reply, err := p.Complete(context.Background(), "hi", "ctx")
	if err != nil || reply != "mock reply" {
		t.Fatalf("reply = %q, err=%v", reply, err)
	}
	// The request itself must be genuine: key in header, model and
	// prompt in the body — a test that only checks the reply would
	// pass even with a broken request.
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"mock-model"`) || !strings.Contains(gotBody, "hi") {
		t.Fatalf("request body = %s", gotBody)
	}
}

func TestAnthropicProviderWithMock(t *testing.T) {
	var gotKey, gotVersion, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		fmt.Fprint(w, `{"content": [{"text": "mock anthropic reply"}]}`)
	}))
	defer srv.Close()
	p := AnthropicProvider{APIKey: "sk-ant-test", Model: "mock-claude", BaseURL: srv.URL}
	reply, err := p.Complete(context.Background(), "hi", "ctx")
	if err != nil || reply != "mock anthropic reply" {
		t.Fatalf("reply = %q, err=%v", reply, err)
	}
	if gotKey != "sk-ant-test" || gotVersion != "2023-06-01" {
		t.Fatalf("headers key=%q version=%q", gotKey, gotVersion)
	}
	if !strings.Contains(gotBody, `"mock-claude"`) || !strings.Contains(gotBody, "hi") {
		t.Fatalf("request body = %s", gotBody)
	}
	// Empty key must still fail before any request is made.
	if _, err := (AnthropicProvider{Model: "mock"}).Complete(context.Background(), "hi", "ctx"); err == nil {
		t.Fatal("anthropic without key should error")
	}
}

func TestIsAllowedRequiresExactCommand(t *testing.T) {
	ag := newTestAgent(t, "default")
	if !ag.IsAllowed("go vet ./...") {
		t.Fatal("exact allow entry should be allowed")
	}
	// A chained command that merely contains an allowed entry must
	// NOT be auto-approved — this was a real hole in v0.2.0.
	if ag.IsAllowed("go vet ./...; rm -rf /") {
		t.Fatal("chained command must not be auto-approved")
	}
	if ag.IsAllowed("go vet ./... && echo pwned") {
		t.Fatal("chained command must not be auto-approved")
	}
}

func TestReviewOnRealGitRepo(t *testing.T) {
	ag := newTestAgent(t, "default")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = ag.Workdir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	_ = os.WriteFile(filepath.Join(ag.Workdir, "a.go"), []byte("package main\n"), 0o644)
	run("add", "-A")
	run("commit", "-qm", "init")
	_ = os.WriteFile(filepath.Join(ag.Workdir, "a.go"), []byte("package main\n\n// TODO: fix this\n"), 0o644)
	reply, err := ag.Review()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "TODO") || !strings.Contains(reply, "a.go") {
		t.Fatalf("review = %q", reply)
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
