# Contributing Guidelines

Thank you for contributing to **Route-X**! Please review the guidelines below before submitting pull requests or code changes.

## Core Architecture Principles

### Pure Go Standard Library / Zero External Dependencies
- Route-X strictly requires **Pure Go Standard Library (Go 1.23)**.
- **Zero External Dependencies:** No third-party modules or external `go.mod` dependencies are allowed under any circumstances.

## Codebase Structure (`internal/` Packages)

The project consists of 15 modular internal packages inside `internal/`:
- `agent` – Core AI agent loop and orchestration logic; providers local/gemini/openai/anthropic/ollama/intern, tool dispatch, subagents, /review, /verify.
- `checkpoint` – Workspace snapshots before risky edits, restore/undo.
- `config` – Configuration loading and user preferences (`~/.route-x/config.json`).
- `cron` – Recurring agent tasks and the `cron watch` watcher.
- `hooks` – Lifecycle hook execution (SessionStart..Stop).
- `loop` – Agentic loop: model-driven tool calling until done or step cap.
- `lsp` – Language Server Protocol client (definition, references, hover, symbols).
- `mcp` – Model Context Protocol client (tools/list, tools/call).
- `memory` – Persistent memory and learned skills (`/remember`, `/learn`).
- `serve` – Local HTTP agent endpoint and tunnel sharing (`serve`/`share`).
- `session` – Session save / resume / search (JSON).
- `skills` – Loading `.route-x/skills/*.md` and plugins.
- `tools` – Built-in tools: read, search, codesearch, list, write+diff, edit, run, glob, apply_patch, skill, todowrite, question, task, plan, webfetch, websearch, lsp, and approval gates.
- `tui` – Terminal UI components (banner, status bar, progress bar, box, markdown, spinner).
- `web` – Web tools: webfetch (SSRF-guarded) and websearch.

## Development Commands

Use the provided `Makefile` targets during development:
- `make build` – Compiles the `route-x` binary.
- `make test` – Runs `go vet` and tests with race detection (`go test -race ./...`).
- `make lint` – Verifies formatting via `gofmt` (`gofmt -l .`) and runs `go vet ./...`.

## Testing Standards

- All unit tests must be written in `*_test.go` files alongside the implementation.
- Every package in `internal/` and `main.go` must have accompanying `*_test.go` coverage.
- Run `make test` locally to ensure all tests pass cleanly before submitting changes.
