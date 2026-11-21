package main

import (
	"os"
	"path/filepath"
	"runtime"
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
