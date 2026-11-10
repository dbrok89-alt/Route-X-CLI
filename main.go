// Command route-x is a terminal coding agent inspired by Google's
// Antigravity CLI (agy): a single Go binary, interactive REPL with
// slash commands, execution modes (default / accept-edits / auto /
// plan), background subagents, skills, session resume, and a
// non-interactive print mode (-p) for scripts and CI.
//
// v0.2.0 merges the four CLI families: Antigravity (modes, subagents),
// Codex (sandbox + approval split, AGENTS.md, /review), Claude Code
// (hooks, MCP client, allow/deny permissions, /context) and Hermes
// (multi-provider, persistent memory, auto-learned skills, /verify).
//
// v0.3.0 adds the missing core: a model-driven agentic loop with
// tool calling (read/search/list/write/edit/run) for Gemini, OpenAI,
// Anthropic and Ollama, plus /compact, token/cost tracking,
// persistent subagents, plugins, serve/share, cron and checkpoints
// (OpenCode / ngrok / Hermes features, ported honestly).
//
// Usage:
//
//	route-x                  # interactive REPL
//	route-x -p "cari TODO"   # print mode, text output
//	route-x -p --output json "baca main.go"
//	route-x --mode plan -p "buat file demo.go"
//	route-x init             # create .route-x/skills example
//	route-x --version
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"route-x/internal/agent"
	"route-x/internal/checkpoint"
	"route-x/internal/config"
	"route-x/internal/cron"
	"route-x/internal/hooks"
	"route-x/internal/loop"
	"route-x/internal/mcp"
	"route-x/internal/memory"
	"route-x/internal/serve"
	"route-x/internal/session"
	"route-x/internal/skills"
	"route-x/internal/tools"
)

const version = "0.3.1"

// stdinReader is the single shared stdin reader for the REPL and
// for approval prompts. See runREPL for why it must be shared.
var stdinReader = bufio.NewReader(os.Stdin)

// printUsage prints the --help text. It is a separate function (instead
// of an inline closure) so tests can call it and assert the subcommands
// are listed — v0.3.0 shipped serve/share/cron without mentioning them
// here, so users had no way to discover them from --help.
func printUsage() {
	fmt.Fprintf(os.Stderr, "Route-X %s — terminal coding agent (inspired by Antigravity CLI / agy)\n\n", version)
	fmt.Fprintf(os.Stderr, "Usage:\n  route-x [prompt]\n  route-x -p \"prompt\" [--output json] [--mode plan]\n  route-x init\n  route-x serve [--port 8080]\n  route-x share [--provider ngrok|cloudflared|localhost.run] [--port 8080]\n  route-x cron add <name> \"<schedule>\" \"<prompt>\" | list | run | remove <name>\n  route-x --version\n\nFlags:\n")
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\nSlash commands (interactive mode):\n  /help /model [name] /mode [name] /provider [name] /goal <task>\n  /plan <task> /agent <task> /agents /usage /skills /config\n  /resume [id] /clear /diff /exit /compact /plugins\n  /permissions /sandbox [name] /approval [name] /review /verify\n  /context /memory /remember <fact> /learn <name> /hooks /mcp\n  /checkpoint /search <word> /lint\n")
}

func main() {
	var (
		printMode = flag.Bool("p", false, "print mode: run once, non-interactively, then exit")
		printLong = flag.Bool("print", false, "same as -p")
		oneshot   = flag.Bool("z", false, "one-shot mode, same as -p (Hermes -z)")
		output    = flag.String("output", "text", "print-mode output: text | json | stream-json")
		model     = flag.String("model", "", "model name (default from config)")
		mode      = flag.String("mode", "", "execution mode: default | accept-edits | auto | plan")
		provider  = flag.String("provider", "", "provider: local | gemini | openai | anthropic | ollama | intern")
		sandbox   = flag.String("sandbox", "", "sandbox: read-only | workspace-write | danger-full-access")
		approval  = flag.String("approval", "", "approval policy: on-request | never")
		baseURL   = flag.String("base-url", "", "OpenAI-compatible endpoint (openai/ollama provider)")
		showVer   = flag.Bool("version", false, "show version")
		showVerS  = flag.Bool("v", false, "show version (short)")
	)
	flag.Usage = printUsage
	flag.Parse()

	if *showVer || *showVerS {
		fmt.Printf("route-x %s\n", version)
		return
	}

	// Subcommand: init — create an example skill. Print mode wins
	// over init, so `route-x -p "init"` prints a plan instead of
	// creating files.
	if !*printMode && !*printLong && !*oneshot && flag.NArg() > 0 && flag.Arg(0) == "init" {
		if err := doInit(); err != nil {
			fmt.Fprintln(os.Stderr, "init:", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not load config:", err)
	}
	if *model != "" {
		cfg.Model = *model
	}
	if *mode != "" {
		if !config.ValidMode(*mode) {
			fmt.Fprintf(os.Stderr, "invalid --mode %q\n", *mode)
			os.Exit(2)
		}
		cfg.Mode = *mode
	}
	if *provider != "" {
		if !config.ValidProvider(*provider) {
			fmt.Fprintf(os.Stderr, "invalid --provider %q\n", *provider)
			os.Exit(2)
		}
		cfg.Provider = *provider
		// Switching provider without naming a model: use that
		// provider's default model — but only when the stored
		// model is still the generic default, so an explicitly
		// chosen model is never clobbered.
		if *model == "" && cfg.Model == config.Default().Model {
			cfg.Model = config.DefaultModelFor(*provider)
		}
	}
	if *sandbox != "" {
		if !config.ValidSandbox(*sandbox) {
			fmt.Fprintf(os.Stderr, "invalid --sandbox %q\n", *sandbox)
			os.Exit(2)
		}
		cfg.Sandbox = *sandbox
	}
	if *approval != "" {
		if !config.ValidApproval(*approval) {
			fmt.Fprintf(os.Stderr, "invalid --approval %q\n", *approval)
			os.Exit(2)
		}
		cfg.Approval = *approval
	}
	if *baseURL != "" {
		cfg.BaseURL = *baseURL
	}

	workdir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	approve := approveForMode(cfg.Mode)
	if cfg.Approval == "never" {
		approve = tools.AutoApprove
	}
	ag := agent.New(cfg, workdir, approve)

	// Subcommands: serve / share — expose the agent over HTTP.
	if flag.NArg() > 0 && (flag.Arg(0) == "serve" || flag.Arg(0) == "share") {
		if err := runServeOrShare(ag, flag.Arg(0), flag.Args()[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Subcommand: cron — scheduled agent tasks.
	if flag.NArg() > 0 && flag.Arg(0) == "cron" {
		if err := runCron(ag, flag.Args()[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if *printMode || *printLong || *oneshot {
		prompt := strings.Join(flag.Args(), " ")
		if prompt == "" {
			data, _ := io.ReadAll(os.Stdin)
			prompt = strings.TrimSpace(string(data))
		}
		if prompt == "" {
			fmt.Fprintln(os.Stderr, "print mode needs a prompt: route-x -p \"your task\"")
			os.Exit(2)
		}
		runPrint(ag, prompt, *output)
		return
	}

	// Interactive REPL (also handles a prompt given as args).
	if flag.NArg() > 0 {
		prompt := strings.Join(flag.Args(), " ")
		reply, err := ag.Run(context.Background(), prompt)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(reply)
		return
	}
	runREPL(ag, cfg, workdir)
}

func doInit() error {
	dir := filepath.Join(".route-x", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	skillPath := filepath.Join(dir, "lint.md")
	if _, err := os.Stat(skillPath); err == nil {
		fmt.Println("Already exists, not overwritten:", skillPath)
		return nil
	}
	skill := "# lint\n\n" +
		"Periksa file Go di workspace: cari TODO, FIXME, dan fmt.Println yang tertinggal.\n" +
		"Laporkan file dan nomor barisnya, jangan ubah file apapun.\n"
	if err := os.WriteFile(skillPath, []byte(skill), 0o644); err != nil {
		return err
	}
	fmt.Println("Created .route-x/skills/lint.md — try /skills, then /lint")
	// Also create a starter AGENTS.md (Codex/Claude-style project
	// instructions) when the project does not have one yet.
	if _, err := os.Stat("AGENTS.md"); os.IsNotExist(err) {
		agents := "# AGENTS.md\n\n" +
			"Instruksi proyek untuk Route-X (dan agen lain yang membaca file ini):\n" +
			"- Build: `go build -o route-x .`\n" +
			"- Test: `go test ./...`\n" +
			"- Jangan ubah file di luar folder proyek.\n"
		if err := os.WriteFile("AGENTS.md", []byte(agents), 0o644); err == nil {
			fmt.Println("Created AGENTS.md — edit it with your build/test commands")
		}
	}
	return nil
}

// approveForMode returns the approval gate for a mode. It is the
// single place that maps mode -> gate, so /mode, /goal and /plan
// can never drift out of sync with startup.
func approveForMode(mode string) tools.ApproveFunc {
	switch mode {
	case "auto", "accept-edits":
		return tools.AutoApprove
	case "plan":
		return tools.NeverApprove
	default:
		return interactiveApprove
	}
}

// interactiveApprove asks y/n on stdin. Used only in default mode.
// It reads from the shared stdinReader, never a fresh reader.
func interactiveApprove(description string) bool {
	fmt.Println(description)
	fmt.Print("Approve? [y/N] ")
	line, _ := stdinReader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// runPrompt is the single entry point for a user task: the
// agentic loop when the provider can call tools, the keyword
// dispatch otherwise (local provider). onEvent prints tool
// progress in the REPL; print mode passes nil.
func runPrompt(ag *agent.Agent, prompt string, onEvent func(loop.Event)) (string, error) {
	if ag.HasToolProvider() {
		return ag.RunAgent(context.Background(), prompt, onEvent)
	}
	return ag.Run(context.Background(), prompt)
}

// replEvent renders agentic-loop progress in the REPL.
func replEvent(e loop.Event) {
	switch e.Kind {
	case "tool_start":
		fmt.Printf("  ▸ %s\n", e.Tool)
	case "tool_result":
		if e.IsError {
			fmt.Printf("    ✗ %s\n", firstLine(e.Text))
		}
	case "done":
		fmt.Printf("  [used %d in / %d out tokens]\n", e.Usage.InputTokens, e.Usage.OutputTokens)
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// runServeOrShare implements `route-x serve` and `route-x share`.
// serve exposes the agent on loopback HTTP with a bearer token
// (printed once, keep it secret). share additionally starts a
// tunnel (ngrok by default) so the server is reachable publicly.
// Anyone holding the public URL and the token can ask the agent
// to read files and run commands — that is printed as a warning.
func runServeOrShare(ag *agent.Agent, cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	port := fs.Int("port", 8080, "local port to serve on")
	tunnelProvider := fs.String("provider", "ngrok", "tunnel provider for share: ngrok | cloudflared | localhost.run")
	allowPublic := fs.Bool("allow-public-bind", false, "allow binding a non-loopback address (dangerous)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	token, err := serve.NewToken()
	if err != nil {
		return fmt.Errorf("could not create serve token: %w", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	srv, err := serve.New(addr, token, func(prompt string) (string, error) {
		return runPrompt(ag, prompt, nil)
	}, *allowPublic)
	if err != nil {
		return err
	}
	fmt.Printf("Serving on http://%s\n", addr)
	fmt.Printf("Bearer token (shown once — keep it secret): %s\n", token)
	fmt.Println("Send prompts with: curl -H \"Authorization: Bearer <token>\" -d '{\"prompt\": \"...\"}' http://127.0.0.1:" + fmt.Sprint(*port) + "/prompt")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if cmd == "serve" {
		go func() {
			if err := srv.ListenAndServe(); err != nil {
				fmt.Fprintln(os.Stderr, "serve:", err)
				os.Exit(1)
			}
		}()
		fmt.Println("Press Ctrl-C to stop.")
		<-sigCh
		return nil
	}

	// share: start the server, then the tunnel.
	go func() {
		_ = srv.ListenAndServe()
	}()
	time.Sleep(300 * time.Millisecond) // let the server bind
	spec := serve.TunnelSpec{Provider: *tunnelProvider, Port: *port}
	tunnel, err := spec.Command()
	if err != nil {
		return err
	}
	fmt.Printf("Starting tunnel (%s)...\n", *tunnelProvider)
	if *tunnelProvider == "ngrok" {
		tunnel.Stdout = os.Stdout
		tunnel.Stderr = os.Stderr
		if err := tunnel.Start(); err != nil {
			return err
		}
		defer func() { _ = tunnel.Process.Kill() }()
		url, err := serve.NgrokPublicURL(30 * time.Second)
		if err != nil {
			return err
		}
		fmt.Printf("\nPublic URL: %s\n", url)
	} else {
		// cloudflared / localhost.run print the URL in stdout.
		out, err := tunnel.StdoutPipe()
		if err != nil {
			return err
		}
		tunnel.Stderr = os.Stderr
		if err := tunnel.Start(); err != nil {
			return err
		}
		defer func() { _ = tunnel.Process.Kill() }()
		url, err := waitForTunnelURL(out, *tunnelProvider, 30*time.Second)
		if err != nil {
			return err
		}
		fmt.Printf("\nPublic URL: %s\n", url)
	}
	fmt.Println("WARNING: anyone with this URL and the bearer token can ask the agent to read files and run commands. Press Ctrl-C to stop sharing.")
	<-sigCh
	return nil
}

// waitForTunnelURL scans tunnel stdout for a public URL.
func waitForTunnelURL(r io.Reader, provider string, timeout time.Duration) (string, error) {
	type result struct {
		url string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			var url string
			var ok bool
			if provider == "cloudflared" {
				url, ok = serve.ParseCloudflaredURL(line)
			} else {
				url, ok = serve.ParseLocalhostRunURL(line)
			}
			if ok {
				ch <- result{url: url}
				return
			}
		}
		ch <- result{err: fmt.Errorf("tunnel output ended without a public URL")}
	}()
	select {
	case res := <-ch:
		return res.url, res.err
	case <-time.After(timeout):
		return "", fmt.Errorf("timed out waiting for a public URL from %s", provider)
	}
}

// runCron implements `route-x cron add|list|run|remove`.
// add <name> "<min hour day month weekday>" "<prompt>"
// list — show tasks; run — execute due tasks now;
// remove <name>. Cron runs are unattended, so approvals are
// auto-approved for the run and the task's prompt is executed
// with the agent's configured mode.
func runCron(ag *agent.Agent, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: route-x cron add <name> \"<schedule>\" \"<prompt>\" | list | run | remove <name>")
	}
	store, err := cron.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		if len(args) != 4 {
			return fmt.Errorf("usage: route-x cron add <name> \"<min hour day month weekday>\" \"<prompt>\"")
		}
		if err := store.Add(cron.Task{Name: args[1], Schedule: args[2], Prompt: args[3]}); err != nil {
			return err
		}
		if err := store.Save(); err != nil {
			return err
		}
		fmt.Printf("Cron task %q added (%s).\n", args[1], args[2])
		fmt.Println("Run it with `route-x cron run` (add that to your own scheduler — Route-X is not a daemon).")
		return nil
	case "list":
		if len(store.Tasks) == 0 {
			fmt.Println("No cron tasks.")
			return nil
		}
		for _, t := range store.Tasks {
			fmt.Printf("- %s  %s  %q  (last run: %s)\n", t.Name, t.Schedule, t.Prompt, t.LastRun)
		}
		return nil
	case "remove":
		if len(args) != 2 {
			return fmt.Errorf("usage: route-x cron remove <name>")
		}
		if !store.Remove(args[1]) {
			return fmt.Errorf("no cron task named %q", args[1])
		}
		return store.Save()
	case "run":
		now := time.Now()
		due := store.DueTasks(now)
		if len(due) == 0 {
			fmt.Println("No cron tasks due.")
			return nil
		}
		// Unattended run: approvals auto-approved.
		oldApprove := ag.Approve
		ag.Approve = tools.AutoApprove
		defer func() { ag.Approve = oldApprove }()
		for _, t := range due {
			fmt.Printf("Running cron task %q: %s\n", t.Name, t.Prompt)
			reply, err := runPrompt(ag, t.Prompt, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "task %q failed: %v\n", t.Name, err)
			} else {
				fmt.Println(reply)
			}
			store.MarkRun(t.Name, now)
		}
		return store.Save()
	}
	return fmt.Errorf("unknown cron subcommand %q (add | list | run | remove)", args[0])
}

func runPrint(ag *agent.Agent, prompt, output string) {
	reply, err := runPrompt(ag, prompt, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	switch output {
	case "json":
		data, _ := json.Marshal(map[string]string{
			"model": ag.Config.Model, "mode": ag.Config.Mode,
			"provider": ag.Config.Provider, "result": reply,
		})
		fmt.Println(string(data))
	case "stream-json":
		for _, line := range strings.Split(reply, "\n") {
			data, _ := json.Marshal(map[string]string{"type": "text", "text": line})
			fmt.Println(string(data))
		}
	default:
		fmt.Println(reply)
	}
}

func runREPL(ag *agent.Agent, cfg config.Config, workdir string) {
	fmt.Printf("Route-X %s — model %s · mode %s · provider %s · sandbox %s · approval %s\n",
		version, cfg.Model, cfg.Mode, cfg.Provider, cfg.Sandbox, cfg.Approval)
	fmt.Println("Type a task in plain language, or /help for slash commands. /exit to quit.")
	if instr := ag.LoadInstructions(); instr != "" {
		fmt.Println("Loaded project instructions (AGENTS.md / CLAUDE.md).")
	}
	// Claude-style lifecycle hooks, from .route-x/hooks.json.
	// Hooks never run in plan mode.
	hk := hooks.Load(workdir)
	fire := func(event string) {
		if cfg.Mode == "plan" {
			return
		}
		if out, err := hk.Fire(event); err != nil {
			fmt.Fprintln(os.Stderr, "hook", event, ":", err)
		} else if strings.TrimSpace(out) != "" {
			fmt.Printf("[hook %s] %s\n", event, strings.TrimSpace(out))
		}
	}
	fire("SessionStart")
	defer fire("Stop")
	sess := &session.Session{
		ID: session.NewID(), Model: cfg.Model, Mode: cfg.Mode,
		Started: time.Now().Format(time.RFC3339),
	}
	// Only save a session that actually had a conversation.
	defer func() {
		if len(sess.Messages) > 0 {
			_ = session.Save(sess)
		}
	}()

	// The REPL and approval prompts share stdinReader (see the
	// package-level comment). The old code used a bufio.Scanner
	// for input and a fresh bufio.Reader for every approval —
	// the scanner could buffer the "y" answer ahead of the
	// approval reader, so approvals were silently lost.
	printedCheckpoint := ""
	reader := stdinReader
	for {
		fmt.Print("route-x> ")
		raw, err := reader.ReadString('\n')
		if err != nil && raw == "" {
			if err != io.EOF {
				fmt.Fprintln(os.Stderr, "error reading input:", err)
			}
			fmt.Println()
			break
		}
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if handleSlash(ag, &cfg, sess, line, workdir) {
				break // /exit
			}
			continue
		}
		sess.Messages = append(sess.Messages, session.Message{Role: "user", Content: line, Time: time.Now().Format(time.RFC3339)})
		prevDiff := ag.LastDiff
		fire("PreToolUse")
		reply, err := runPrompt(ag, line, replEvent)
		fire("PostToolUse")
		if ag.LastDiff != prevDiff {
			fire("PostEdit")
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		fmt.Println(reply)
		if ag.LastCheckpointID != "" && ag.LastCheckpointID != printedCheckpoint {
			printedCheckpoint = ag.LastCheckpointID
			fmt.Printf("  [checkpoint %s created — /checkpoint restore %s to undo]\n", printedCheckpoint, printedCheckpoint)
		}
		sess.Messages = append(sess.Messages, session.Message{Role: "agent", Content: reply, Time: time.Now().Format(time.RFC3339)})
		_ = session.Save(sess)
	}
}

// handleSlash runs one slash command. It returns true when the REPL
// should exit.
func handleSlash(ag *agent.Agent, cfg *config.Config, sess *session.Session, line, workdir string) bool {
	fields := strings.Fields(line)
	cmd := fields[0]
	arg := ""
	if len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	switch cmd {
	case "/exit", "/quit":
		fmt.Println("Bye, bos.")
		return true
	case "/help":
		fmt.Println("Slash commands:\n" +
			"  /help              show this help\n" +
			"  /model [name]      show / set model\n" +
			"  /mode [name]       show / set mode (default, accept-edits, auto, plan)\n" +
			"  /provider [name]   show / set provider (local, gemini, openai, anthropic, ollama, intern)\n" +
			"  /sandbox [name]    show / set sandbox (read-only, workspace-write, danger-full-access)\n" +
			"  /approval [name]   show / set approval (on-request, never)\n" +
			"  /permissions       show sandbox, approval, allow/deny lists\n" +
			"  /goal <task>       run a task autonomously (auto-approves edits)\n" +
			"  /plan <task>       plan a task without writing anything\n" +
			"  /agent <task>      dispatch a background subagent\n" +
			"  /agents            list subagents (incl. earlier runs)\n" +
			"  /compact           summarise session into one checkpoint\n" +
			"  /plugins           list plugins from .route-x/plugins\n" +
			"  /review            review git diff + TODO/FIXME\n" +
			"  /verify            run project checks and report evidence\n" +
			"  /context           show session context usage\n" +
			"  /memory            show persistent memory\n" +
			"  /remember <fact>   save a fact to persistent memory\n" +
			"  /learn <name> [desc] save a repeated task as a skill\n" +
			"  /hooks             list lifecycle hooks\n" +
			"  /mcp               list MCP servers and their tools\n" +
			"  /mcp-call <srv> <tool> [json] call an MCP tool\n" +
			"  /usage             show model / mode / provider + tokens & cost\n" +
			"  /checkpoint [list | restore <id>] snapshot & undo workspace\n" +
			"  /search <word>     search saved sessions\n" +
			"  /skills            list skills from .route-x/skills\n" +
			"  /lint              run the lint skill (search TODO / FIXME)\n" +
			"  /config            show config (API key masked)\n" +
			"  /resume [id]       list sessions, or resume one\n" +
			"  /diff              show the last diff\n" +
			"  /clear             clear the screen\n" +
			"  /exit              quit")
	case "/model":
		if arg == "" {
			fmt.Println("model:", cfg.Model)
		} else {
			cfg.Model = arg
			ag.Config.Model = arg
			_ = config.Save(*cfg)
			fmt.Println("model set to", arg)
		}
	case "/mode":
		if arg == "" {
			fmt.Println("mode:", cfg.Mode)
		} else if config.ValidMode(arg) {
			cfg.Mode = arg
			ag.Config.Mode = arg
			// The approval gate must follow the mode, otherwise
			// switching mode in the REPL had no effect at all.
			// Approval policy "never" always wins over the mode.
			if cfg.Approval == "never" {
				ag.Approve = tools.AutoApprove
			} else {
				ag.Approve = approveForMode(arg)
			}
			_ = config.Save(*cfg)
			fmt.Println("mode set to", arg, "(writes/shell are re-gated from now on)")
		} else {
			fmt.Println("invalid mode:", arg)
		}
	case "/provider":
		if arg == "" {
			fmt.Println("provider:", cfg.Provider)
		} else if config.ValidProvider(arg) {
			changed := cfg.Provider != arg
			cfg.Provider = arg
			ag.Config.Provider = arg
			if changed && cfg.Model == config.Default().Model {
				cfg.Model = config.DefaultModelFor(arg)
				ag.Config.Model = cfg.Model
			}
			_ = config.Save(*cfg)
			fmt.Println("provider set to", arg, "(model:", cfg.Model+")")
		} else {
			fmt.Println("invalid provider:", arg)
		}
	case "/sandbox":
		if arg == "" {
			fmt.Println("sandbox:", cfg.Sandbox)
		} else if config.ValidSandbox(arg) {
			cfg.Sandbox = arg
			ag.Config.Sandbox = arg
			_ = config.Save(*cfg)
			fmt.Println("sandbox set to", arg)
		} else {
			fmt.Println("invalid sandbox:", arg)
		}
	case "/approval":
		if arg == "" {
			fmt.Println("approval:", cfg.Approval)
		} else if config.ValidApproval(arg) {
			cfg.Approval = arg
			ag.Config.Approval = arg
			if arg == "never" {
				ag.Approve = tools.AutoApprove
			} else {
				ag.Approve = approveForMode(cfg.Mode)
			}
			_ = config.Save(*cfg)
			fmt.Println("approval set to", arg)
		} else {
			fmt.Println("invalid approval:", arg)
		}
	case "/permissions":
		fmt.Printf("sandbox: %s\napproval: %s\nmode: %s\nallow: %v\ndeny: %v\n",
			cfg.Sandbox, cfg.Approval, cfg.Mode, cfg.Allow, cfg.Deny)
	case "/review":
		reply, err := ag.Review()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		} else {
			fmt.Println(reply)
		}
	case "/verify":
		reply, err := ag.Verify()
		fmt.Println(reply)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	case "/context":
		chars := 0
		for _, m := range sess.Messages {
			chars += len(m.Content)
		}
		instr := ag.LoadInstructions()
		mem := memory.Read("MEMORY.md")
		fmt.Printf("messages: %d\nchars: %d (est. tokens ~%d)\nfiles in workdir: %d\ninstructions (AGENTS.md/CLAUDE.md): %d chars\nmemory: %d chars\n",
			len(sess.Messages), chars, chars/4,
			len(tools.ListFiles(ag.Workdir, 1000)), len(instr), len(mem))
	case "/memory":
		if mem := memory.Read("MEMORY.md"); mem == "" {
			fmt.Println("No persistent memory yet. Use /remember <fact>.")
		} else {
			fmt.Println(mem)
		}
	case "/remember":
		if arg == "" {
			fmt.Println("usage: /remember <fact>")
			break
		}
		if _, err := memory.Remember(arg); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		} else {
			fmt.Println("Remembered.")
		}
	case "/learn":
		if arg == "" {
			fmt.Println("usage: /learn <name> [description]")
			break
		}
		parts := strings.Fields(arg)
		desc := "Skill learned from a repeated task."
		if len(parts) > 1 {
			desc = strings.Join(parts[1:], " ")
		}
		reply, err := ag.Learn(parts[0], desc)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		} else {
			fmt.Println(reply)
		}
	case "/hooks":
		listed := hooks.Load(ag.Workdir).List()
		if len(listed) == 0 {
			fmt.Println("No hooks configured. Add .route-x/hooks.json (SessionStart, PreToolUse, PostToolUse, PostEdit, Stop).")
		}
		for _, h := range listed {
			fmt.Println("-", h)
		}
	case "/mcp":
		mcfg := mcp.Load(ag.Workdir)
		if len(mcfg.Servers) == 0 {
			fmt.Println("No MCP servers configured. Add .route-x/mcp.json.")
			break
		}
		for name, srv := range mcfg.Servers {
			fmt.Printf("- %s", name)
			if srv.URL != "" {
				fmt.Printf(" (http %s)\n", srv.URL)
				continue
			}
			client, err := mcp.Connect(srv)
			if err != nil {
				fmt.Printf(" — connect failed: %v\n", err)
				continue
			}
			toolsList, err := client.ListTools()
			_ = client.Close()
			if err != nil {
				fmt.Printf(" — list tools failed: %v\n", err)
				continue
			}
			fmt.Printf(" (%d tools)\n", len(toolsList))
			for _, t := range toolsList {
				fmt.Printf("    - %s: %s\n", t.Name, t.Description)
			}
		}
	case "/mcp-call":
		parts := strings.Fields(arg)
		if len(parts) < 2 {
			fmt.Println("usage: /mcp-call <server> <tool> [json args]")
			break
		}
		mcfg := mcp.Load(ag.Workdir)
		srv, ok := mcfg.Servers[parts[0]]
		if !ok {
			fmt.Println("unknown MCP server:", parts[0])
			break
		}
		argsMap := map[string]any{}
		if len(parts) > 2 {
			if err := json.Unmarshal([]byte(strings.Join(parts[2:], " ")), &argsMap); err != nil {
				fmt.Fprintln(os.Stderr, "invalid json args:", err)
				break
			}
		}
		client, err := mcp.Connect(srv)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			break
		}
		out, err := client.CallTool(parts[1], argsMap)
		_ = client.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		} else {
			fmt.Println(out)
		}
	case "/goal":
		if arg == "" {
			fmt.Println("usage: /goal <task>")
			break
		}
		old := ag.Config.Mode
		oldApprove := ag.Approve
		ag.Config.Mode = "auto"
		ag.Approve = tools.AutoApprove
		reply, err := ag.Run(context.Background(), arg)
		ag.Config.Mode = old
		ag.Approve = oldApprove
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		} else {
			fmt.Println("[goal, auto mode]\n" + reply)
		}
	case "/plan":
		if arg == "" {
			fmt.Println("usage: /plan <task>")
			break
		}
		old := ag.Config.Mode
		oldApprove := ag.Approve
		ag.Config.Mode = "plan"
		ag.Approve = tools.NeverApprove
		reply, err := ag.Run(context.Background(), arg)
		ag.Config.Mode = old
		ag.Approve = oldApprove
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		} else {
			fmt.Println("[plan — nothing was written]\n" + reply)
		}
	case "/agent":
		if arg == "" {
			fmt.Println("usage: /agent <task>")
			break
		}
		sa := ag.Spawn(arg)
		fmt.Printf("Dispatched %s: %s (check with /agents)\n", sa.ID, arg)
	case "/agents":
		for _, sa := range ag.ListSubagents() {
			fmt.Printf("%s [%s] %s — %s\n", sa.ID, sa.Status, sa.Task, sa.Result)
		}
		for _, rec := range ag.LoadPersistedSubagents() {
			seen := false
			for _, sa := range ag.ListSubagents() {
				if sa.ID == rec.ID {
					seen = true
				}
			}
			if !seen {
				fmt.Printf("%s [%s] %s — %s (from earlier run)\n", rec.ID, rec.Status, rec.Task, rec.Result)
			}
		}
		if len(ag.ListSubagents()) == 0 && len(ag.LoadPersistedSubagents()) == 0 {
			fmt.Println("No subagents yet. Try /agent cari TODO")
		}
	case "/compact":
		if err := ag.Compact(context.Background(), sess); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		} else {
			fmt.Printf("Session compacted to 1 summary message.\n")
			_ = session.Save(sess)
		}
	case "/plugins":
		for _, p := range skills.LoadPlugins(".") {
			fmt.Printf("- %s (%s)\n", p.Name, p.Path)
		}
		if len(skills.LoadPlugins(".")) == 0 {
			fmt.Println("No plugins found. Drop a .md file in .route-x/plugins/ to add one.")
		}
	case "/checkpoint":
		if arg == "" || arg == "list" {
			for _, m := range checkpoint.List() {
				fmt.Printf("- %s (%s, %d files)\n", m.ID, m.Created, len(m.Files))
			}
			if len(checkpoint.List()) == 0 {
				fmt.Println("No checkpoints yet. One is created automatically before the first write/edit of a run.")
			}
		} else if strings.HasPrefix(arg, "restore ") {
			id := strings.TrimSpace(strings.TrimPrefix(arg, "restore "))
			if err := checkpoint.Restore(workdir, id); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
			} else {
				fmt.Printf("Restored checkpoint %s.\n", id)
			}
		} else {
			fmt.Println("usage: /checkpoint [list | restore <id>]")
		}
	case "/search":
		if arg == "" {
			fmt.Println("usage: /search <word>")
			break
		}
		matches, err := session.Search(arg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			break
		}
		if len(matches) == 0 {
			fmt.Printf("No sessions mention %q.\n", arg)
			break
		}
		for _, m := range matches {
			fmt.Printf("- %s: %s\n", m.ID, m.Excerpt)
		}
	case "/usage":
		fmt.Printf("model: %s\nmode: %s\nprovider: %s\nsandbox: %s\napproval: %s\nlocal provider: unlimited (offline)\ngemini provider: needs GEMINI_API_KEY\nopenai provider: needs OPENAI_API_KEY\nanthropic provider: needs ANTHROPIC_API_KEY\nollama provider: local OpenAI-compatible endpoint (default http://localhost:11434/v1)\nintern provider: needs INTERN_API_KEY (Shanghai AI Lab discovery API, default model Atria-Dawn)\n",
			cfg.Model, cfg.Mode, cfg.Provider, cfg.Sandbox, cfg.Approval)
		fmt.Printf("last run: %d in / %d out tokens\n", ag.LastUsage.InputTokens, ag.LastUsage.OutputTokens)
		fmt.Printf("session total: %d in / %d out tokens\n", ag.TotalUsage.InputTokens, ag.TotalUsage.OutputTokens)
		if cost, ok := agent.CostUSD(cfg.Model, ag.TotalUsage); ok {
			fmt.Printf("estimated cost (list prices, may change): $%.4f\n", cost)
		} else {
			fmt.Println("estimated cost: unknown model price")
		}
	case "/skills":
		for _, s := range skills.Load(".") {
			fmt.Printf("- %s (%s)\n", s.Name, s.Path)
		}
		if len(skills.Load(".")) == 0 {
			fmt.Println("No skills found. Run `route-x init` to create an example.")
		}
	case "/lint":
		matches, _ := tools.Search(".", "TODO|FIXME")
		if len(matches) == 0 {
			fmt.Println("lint: no TODO / FIXME found.")
		} else {
			for _, m := range matches {
				fmt.Printf("%s:%d: %s\n", m.File, m.Line, m.Text)
			}
		}
	case "/config":
		path, _ := config.Path()
		fmt.Printf("config: %s\nmodel: %s\nmode: %s\nprovider: %s\nsandbox: %s\napproval: %s\nbase_url: %s\napi_key: %s\n",
			path, cfg.Model, cfg.Mode, cfg.Provider, cfg.Sandbox, cfg.Approval, cfg.BaseURL, cfg.MaskedAPIKey())
	case "/resume":
		if arg == "" {
			ids, _ := session.List()
			if len(ids) == 0 {
				fmt.Println("No saved sessions.")
			}
			for _, id := range ids {
				fmt.Println("-", id)
			}
		} else {
			s, err := session.Load(arg)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
			} else {
				fmt.Printf("Resumed session %s (%d messages):\n", s.ID, len(s.Messages))
				for _, m := range s.Messages {
					fmt.Printf("[%s] %s: %s\n", m.Time, m.Role, m.Content)
				}
			}
		}
	case "/diff":
		if ag.LastDiff == "" {
			fmt.Println("No diff yet — make an edit first.")
		} else {
			fmt.Println(ag.LastDiff)
		}
	case "/clear":
		fmt.Print("\033[H\033[2J")
	default:
		fmt.Println("Unknown slash command:", cmd, "— try /help")
	}
	return false
}
