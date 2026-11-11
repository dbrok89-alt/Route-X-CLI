# Security Policy & Architecture

## Security Architecture

Route-X is designed with security and safety at its core.

### 1. Zero External Dependencies (Go 1.23)
Route-X relies exclusively on the **Go 1.23 Pure Standard Library** without any third-party external dependencies. This eliminates supply-chain vulnerabilities, third-party package tampering, and unverified transitive code execution.

### 2. Command Allow-List & User Confirmation
Execution of sensitive tools (including `bash` shell commands and `apply_patch` file modifications) is strictly governed by:
- **Command Allow-List:** Verification against allowed command rules.
- **Explicit User Confirmation:** Mandatory interactive prompt requiring user permission before executing command lines or applying patches.

### 3. Subprocess Isolation (MCP, LSP, & Hooks)
Integrations for Model Context Protocol (MCP), Language Server Protocol (LSP), and lifecycle hooks operate in isolated subprocess environments:
- Process boundaries prevent arbitrary memory access across tasks.
- Controlled I/O streaming ensures safe tool and server communication.

## Reporting a Vulnerability

If you discover a security vulnerability in Route-X, please do **NOT** open a public issue.

Please report vulnerabilities privately via:
- GitHub Private Vulnerability Reporting on the `dbrok89-alt/Route-X-CLI` repository.

We will review reports promptly and publish fixes in a security advisory.
