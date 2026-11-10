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
	"path/filepath"
	"strings"
	"time"

	"route-x/internal/agent"
	"route-x/internal/config"
	"route-x/internal/hooks"
	"route-x/internal/mcp"
	"route-x/internal/memory"
	"route-x/internal/session"
	"route-x/internal/skills"
	"route-x/internal/tools"
)

const version = "0.2.0"

// stdinReader is the single shared stdin reader for the REPL and
// for approval prompts. See runREPL for why it must be shared.
var stdinReader = bufio.NewReader(os.Stdin)

func main() {
	var (
		printMode = flag.Bool("p", false, "print mode: run once, non-interactively, then exit")
		printLong = flag.Bool("print", false, "same as -p")
		oneshot   = flag.Bool("z", false, "one-shot mode, same as -p (Hermes -z)")
		output    = flag.String("output", "text", "print-mode output: text | json | stream-json")
		model     = flag.String("model", "", "model name (default from config)")
		mode      = flag.String("mode", "", "execution mode: default | accept-edits | auto | plan")
		provider  = flag.String("provider", "", "provider: local | gemini | openai | anthropic | ollama")
		sandbox   = flag.String("sandbox", "", "sandbox: read-only | workspace-write | danger-full-access")
		approval  = flag.String("approval", "", "approval policy: on-request | never")
		baseURL   = flag.String("base-url", "", "OpenAI-compatible endpoint (openai/ollama provider)")
		showVer   = flag.Bool("version", false, "show version")
		showVerS  = flag.Bool("v", false, "show version (short)")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Route-X %s — terminal coding agent (inspired by Antigravity CLI / agy)\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage:\n  route-x [prompt]\n  route-x -p \"prompt\" [--output json] [--mode plan]\n  route-x init\n  route-x --version\n\nFlags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nSlash commands (interactive mode):\n  /help /model [name] /mode [name] /provider [name] /goal <task>\n  /plan <task> /agent <task> /agents /usage /skills /config\n  /resume [id] /clear /diff /exit\n  /permissions /sandbox [name] /approval [name] /review /verify\n  /context /memory /remember <fact> /learn <name> /hooks /mcp\n")
	}
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

func runPrint(ag *agent.Agent, prompt, output string) {
	reply, err := ag.Run(context.Background(), prompt)
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
			if handleSlash(ag, &cfg, sess, line) {
				break // /exit
			}
			continue
		}
		sess.Messages = append(sess.Messages, session.Message{Role: "user", Content: line, Time: time.Now().Format(time.RFC3339)})
		prevDiff := ag.LastDiff
		fire("PreToolUse")
		reply, err := ag.Run(context.Background(), line)
		fire("PostToolUse")
		if ag.LastDiff != prevDiff {
			fire("PostEdit")
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		fmt.Println(reply)
		sess.Messages = append(sess.Messages, session.Message{Role: "agent", Content: reply, Time: time.Now().Format(time.RFC3339)})
		_ = session.Save(sess)
	}
}

// handleSlash runs one slash command. It returns true when the REPL
// should exit.
func handleSlash(ag *agent.Agent, cfg *config.Config, sess *session.Session, line string) bool {
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
			"  /provider [name]   show / set provider (local, gemini, openai, anthropic, ollama)\n" +
			"  /sandbox [name]    show / set sandbox (read-only, workspace-write, danger-full-access)\n" +
			"  /approval [name]   show / set approval (on-request, never)\n" +
			"  /permissions       show sandbox, approval, allow/deny lists\n" +
			"  /goal <task>       run a task autonomously (auto-approves edits)\n" +
			"  /plan <task>       plan a task without writing anything\n" +
			"  /agent <task>      dispatch a background subagent\n" +
			"  /agents            list subagents\n" +
			"  /review            review git diff + TODO/FIXME\n" +
			"  /verify            run project checks and report evidence\n" +
			"  /context           show session context usage\n" +
			"  /memory            show persistent memory\n" +
			"  /remember <fact>   save a fact to persistent memory\n" +
			"  /learn <name> [desc] save a repeated task as a skill\n" +
			"  /hooks             list lifecycle hooks\n" +
			"  /mcp               list MCP servers and their tools\n" +
			"  /mcp-call <srv> <tool> [json] call an MCP tool\n" +
			"  /usage             show model / mode / provider\n" +
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
			cfg.Provider = arg
			ag.Config.Provider = arg
			_ = config.Save(*cfg)
			fmt.Println("provider set to", arg)
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
		if len(ag.ListSubagents()) == 0 {
			fmt.Println("No subagents yet. Try /agent cari TODO")
		}
	case "/usage":
		fmt.Printf("model: %s\nmode: %s\nprovider: %s\nsandbox: %s\napproval: %s\nlocal provider: unlimited (offline)\ngemini provider: needs GEMINI_API_KEY\nopenai provider: needs OPENAI_API_KEY\nanthropic provider: needs ANTHROPIC_API_KEY\nollama provider: local OpenAI-compatible endpoint (default http://localhost:11434/v1)\n",
			cfg.Model, cfg.Mode, cfg.Provider, cfg.Sandbox, cfg.Approval)
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
