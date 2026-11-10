// Package agent is the core of Route-X: a provider interface, a
// deterministic local provider (offline, no API key), an optional
// Gemini provider, tool dispatch, and background subagents.
// It mirrors agy's model: one main agent that can spawn subagents
// in parallel and review every edit as a diff before applying it.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"route-x/internal/config"
	"route-x/internal/memory"
	"route-x/internal/tools"
)

// Provider generates text for a prompt, given workspace context.
type Provider interface {
	Name() string
	Complete(ctx context.Context, prompt, workspaceContext string) (string, error)
}

// LocalProvider is the offline provider: it never calls the network.
// It plans from keywords and delegates real work to the tools.
type LocalProvider struct{}

func (LocalProvider) Name() string { return "local" }

func (LocalProvider) Complete(_ context.Context, prompt, workspaceContext string) (string, error) {
	return fmt.Sprintf(
		"Plan (local provider, no API key):\n"+
			"1. Understand the request: %s\n"+
			"2. Use the workspace tools — read, search, write (diff first), run — to do it.\n"+
			"3. Verify with a second read / test run before finishing.\n\nWorkspace context:\n%s",
		prompt, workspaceContext), nil
}

// GeminiProvider calls the Gemini REST API. It needs GEMINI_API_KEY
// or config.api_key. Model names change over time; the default in
// config is a starting point, override with --model.
type GeminiProvider struct {
	APIKey string
	Model  string
}

func (g GeminiProvider) Name() string { return "gemini:" + g.Model }

func (g GeminiProvider) Complete(ctx context.Context, prompt, workspaceContext string) (string, error) {
	if g.APIKey == "" {
		return "", fmt.Errorf("gemini provider needs GEMINI_API_KEY (or config.api_key)")
	}
	// The API key travels in a header, never in the URL, so it
	// cannot leak into logs, shell history or error messages.
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent",
		g.Model)
	body, _ := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]string{{"text": workspaceContext + "\n\nUser request:\n" + prompt}}},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("gemini API error: %s", parsed.Error.Message)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini returned no text")
	}
	return parsed.Candidates[0].Content.Parts[0].Text, nil
}

// OpenAICompatibleProvider talks to any OpenAI-compatible
// /chat/completions endpoint: OpenAI itself, Ollama
// (http://localhost:11434/v1) and any other compatible server.
// This is the Hermes-style model freedom: one client, many models.
type OpenAICompatibleProvider struct {
	APIKey  string
	Model   string
	BaseURL string // e.g. https://api.openai.com/v1
}

func (p OpenAICompatibleProvider) Name() string { return "openai-compatible:" + p.Model }

func (p OpenAICompatibleProvider) Complete(ctx context.Context, prompt, workspaceContext string) (string, error) {
	base := strings.TrimSuffix(p.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	body, _ := json.Marshal(map[string]any{
		"model": p.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "You are Route-X, a terminal coding agent. " + workspaceContext},
			{"role": "user", "content": prompt},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("openai-compatible API error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("openai-compatible API returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

// AnthropicProvider calls the Claude Messages API. Untested live
// without an ANTHROPIC_API_KEY; the request shape follows the
// public Anthropic API docs.
type AnthropicProvider struct {
	APIKey string
	Model  string
}

func (p AnthropicProvider) Name() string { return "anthropic:" + p.Model }

func (p AnthropicProvider) Complete(ctx context.Context, prompt, workspaceContext string) (string, error) {
	if p.APIKey == "" {
		return "", fmt.Errorf("anthropic provider needs ANTHROPIC_API_KEY (or config.api_key)")
	}
	body, _ := json.Marshal(map[string]any{
		"model":      p.Model,
		"max_tokens": 4096,
		"messages": []map[string]string{
			{"role": "user", "content": workspaceContext + "\n\nUser request:\n" + prompt},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("anthropic API error: %s", parsed.Error.Message)
	}
	var out strings.Builder
	for _, c := range parsed.Content {
		out.WriteString(c.Text)
	}
	return out.String(), nil
}

// SubAgent is one background agent, like agy's /agent tasks.
type SubAgent struct {
	ID     string
	Task   string
	Status string // running | done | failed
	Result string
	done   chan struct{}
}

// Agent holds the runtime state for one Route-X run.
type Agent struct {
	Config   config.Config
	Workdir  string
	Approve  tools.ApproveFunc
	LastDiff string

	mu        sync.Mutex
	subagents map[string]*SubAgent
	subSeq    int
}

// New builds an Agent for the given workdir.
func New(cfg config.Config, workdir string, approve tools.ApproveFunc) *Agent {
	return &Agent{Config: cfg, Workdir: workdir, Approve: approve, subagents: map[string]*SubAgent{}}
}

// provider picks the active provider. Keys come from the
// per-provider env var first, then config.api_key.
func (a *Agent) provider() Provider {
	key := config.EnvAPIKey(a.Config.Provider)
	if key == "" {
		key = a.Config.APIKey
	}
	switch a.Config.Provider {
	case "gemini":
		return GeminiProvider{APIKey: key, Model: a.Config.Model}
	case "openai":
		base := a.Config.BaseURL
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		return OpenAICompatibleProvider{APIKey: key, Model: a.Config.Model, BaseURL: base}
	case "ollama":
		base := a.Config.BaseURL
		if base == "" {
			base = "http://localhost:11434/v1"
		}
		return OpenAICompatibleProvider{APIKey: key, Model: a.Config.Model, BaseURL: base}
	case "anthropic":
		return AnthropicProvider{APIKey: key, Model: a.Config.Model}
	}
	return LocalProvider{}
}

// LoadInstructions reads Codex/Claude-style instruction files:
// AGENTS.md and CLAUDE.md in the workdir, plus a global
// ~/.route-x/AGENTS.md. Missing files are not an error.
func (a *Agent) LoadInstructions() string {
	var parts []string
	for _, path := range []string{
		filepath.Join(a.Workdir, "AGENTS.md"),
		filepath.Join(a.Workdir, "CLAUDE.md"),
	} {
		if data, err := os.ReadFile(path); err == nil {
			parts = append(parts, "--- "+path+" ---\n"+string(data))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if data, err := os.ReadFile(filepath.Join(home, ".route-x", "AGENTS.md")); err == nil {
			parts = append(parts, "--- global AGENTS.md ---\n"+string(data))
		}
	}
	return strings.Join(parts, "\n")
}

// workspaceContext gives the provider a short view of the project.
func (a *Agent) workspaceContext() string {
	files := tools.ListFiles(a.Workdir, 20)
	ctx := "Workdir: " + a.Workdir + "\nFiles (first 20):\n- " + strings.Join(files, "\n- ")
	if instr := a.LoadInstructions(); instr != "" {
		ctx += "\nProject instructions:\n" + instr
	}
	if mem := memory.Read("MEMORY.md"); mem != "" {
		ctx += "\nPersistent memory:\n" + mem
	}
	return ctx
}

// CanWrite reports whether a write is allowed by the sandbox.
// read-only never writes; workspace-write only inside the workdir;
// danger-full-access writes anywhere (use only in a throwaway VM).
func (a *Agent) CanWrite(path string) (bool, string) {
	switch a.Config.Sandbox {
	case "read-only":
		return false, "sandbox is read-only"
	case "danger-full-access":
		return true, ""
	default: // workspace-write
		wd, err := filepath.Abs(a.Workdir)
		if err != nil {
			return false, err.Error()
		}
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(wd, abs)
		}
		abs = filepath.Clean(abs)
		if abs != wd && !strings.HasPrefix(abs, wd+string(os.PathSeparator)) {
			return false, "write outside the workspace is blocked by the sandbox"
		}
		return true, ""
	}
}

// CanRun reports whether a shell command may run. Deny list wins
// over everything; read-only sandbox blocks all commands.
func (a *Agent) CanRun(cmd string) (bool, string) {
	for _, d := range a.Config.Deny {
		if d != "" && strings.Contains(cmd, d) {
			return false, fmt.Sprintf("command matches deny list %q", d)
		}
	}
	if a.Config.Sandbox == "read-only" {
		return false, "sandbox is read-only"
	}
	return true, ""
}

// IsAllowed reports whether a command is on the allow list and
// may run without asking (Codex on-request + Claude allow-list).
func (a *Agent) IsAllowed(cmd string) bool {
	for _, pat := range a.Config.Allow {
		if pat != "" && strings.Contains(cmd, pat) {
			return true
		}
	}
	return false
}

// Review summarises the working tree (git diff) and flags TODO /
// FIXME left in changed files, like Codex /review.
func (a *Agent) Review() (string, error) {
	stat, err := a.runInWorkdir("git", "diff", "--stat")
	if err != nil {
		return "", fmt.Errorf("review needs a git repo: %w", err)
	}
	diff, _ := a.runInWorkdir("git", "diff")
	var b strings.Builder
	b.WriteString("Review — working tree:\n")
	b.WriteString(stat)
	matches, _ := tools.Search(a.Workdir, "TODO|FIXME")
	if len(matches) > 0 {
		fmt.Fprintf(&b, "\n%d TODO/FIXME marker(s):\n", len(matches))
		for _, m := range matches {
			fmt.Fprintf(&b, "%s:%d: %s\n", m.File, m.Line, m.Text)
		}
	}
	if diff != "" {
		b.WriteString("\nDiff (first 4000 chars):\n")
		if len(diff) > 4000 {
			diff = diff[:4000]
		}
		b.WriteString(diff)
	}
	return b.String(), nil
}

// Verify runs the project's checks and reports evidence, Hermes
// style: "done" means proven, not claimed. For Go projects it
// runs go vet and go test; otherwise it reports that no Go
// project was found.
func (a *Agent) Verify() (string, error) {
	if _, err := os.Stat(filepath.Join(a.Workdir, "go.mod")); err != nil {
		return "verify: no go.mod in the workdir — no Go checks to run", nil
	}
	vet, vetErr := a.runInWorkdir("go", "vet", "./...")
	test, testErr := a.runInWorkdir("go", "test", "./...")
	var b strings.Builder
	b.WriteString("Verification evidence:\n")
	if vetErr == nil {
		b.WriteString("- go vet: PASS\n")
	} else {
		fmt.Fprintf(&b, "- go vet: FAIL\n%s\n", vet)
	}
	if testErr == nil {
		b.WriteString("- go test: PASS\n")
	} else {
		fmt.Fprintf(&b, "- go test: FAIL\n%s\n", test)
	}
	if vetErr != nil || testErr != nil {
		return b.String(), fmt.Errorf("verification failed")
	}
	return b.String(), nil
}

func (a *Agent) runInWorkdir(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = a.Workdir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Learn creates a skill from a repeated task, Hermes style:
// the skill is a plain Markdown file the user can read, edit
// or delete. It is created, not auto-triggered.
func (a *Agent) Learn(name, description string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("learn needs a skill name")
	}
	dir := filepath.Join(a.Workdir, ".route-x", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".md")
	content := "# " + name + "\n\n" + description + "\n\n" +
		"Skill ini dibuat oleh Route-X dari tugas yang berulang. " +
		"Baca, ubah, atau hapus file ini bila sudah tidak sesuai.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return "Learned skill " + name + " -> " + path, nil
}

var fileToken = regexp.MustCompile(`[\w./-]+\.[a-zA-Z0-9]{1,10}`)

func extractFile(prompt string) string {
	return fileToken.FindString(prompt)
}

// Run handles one user prompt: tool dispatch first, provider second.
// Returns the agent's reply text.
func (a *Agent) Run(ctx context.Context, prompt string) (string, error) {
	lower := strings.ToLower(prompt)

	// 1. Read a file: "baca file X" / "read X.go"
	if strings.Contains(lower, "baca") || strings.HasPrefix(lower, "read ") {
		if f := extractFile(prompt); f != "" {
			content, err := tools.ReadFile(f)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("--- %s ---\n%s", f, content), nil
		}
	}

	// 2. Search: "cari <pattern>" / "search <pattern>"
	if strings.HasPrefix(lower, "cari ") || strings.HasPrefix(lower, "search ") {
		pattern := strings.TrimSpace(prompt[strings.Index(prompt, " ")+1:])
		matches, err := tools.Search(a.Workdir, pattern)
		if err != nil {
			return "", err
		}
		if len(matches) == 0 {
			return fmt.Sprintf("No matches for %q in %s.", pattern, a.Workdir), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d match(es) for %q:\n", len(matches), pattern)
		for _, m := range matches {
			fmt.Fprintf(&b, "%s:%d: %s\n", m.File, m.Line, m.Text)
		}
		return b.String(), nil
	}

	// 3. Create a file: "buat file X" / "create file X"
	if strings.Contains(lower, "buat file") || strings.Contains(lower, "create file") {
		if f := extractFile(prompt); f != "" {
			if ok, reason := a.CanWrite(f); !ok {
				return "", fmt.Errorf("write blocked: %s", reason)
			}
			content := templateFor(f)
			diff, written, err := tools.WriteFile(f, content, a.Config.Mode, a.Approve)
			if err != nil {
				return "", err
			}
			a.LastDiff = diff
			if a.Config.Mode == "plan" {
				return fmt.Sprintf("Plan mode — would create %s, not written:\n%s", f, diff), nil
			}
			if !written {
				return fmt.Sprintf("Write rejected, diff was:\n%s", diff), nil
			}
			return fmt.Sprintf("Created %s:\n%s", f, diff), nil
		}
	}

	// 4. Run a command: "jalankan <cmd>" / "run <cmd>"
	if strings.HasPrefix(lower, "jalankan ") || strings.HasPrefix(lower, "run ") {
		cmd := strings.TrimSpace(prompt[strings.Index(prompt, " ")+1:])
		if ok, reason := a.CanRun(cmd); !ok {
			return "", fmt.Errorf("command blocked: %s", reason)
		}
		approve := a.Approve
		if a.IsAllowed(cmd) {
			approve = tools.AutoApprove
		}
		out, err := tools.RunCommand(cmd, a.Config.Mode, approve)
		if err != nil {
			return out, err
		}
		return "$ " + cmd + "\n" + out, nil
	}

	// 5. Otherwise ask the provider for a plan / answer.
	reply, err := a.provider().Complete(ctx, prompt, a.workspaceContext())
	if err != nil {
		return "", err
	}
	return reply, nil
}

// templateFor returns a starter template based on file extension.
func templateFor(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello from Route-X\")\n}\n"
	case strings.HasSuffix(path, ".html"):
		return "<!doctype html>\n<html><body><h1>Route-X</h1></body></html>\n"
	case strings.HasSuffix(path, ".md"):
		return "# Route-X\n\nDibuat oleh Route-X.\n"
	default:
		return "Dibuat oleh Route-X.\n"
	}
}

// Spawn starts a background subagent that searches the workspace
// for the task's keywords, like agy's async /agent.
func (a *Agent) Spawn(task string) *SubAgent {
	a.mu.Lock()
	a.subSeq++
	id := fmt.Sprintf("agent-%d", a.subSeq)
	sa := &SubAgent{ID: id, Task: task, Status: "running", done: make(chan struct{})}
	a.subagents[id] = sa
	a.mu.Unlock()

	go func() {
		defer close(sa.done)
		// The subagent's real work: search for the first word of the task.
		words := strings.Fields(task)
		keyword := ""
		if len(words) > 0 {
			keyword = words[len(words)-1]
		}
		matches, _ := tools.Search(a.Workdir, regexp.QuoteMeta(keyword))
		time.Sleep(200 * time.Millisecond) // keep the demo visibly async
		a.mu.Lock()
		defer a.mu.Unlock()
		if len(matches) == 0 {
			sa.Result = fmt.Sprintf("Subagent %s finished: no matches for %q.", id, keyword)
		} else {
			sa.Result = fmt.Sprintf("Subagent %s finished: %d match(es) for %q, first at %s:%d.",
				id, len(matches), keyword, matches[0].File, matches[0].Line)
		}
		sa.Status = "done"
	}()
	return sa
}

// ListSubagents returns a snapshot of all subagents.
func (a *Agent) ListSubagents() []*SubAgent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*SubAgent, 0, len(a.subagents))
	for _, sa := range a.subagents {
		out = append(out, sa)
	}
	return out
}

// WaitSubagent blocks until the named subagent finishes (max 30s).
func (a *Agent) WaitSubagent(id string) *SubAgent {
	a.mu.Lock()
	sa, ok := a.subagents[id]
	a.mu.Unlock()
	if !ok {
		return nil
	}
	select {
	case <-sa.done:
	case <-time.After(30 * time.Second):
	}
	return sa
}
