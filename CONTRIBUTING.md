# Contributing Guidelines

Thank you for contributing to **Route-X**! Please review the guidelines below before submitting pull requests or code changes.

## Core Architecture Principles

### Pure Go Standard Library / Zero External Dependencies
- Route-X strictly requires **Pure Go Standard Library (Go 1.23)**.
- **Zero External Dependencies:** No third-party modules or external `go.mod` dependencies are allowed under any circumstances.

## Codebase Structure (`internal/` Packages)

The project consists of 14 modular internal packages inside `internal/`:
- `agent` – Core AI agent loop and orchestration logic.
- `agentic` – Planning, tool invocation, and decision-making engine.
- `config` – Configuration loading and user preferences.
- `context` – Session context, tokens, and history management.
- `hooks` – Lifecycle hook execution and process isolation.
- `llm` – LLM API provider integrations and streaming interface.
- `lsp` – Language Server Protocol client and subprocess protocol.
- `mcp` – Model Context Protocol server/client protocol handler.
- `patch` – Code diff parsing and file application engine.
- `prompt` – System prompts, templates, and formatting.
- `security` – Command allow-lists and permission confirmation.
- `terminal` – CLI interactive terminal formatting and output.
- `tools` – Built-in system execution tools (`bash`, `apply_patch`).
- `util` – Core utilities and standard helper functions.

## Development Commands

Use the provided `Makefile` targets during development:
- `make build` – Compiles the `route-x` binary.
- `make test` – Runs `go vet` and tests with race detection (`go test -race ./...`).
- `make lint` – Verifies formatting via `gofmt` (`gofmt -l .`) and runs `go vet ./...`.

## Testing Standards

- All unit tests must be written in `*_test.go` files alongside the implementation.
- Every package in `internal/` and `main.go` must have accompanying `*_test.go` coverage.
- Run `make test` locally to ensure all tests pass cleanly before submitting changes.
