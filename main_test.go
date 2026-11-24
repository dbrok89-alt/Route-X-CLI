package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"route-x/internal/agent"
	"route-x/internal/config"
	"route-x/internal/loop"
	"route-x/internal/session"
	"route-x/internal/tools"
	"route-x/internal/tui"
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

// captureStderr runs f while os.Stderr points at a pipe and returns
// everything f printed.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	f()
	_ = w.Close()
	os.Stderr = old
	buf := make([]byte, 1<<20)
	n, _ := r.Read(buf)
	_ = r.Close()
	return string(buf[:n])
}

// allSlashCommands is the single source of truth for every slash
// command handleSlash accepts — the 33 distinct case labels plus
// /quit, the alias for /exit. Any command added to handleSlash must
// be added here too, or the sync test below fails.
var allSlashCommands = []string{
	"/agent", "/agents", "/approval", "/checkpoint", "/clear",
	"/codesearch", "/compact", "/config", "/context", "/diff",
	"/exit", "/goal", "/help", "/hooks", "/learn", "/lint",
	"/mcp", "/mcp-call", "/memory", "/mode", "/model",
	"/permissions", "/plan", "/plugins", "/provider", "/quit",
	"/remember", "/resume", "/review", "/sandbox", "/search",
	"/skills", "/usage", "/verify",
}

// repoRoot returns the repository root (the directory holding
// README.md), located from this test file so the test works no
// matter which directory `go test` is run from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), name))
	if err != nil {
		t.Fatalf("cannot read %s: %v", name, err)
	}
	return string(data)
}

// Regression test for the v0.3.0 --help gap: serve/share/cron existed
// as subcommands but --help never mentioned them, and the slash list
// was missing /compact, /plugins, /checkpoint, /search and /lint.
// The same pattern recurred afterwards: --help omitted /mcp-call
// and the /quit alias for /exit (PR #12), and README omitted 5
// commands (PR #11). The old test only asserted 10 strings, so a
// future omission of any other command would have passed silently.
// This test is exhaustive: every command in allSlashCommands must
// appear in --help (printUsage), in the REPL /help output, and in
// the README slash-command table. It must also pass on main as-is
// today — all three surfaces are currently in sync.
func TestHelpListsSubcommands(t *testing.T) {
	if len(allSlashCommands) != 34 {
		t.Fatalf("allSlashCommands has %d entries, want 34 — update it when adding a command", len(allSlashCommands))
	}
	usage := captureStderr(t, printUsage)
	for _, want := range []string{"route-x serve", "route-x share", "route-x cron"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("--help is missing %q:\n%s", want, usage)
		}
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	workdir := t.TempDir()
	cfg := config.Default()
	ag := agent.New(cfg, workdir, tools.AutoApprove)
	sess := &session.Session{ID: "test", Model: cfg.Model, Mode: cfg.Mode}
	helpOut := captureStdout(t, func() {
		handleSlash(ag, &cfg, sess, "/help", ".")
	})

	readme := readRepoFile(t, "README.md")
	// Scope the README check to the "## Slash command" table: many
	// commands are also mentioned in the release-notes prose, so a
	// plain strings.Contains on the whole file would pass even after
	// the table row was deleted (proved by mutation: removing the
	// /compact row passed until the check was scoped here).
	table := readmeTable(t, readme)
	for _, cmd := range allSlashCommands {
		if !strings.Contains(usage, cmd) {
			t.Fatalf("--help is missing %q:\n%s", cmd, usage)
		}
		if !strings.Contains(helpOut, cmd) {
			t.Fatalf("/help is missing %q:\n%s", cmd, helpOut)
		}
		if !strings.Contains(table, cmd) {
			t.Fatalf("README slash-command table is missing %q", cmd)
		}
	}
}

// readmeTable returns the "## Slash command" table from README.md.
func readmeTable(t *testing.T, readme string) string {
	t.Helper()
	start := strings.Index(readme, "## Slash command")
	if start < 0 {
		t.Fatal("README is missing the '## Slash command' section")
	}
	rest := readme[start:]
	end := strings.Index(rest[len("## Slash command"):], "\n## ")
	if end < 0 {
		t.Fatal("README '## Slash command' section has no closing heading")
	}
	return rest[:len("## Slash command")+end]
}

// The same drift once hit docs/index.html: it listed 15 invented
// tools instead of the 17 real ones from ToolDefs() (PR #10). Lock
// the tool list the same way — every ToolDefs name must appear there.
func TestDocsListsAllTools(t *testing.T) {
	html := readRepoFile(t, filepath.Join("docs", "index.html"))
	defs := agent.ToolDefs()
	if len(defs) != 17 {
		t.Fatalf("ToolDefs has %d tools, want 17 — update docs/index.html when adding a tool", len(defs))
	}
	for _, def := range defs {
		if !strings.Contains(html, "<li>"+def.Name+"</li>") {
			t.Fatalf("docs/index.html tool list is missing %q", def.Name)
		}
	}
}

// PR #22 wired all six TUI components into the REPL, but the
// public landing page still said nothing about them — the same docs
// drift PR #10 fixed for the tool list. Lock the new descriptions
// the same way: the page must describe the Terminal UI and the code
// search / cron features, must not call codesearch semantic search
// (it is keyword-ranked, "not semantic"), and must not present
// cron watch as a subcommand of /checkpoint — it is a route-x
// subcommand of its own. (PR #23.)
func TestDocsDescribesTuiAndStabilityFeatures(t *testing.T) {
	html := readRepoFile(t, filepath.Join("docs", "index.html"))
	for _, want := range []string{"Terminal UI", "spinner", "codesearch", "cron watch"} {
		if !strings.Contains(html, want) {
			t.Fatalf("docs/index.html must describe %q", want)
		}
	}
	if strings.Contains(html, "semantic search") {
		t.Fatal("docs/index.html must not call codesearch semantic search — it is keyword-ranked")
	}
	if strings.Contains(html, "/checkpoint</code> — subcommands:") {
		t.Fatal("docs/index.html must not present cron watch as a subcommand of /checkpoint")
	}
}

// The same drift hit CONTRIBUTING.md, which PR #11 fixed for the
// package list but not for the tools inside it: its `tools` bullet
// listed only 12 of the 17 ToolDefs names — edit, skill, webfetch,
// websearch and lsp were missing, even though webfetch/websearch/lsp
// are named in the `web`/`lsp` bullets below it, so a whole-file
// strings.Contains would pass while the tools bullet stayed wrong.
// Its `tui` bullet had the same gap: banner, box, markdown, spinner
// but no status bar or progress bar, both real components (v0.5.0).
// Lock both bullets, scoped to their own lines.
func TestContributingListsAllTools(t *testing.T) {
	contrib := readRepoFile(t, "CONTRIBUTING.md")
	toolsLine := contributingBullet(t, contrib, "`tools`")
	defs := agent.ToolDefs()
	if len(defs) != 17 {
		t.Fatalf("ToolDefs has %d tools, want 17 — update CONTRIBUTING.md when adding a tool", len(defs))
	}
	for _, def := range defs {
		if !strings.Contains(toolsLine, def.Name) {
			t.Fatalf("CONTRIBUTING.md `tools` bullet is missing %q:\n%s", def.Name, toolsLine)
		}
	}
	tuiLine := contributingBullet(t, contrib, "`tui`")
	for _, want := range []string{"status bar", "progress bar"} {
		if !strings.Contains(tuiLine, want) {
			t.Fatalf("CONTRIBUTING.md `tui` bullet is missing %q:\n%s", want, tuiLine)
		}
	}
}

// The same drift hit the README project tree, which PR #18 fixed
// in CONTRIBUTING.md but not here: the `tui/` line in
// "## Struktur proyek" listed only banner, box, markdown, spinner
// — status bar and progress bar were missing, even though both are
// real components (RenderStatusBar, RenderProgressBar in
// internal/tui) and both are named in the same README (v0.5.0
// paragraph and "## Yang tidak sama dengan `agy` asli") and in CONTRIBUTING.md. Lock all
// six on the tree line itself: a whole-file check would pass while
// the line stayed wrong, because both names appear elsewhere.
func TestReadmeTreeListsAllTuiComponents(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	tree := readmeSection(t, readme, "## Struktur proyek")
	var tuiLine string
	for _, line := range strings.Split(tree, "\n") {
		if strings.Contains(line, "tui/") {
			tuiLine = line
			break
		}
	}
	if tuiLine == "" {
		t.Fatal("README \"## Struktur proyek\" is missing the tui/ line")
	}
	for _, want := range []string{"banner", "status bar", "progress bar", "box", "markdown", "spinner"} {
		if !strings.Contains(tuiLine, want) {
			t.Fatalf("README project-tree tui/ line is missing %q:\n%s", want, tuiLine)
		}
	}
}

// The v0.5.0 TUI claim overstated the wiring: README and the main.go
// package comment both wrote all six TUI components as "used by the
// REPL banner", but in production code only RenderBanner is called
// (main.go, the REPL banner line) — RenderStatusBar,
// RenderProgressBar, RenderBox, RenderMarkdown and Spinner are only
// exercised by tests, and RenderToolCard (which uses two of them
// internally) is never called from production code either. That is
// the same "file without a caller" pattern v0.5.0 was meant to fix,
// so the claim was qualified in both README spots and in main.go
// (PR #20). PR #21 wires four of the five back in: the status bar
// and tool card for tool progress, the progress bar for the step
// cap, and markdown rendering for agent replies. PR #22 closes the
// last gap: the spinner's Start() now prints its frames, and the
// REPL spins it while waiting for the model, stopping at the first
// loop event — silent when stdout is not a terminal. The wording
// below locks the new state; the tools/ line still locks all 17
// ToolDefs names on that line itself.
func TestReadmeTuiWiringIsHonest(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	var v050Line string
	for _, line := range strings.Split(readme, "\n") {
		if strings.Contains(line, "v0.5.0 adalah rilis stabilitas") {
			v050Line = line
			break
		}
	}
	if v050Line == "" {
		t.Fatal("README is missing the v0.5.0 paragraph")
	}
	for _, want := range []string{"PR #21", "PR #22", "spinner menunggu jawaban model"} {
		if !strings.Contains(v050Line, want) {
			t.Fatalf("README v0.5.0 paragraph must describe the PR #21 wiring with %q:\n%s", want, v050Line)
		}
	}
	if strings.Contains(v050Line, "hanya banner yang dipakai di banner REPL") {
		t.Fatalf("README v0.5.0 paragraph must not carry the PR #20 qualification any more:\n%s", v050Line)
	}
	agy := readmeSection(t, readme, "## Yang tidak sama dengan `agy` asli")
	var agyLine string
	for _, line := range strings.Split(agy, "\n") {
		if strings.Contains(line, "komponen TUI (banner") {
			agyLine = line
			break
		}
	}
	if agyLine == "" {
		t.Fatal("README \"Yang tidak sama dengan `agy` asli\" is missing the TUI bullet")
	}
	for _, want := range []string{"(PR #21)", "(PR #22)", "spinner menunggu jawaban model"} {
		if !strings.Contains(agyLine, want) {
			t.Fatalf("README TUI bullet must describe the PR #21 wiring with %q:\n%s", want, agyLine)
		}
	}
	mainGo := readRepoFile(t, "main.go")
	for _, want := range []string{"PR #21 wires the rest", "PR #22 fixes the spinner"} {
		if !strings.Contains(mainGo, want) {
			t.Fatalf("main.go v0.5.0 package comment must describe the PR #21 wiring with %q", want)
		}
	}
	if strings.Contains(mainGo, "spinner) used by\n// the REPL banner") {
		t.Fatal("main.go v0.5.0 package comment must not claim all TUI components are used by the REPL banner")
	}
	tree := readmeSection(t, readme, "## Struktur proyek")
	var toolsLine string
	for _, line := range strings.Split(tree, "\n") {
		if strings.Contains(line, "├── tools/") {
			toolsLine = line
			break
		}
	}
	if toolsLine == "" {
		t.Fatal("README \"## Struktur proyek\" is missing the tools/ line")
	}
	if strings.Contains(toolsLine, "...") {
		t.Fatalf("README project-tree tools/ line must list all tools, not end with \"...\":\n%s", toolsLine)
	}
	for _, def := range agent.ToolDefs() {
		if !strings.Contains(toolsLine, def.Name) {
			t.Fatalf("README project-tree tools/ line is missing tool %q:\n%s", def.Name, toolsLine)
		}
	}
}

// PR #21 wires the TUI components into replEvent: the status bar
// for a tool starting, the tool card for its result, and the status
// bar plus progress bar when the loop finishes. Call replEvent
// directly with synthetic events and assert each component actually
// renders — if the wiring regresses to plain prints, the tool name,
// status codes and progress percentage disappear from the output.
func TestReplEventRendersTuiComponents(t *testing.T) {
	out := captureStdout(t, func() {
		replEvent(loop.Event{Kind: "tool_start", Tool: "read", Step: 1})
	})
	for _, want := range []string{"[INFO 0]", "read", "step 1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool_start must render a status bar with %q, got:\n%s", want, out)
		}
	}

	out = captureStdout(t, func() {
		replEvent(loop.Event{Kind: "tool_result", Tool: "read", Text: "hello world", Step: 1})
	})
	for _, want := range []string{"Tool: read", "[OK 200]", "hello world"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool_result must render a tool card with %q, got:\n%s", want, out)
		}
	}

	out = captureStdout(t, func() {
		replEvent(loop.Event{Kind: "tool_result", Tool: "run", Text: "boom", IsError: true, Step: 2})
	})
	for _, want := range []string{"Tool: run", "[ERROR 500]", "boom"} {
		if !strings.Contains(out, want) {
			t.Fatalf("error tool_result must render an error tool card with %q, got:\n%s", want, out)
		}
	}

	// One very long result line must be capped, not stretch the card.
	long := strings.Repeat("x", 200)
	out = captureStdout(t, func() {
		replEvent(loop.Event{Kind: "tool_result", Tool: "read", Text: long, Step: 1})
	})
	if !strings.Contains(out, "...") {
		t.Fatalf("long tool_result must be truncated with \"...\", got:\n%s", out)
	}
	if strings.Contains(out, long) {
		t.Fatalf("long tool_result must not print the full %d-char line", len(long))
	}

	out = captureStdout(t, func() {
		replEvent(loop.Event{Kind: "done", Step: 7, Usage: loop.Usage{InputTokens: 10, OutputTokens: 5}})
	})
	for _, want := range []string{"10 in / 5 out tokens", "70%"} {
		if !strings.Contains(out, want) {
			t.Fatalf("done must render the token summary and progress bar with %q, got:\n%s", want, out)
		}
	}
}

// PR #22 wires a spinner into the REPL while it waits for the
// model: spinnerEvent stops the spinner on the first loop event
// and forwards the event to replEvent. Drive it with a
// buffer-backed spinner and assert both sides: the spinner must
// have rendered before the event, must write nothing more after
// it, and the wrapped handler must still receive the event.
func TestSpinnerEventStopsSpinner(t *testing.T) {
	var buf bytes.Buffer
	sp := tui.NewSpinnerTo("Thinking...", &buf)
	sp.Start()
	time.Sleep(350 * time.Millisecond) // let at least one frame render
	var got []loop.Event
	wrapped := spinnerEvent(sp, func(e loop.Event) { got = append(got, e) })
	wrapped(loop.Event{Kind: "text", Text: "hi", Step: 1})
	if len(got) != 1 || got[0].Kind != "text" {
		t.Fatalf("wrapped handler must forward the event, got %v", got)
	}
	if !strings.Contains(buf.String(), "Thinking...") {
		t.Fatalf("spinner must have rendered a frame before the event, got %q", buf.String())
	}
	// After Stop, no further frames may appear.
	n := buf.Len()
	time.Sleep(250 * time.Millisecond)
	if buf.Len() != n {
		t.Fatalf("spinner kept writing after Stop: %q", buf.String()[n:])
	}
	sp.Stop() // double stop must stay safe
}

// scriptedReplyProvider answers every agentic step with one fixed
// text-only reply, so the REPL can be driven without a live model.
type scriptedReplyProvider struct{ reply string }

func (p *scriptedReplyProvider) CompleteTools(ctx context.Context, messages []loop.Message, defs []loop.ToolDef) (loop.Message, loop.Usage, error) {
	return loop.Message{Role: "assistant", Content: p.reply}, loop.Usage{}, nil
}

// PR #21 renders the agent reply as markdown in the REPL, but the
// session must still store the raw reply. Drive runREPL end to end
// with piped stdin and a scripted provider, then check both sides:
// the terminal shows the rendered form (the "# " heading becomes a
// bold cyan line, so the raw "# Done" must not appear), while the
// session file on disk holds the raw "# Done" text.
func TestReplRendersAgentReplyAsMarkdown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	cfg := config.Config{Mode: "auto", Sandbox: "workspace-write", Approval: "on-request", Provider: "local"}
	ag := agent.New(cfg, dir, tools.AutoApprove)
	ag.ToolProviderOverride = &scriptedReplyProvider{reply: "# Done\n\nAll good."}
	oldStdin := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader("hello\n/exit\n"))
	defer func() { stdinReader = oldStdin }()

	out := captureStdout(t, func() { runREPL(ag, cfg, dir) })
	rendered := tui.RenderMarkdown("# Done\n\nAll good.")
	if !strings.Contains(out, rendered) {
		t.Fatalf("REPL must print the markdown-rendered reply %q, got:\n%s", rendered, out)
	}
	if strings.Contains(out, "# Done") {
		t.Fatalf("REPL must not print the raw markdown reply, got:\n%s", out)
	}

	files, err := filepath.Glob(filepath.Join(home, ".route-x", "sessions", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected exactly one saved session, got %v, err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("cannot read saved session: %v", err)
	}
	if !strings.Contains(string(data), "# Done") {
		t.Fatalf("session must store the raw reply, got:\n%s", data)
	}
	if strings.Contains(string(data), "\x1b[1m\x1b[36mDone") {
		t.Fatalf("session must not store the ANSI-rendered reply, got:\n%s", data)
	}
}

// contributingBullet returns the single bullet line starting with
// "- <name> –" from CONTRIBUTING.md, so a name mentioned in another
// bullet cannot mask its absence from this one.
func contributingBullet(t *testing.T, contrib, name string) string {
	t.Helper()
	for _, line := range strings.Split(contrib, "\n") {
		if strings.HasPrefix(line, "- "+name+" –") {
			return line
		}
	}
	t.Fatalf("CONTRIBUTING.md is missing the %s bullet", name)
	return ""
}

// The install one-liner lived only in docs/index.html while the
// README "Install / build" section showed build-from-source only
// (PR #16): a reader following the README had to install Go and
// clone, even though a release binary needs neither. Lock the
// one-liner in the README so it cannot silently disappear again.
// The install one-liner puts the binary in /usr/local/bin or
// ~/.local/bin (PR #16), not in the current directory — so the
// example after it must be `route-x --version`, not
// `./route-x --version` (that form is only right after a local
// `go build -o route-x .`). The same drift hit the "## Keamanan"
// section: it said default mode "selalu" asks y/N, but approval
// `never` wins over the mode (main.go:168, /approval handler) —
// the wording SECURITY.md was qualified for in PR #16, while the
// README bullet was left unqualified. Lock both.
func TestReadmeInstallExampleAndSecurityQualification(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	install := readmeSection(t, readme, "## Install / build")
	// Only the one-liner part (before "Atau build dari sumber"):
	// the build-from-source part below it correctly uses ./route-x,
	// because `go build -o route-x .` puts the binary in the cwd.
	oneLiner, _, _ := strings.Cut(install, "Atau build dari sumber")
	if strings.Contains(oneLiner, "./route-x --version") {
		t.Fatalf("README install one-liner must use `route-x --version` (install.sh puts the binary in /usr/local/bin or ~/.local/bin, not the current directory):\n%s", oneLiner)
	}
	if !strings.Contains(oneLiner, "route-x --version") {
		t.Fatalf("README install one-liner is missing the version check:\n%s", oneLiner)
	}
	security := readmeSection(t, readme, "## Keamanan")
	for _, want := range []string{"approval", "never"} {
		if !strings.Contains(security, want) {
			t.Fatalf("README \"## Keamanan\" must qualify the default-mode prompt with %q:\n%s", want, security)
		}
	}
	if strings.Contains(security, "selalu minta persetujuan") {
		t.Fatalf("README \"## Keamanan\" must not claim default mode always prompts:\n%s", security)
	}
}

// readmeSection returns the README section starting at heading.
func readmeSection(t *testing.T, readme, heading string) string {
	t.Helper()
	start := strings.Index(readme, heading)
	if start < 0 {
		t.Fatalf("README is missing the %q section", heading)
	}
	rest := readme[start:]
	end := strings.Index(rest[len(heading):], "\n## ")
	if end < 0 {
		return rest
	}
	return rest[:len(heading)+end]
}

func TestReadmeDocumentsInstallScript(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	for _, want := range []string{
		"https://raw.githubusercontent.com/dbrok89-alt/Route-X-CLI/main/install.sh",
		"curl -fsSL",
		"~/.local/bin",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("README install section is missing %q", want)
		}
	}
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

func TestSlashCodesearch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workdir := t.TempDir()
	cfg := config.Default()
	ag := agent.New(cfg, workdir, tools.AutoApprove)
	sess := &session.Session{ID: "test", Model: cfg.Model, Mode: cfg.Mode}
	if err := os.WriteFile(filepath.Join(workdir, "a.go"), []byte("package main\n\nfunc ProcessData(x string) {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		handleSlash(ag, &cfg, sess, "/codesearch ProcessData", workdir)
	})
	if !strings.Contains(out, "func ProcessData") {
		t.Fatalf("/codesearch output missing declaration:\n%s", out)
	}
	out = captureStdout(t, func() {
		handleSlash(ag, &cfg, sess, "/codesearch NoSuchSymbolZZZ", workdir)
	})
	if !strings.Contains(out, "No code matches") {
		t.Fatalf("/codesearch no-match output wrong:\n%s", out)
	}
}
