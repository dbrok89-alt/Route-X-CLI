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
	"route-x/internal/loop"
	"route-x/internal/session"
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

func TestOpenAICompleteToolsParsesToolCalls(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		fmt.Fprint(w, `{"choices": [{"message": {"content": "", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read", "arguments": "{\"path\": \"a.txt\"}"}}]}}], "usage": {"prompt_tokens": 11, "completion_tokens": 7}}`)
	}))
	defer srv.Close()
	p := OpenAICompatibleProvider{APIKey: "k", Model: "m", BaseURL: srv.URL}
	msg, usage, err := p.CompleteTools(context.Background(),
		[]loop.Message{{Role: "user", Content: "read a.txt"}},
		[]loop.ToolDef{{Name: "read", Description: "read a file", Parameters: loop.Schema(map[string]any{"path": map[string]any{"type": "string"}}, "path")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Name != "read" || msg.ToolCalls[0].Arguments["path"] != "a.txt" {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", usage)
	}
	// The request must actually declare the tool to the model.
	if !strings.Contains(gotBody, `"read"`) || !strings.Contains(gotBody, `"tools"`) {
		t.Fatalf("request body did not declare tools: %s", gotBody)
	}
}

func TestAnthropicCompleteToolsParsesToolUse(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		fmt.Fprint(w, `{"content": [{"type": "tool_use", "id": "toolu_1", "name": "search", "input": {"pattern": "TODO"}}], "usage": {"input_tokens": 9, "output_tokens": 4}}`)
	}))
	defer srv.Close()
	p := AnthropicProvider{APIKey: "k", Model: "m", BaseURL: srv.URL}
	msg, usage, err := p.CompleteTools(context.Background(),
		[]loop.Message{{Role: "user", Content: "find TODO"}},
		[]loop.ToolDef{{Name: "search", Description: "search", Parameters: loop.Schema(map[string]any{"pattern": map[string]any{"type": "string"}}, "pattern")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Name != "search" || msg.ToolCalls[0].Arguments["pattern"] != "TODO" {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
	if usage.InputTokens != 9 || usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", usage)
	}
	if !strings.Contains(gotBody, `"input_schema"`) || !strings.Contains(gotBody, `"search"`) {
		t.Fatalf("request body did not declare tools: %s", gotBody)
	}
}

func TestGeminiCompleteToolsParsesFunctionCall(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		fmt.Fprint(w, `{"candidates": [{"content": {"parts": [{"functionCall": {"name": "run", "args": {"command": "go vet ./..."}}}]}}], "usageMetadata": {"promptTokenCount": 12, "candidatesTokenCount": 6}}`)
	}))
	defer srv.Close()
	p := GeminiProvider{APIKey: "k", Model: "m", BaseURL: srv.URL}
	msg, usage, err := p.CompleteTools(context.Background(),
		[]loop.Message{{Role: "user", Content: "vet the code"}},
		[]loop.ToolDef{{Name: "run", Description: "run a command", Parameters: loop.Schema(map[string]any{"command": map[string]any{"type": "string"}}, "command")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Name != "run" || msg.ToolCalls[0].Arguments["command"] != "go vet ./..." {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
	if !strings.Contains(gotBody, `"functionDeclarations"`) || !strings.Contains(gotBody, `"run"`) {
		t.Fatalf("request body did not declare functions: %s", gotBody)
	}
}

// scriptedToolProvider is a test double that returns a fixed
// script of replies for the agentic loop.
type scriptedToolProvider struct {
	script []loop.Message
}

func (p *scriptedToolProvider) CompleteTools(_ context.Context, _ []loop.Message, _ []loop.ToolDef) (loop.Message, loop.Usage, error) {
	if len(p.script) == 0 {
		return loop.Message{}, loop.Usage{}, fmt.Errorf("script exhausted")
	}
	reply := p.script[0]
	p.script = p.script[1:]
	return reply, loop.Usage{InputTokens: 10, OutputTokens: 5}, nil
}

func TestRunAgentTwoStepRealFiles(t *testing.T) {
	dir := t.TempDir()
	// Production always runs with cwd == workdir (main.go sets
	// workdir from os.Getwd), and tools resolve paths against
	// the cwd, so the test must do the same. (Go 1.23 has no
	// t.Chdir; restore the old cwd by hand.)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0o644)
	cfg := config.Config{Mode: "auto", Sandbox: "workspace-write", Approval: "on-request", Provider: "local"}
	a := New(cfg, dir, tools.AutoApprove)
	a.ToolProviderOverride = &scriptedToolProvider{script: []loop.Message{
		{Role: "assistant", ToolCalls: []loop.ToolCall{{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.txt"}}}},
		{Role: "assistant", ToolCalls: []loop.ToolCall{{ID: "c2", Name: "write", Arguments: map[string]any{"path": "b.txt", "content": "hello world"}}}},
		{Role: "assistant", Content: "copied"},
	}}
	answer, err := a.RunAgent(context.Background(), "copy a.txt to b.txt", nil)
	if err != nil || answer != "copied" {
		t.Fatalf("answer = %q, err=%v", answer, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "b.txt"))
	if err != nil || string(got) != "hello world" {
		t.Fatalf("b.txt = %q, err=%v", got, err)
	}
	if a.LastUsage.InputTokens == 0 || a.LastUsage.OutputTokens == 0 {
		t.Fatalf("usage not recorded: %+v", a.LastUsage)
	}
}

func TestRunAgentRefusesDangerousCommand(t *testing.T) {
	cfg := config.Config{Mode: "auto", Sandbox: "workspace-write", Approval: "on-request", Provider: "local"}
	a := New(cfg, t.TempDir(), tools.AutoApprove)
	a.ToolProviderOverride = &scriptedToolProvider{script: []loop.Message{
		{Role: "assistant", ToolCalls: []loop.ToolCall{{ID: "c1", Name: "run", Arguments: map[string]any{"command": "go vet ./...; rm -rf /"}}}},
		{Role: "assistant", Content: "blocked, will not run it"},
	}}
	answer, err := a.RunAgent(context.Background(), "vet then clean up", nil)
	if err != nil || answer != "blocked, will not run it" {
		t.Fatalf("answer = %q, err=%v", answer, err)
	}
	// The dangerous command must never have run: ExecuteTool
	// returns the block as a tool error, which the model sees.
}

func TestExecuteToolWriteBlockedByReadOnlySandbox(t *testing.T) {
	cfg := config.Config{Mode: "auto", Sandbox: "read-only", Approval: "on-request", Provider: "local"}
	a := New(cfg, t.TempDir(), tools.AutoApprove)
	_, err := a.ExecuteTool(context.Background(), loop.ToolCall{Name: "write", Arguments: map[string]any{"path": "x.txt", "content": "hi"}})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunAgentNoToolProviderFallsBack(t *testing.T) {
	cfg := config.Config{Mode: "auto", Sandbox: "workspace-write", Approval: "on-request", Provider: "local"}
	a := New(cfg, t.TempDir(), tools.AutoApprove)
	_, err := a.RunAgent(context.Background(), "hi", nil)
	if err == nil || !strings.Contains(err.Error(), "cannot call tools") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIStreamToolsReassembles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Text split across two chunks, then a tool call split
		// across two chunks (arguments fragmented mid-string).
		fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"content\": \"read\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"content\": \"ing\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"tool_calls\": [{\"index\": 0, \"id\": \"call_9\", \"function\": {\"name\": \"read\", \"arguments\": \"{\\\"pa\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"tool_calls\": [{\"index\": 0, \"function\": {\"arguments\": \"th\\\": \\\"a.txt\\\"}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	p := OpenAICompatibleProvider{APIKey: "k", Model: "m", BaseURL: srv.URL}
	var tokens []string
	msg, _, err := p.StreamTools(context.Background(),
		[]loop.Message{{Role: "user", Content: "read a.txt"}},
		[]loop.ToolDef{{Name: "read"}}, func(s string) { tokens = append(tokens, s) })
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "reading" {
		t.Fatalf("content = %q", msg.Content)
	}
	if len(tokens) != 2 || tokens[0] != "read" || tokens[1] != "ing" {
		t.Fatalf("tokens = %v", tokens)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Name != "read" || msg.ToolCalls[0].Arguments["path"] != "a.txt" {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
}

func TestAnthropicStreamToolsReassembles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\": \"content_block_start\", \"content_block\": {\"type\": \"tool_use\", \"id\": \"toolu_7\", \"name\": \"search\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\": \"content_block_delta\", \"delta\": {\"type\": \"input_json_delta\", \"partial_json\": \"{\\\"pattern\\\": \\\"TO\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\": \"content_block_delta\", \"delta\": {\"type\": \"input_json_delta\", \"partial_json\": \"DO\\\"}\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\": \"content_block_stop\"}\n\n")
	}))
	defer srv.Close()
	p := AnthropicProvider{APIKey: "k", Model: "m", BaseURL: srv.URL}
	msg, _, err := p.StreamTools(context.Background(),
		[]loop.Message{{Role: "user", Content: "find TODO"}},
		[]loop.ToolDef{{Name: "search"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Name != "search" || msg.ToolCalls[0].Arguments["pattern"] != "TODO" {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
}

// scriptedProvider is a test double for plain completions.
type scriptedProvider struct {
	reply string
}

func (p scriptedProvider) Name() string { return "scripted" }
func (p scriptedProvider) Complete(_ context.Context, prompt, _ string) (string, error) {
	return p.reply + " (prompt was " + fmt.Sprint(len(prompt)) + " chars)", nil
}

func TestCompactReplacesHistory(t *testing.T) {
	cfg := config.Config{Mode: "auto", Provider: "local"}
	a := New(cfg, t.TempDir(), tools.AutoApprove)
	a.ProviderOverride = scriptedProvider{reply: "Goal: copy file. Done: b.txt created. Open: none."}
	sess := &session.Session{Messages: []session.Message{
		{Role: "user", Content: "copy a.txt"},
		{Role: "agent", Content: "ok"},
		{Role: "user", Content: "and check it"},
		{Role: "agent", Content: "checked"},
		{Role: "user", Content: "thanks"},
	}}
	if err := a.Compact(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if len(sess.Messages) != 1 {
		t.Fatalf("messages = %d", len(sess.Messages))
	}
	if !strings.Contains(sess.Messages[0].Content, "Goal: copy file") || !strings.Contains(sess.Messages[0].Content, "compacted from 5") {
		t.Fatalf("summary = %q", sess.Messages[0].Content)
	}
}

func TestCompactRefusesShortSession(t *testing.T) {
	a := New(config.Config{}, t.TempDir(), tools.AutoApprove)
	a.ProviderOverride = scriptedProvider{reply: "x"}
	sess := &session.Session{Messages: []session.Message{{Role: "user", Content: "hi"}}}
	if err := a.Compact(context.Background(), sess); err == nil {
		t.Fatal("short session should not be compacted")
	}
}

func TestSubagentPersistedAcrossRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644)
	a := New(config.Config{Mode: "auto"}, dir, tools.AutoApprove)
	sa := a.Spawn("cari hello")
	a.WaitSubagent(sa.ID)
	if sa.Status != "done" {
		t.Fatalf("status = %s", sa.Status)
	}
	// A fresh agent (new process in real life) must see it.
	b := New(config.Config{Mode: "auto"}, dir, tools.AutoApprove)
	recs := b.LoadPersistedSubagents()
	if len(recs) != 1 || recs[0].ID != sa.ID || recs[0].Status != "done" || recs[0].Result == "" {
		t.Fatalf("records = %+v", recs)
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
