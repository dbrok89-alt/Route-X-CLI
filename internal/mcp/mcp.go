// Package mcp implements a minimal Model Context Protocol client
// (Claude Code / Codex / Hermes style): stdio JSON-RPC servers
// configured in .route-x/mcp.json, plus a simple HTTP transport.
// Only the operations Route-X needs are implemented: initialize,
// tools/list and tools/call. No MCP server is bundled.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ServerConfig is one MCP server entry in mcp.json.
type ServerConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"` // HTTP transport, when set
}

// Config is the .route-x/mcp.json file.
type Config struct {
	Servers map[string]ServerConfig `json:"servers"`
}

// Load reads .route-x/mcp.json from the workdir, then ~/.route-x.
func Load(workdir string) Config {
	var out Config
	out.Servers = map[string]ServerConfig{}
	for _, path := range []string{
		filepath.Join(workdir, ".route-x", "mcp.json"),
	} {
		data, err := os.ReadFile(path)
		if err == nil {
			_ = json.Unmarshal(data, &out)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		data, err := os.ReadFile(filepath.Join(home, ".route-x", "mcp.json"))
		if err == nil {
			var global Config
			if json.Unmarshal(data, &global) == nil {
				for k, v := range global.Servers {
					if _, ok := out.Servers[k]; !ok {
						out.Servers[k] = v
					}
				}
			}
		}
	}
	if out.Servers == nil {
		out.Servers = map[string]ServerConfig{}
	}
	return out
}

// Tool is one tool exposed by an MCP server.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Client is a connected MCP server (stdio only for now).
type Client struct {
	cfg    ServerConfig
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	mu     sync.Mutex
	nextID int
}

// Connect starts a stdio MCP server and runs initialize.
func Connect(cfg ServerConfig) (*Client, error) {
	if cfg.URL != "" {
		return nil, fmt.Errorf("HTTP MCP transport is configured, use CallHTTP instead of Connect")
	}
	if cfg.Command == "" {
		return nil, fmt.Errorf("mcp server has no command")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, cmd: cmd, stdin: stdin, reader: bufio.NewReader(stdout)}
	if _, err := c.request("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "route-x", "version": "0.2.0"},
	}); err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = c.notify("notifications/initialized", nil)
	return c, nil
}

func (c *Client) request(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	data, _ := json.Marshal(msg)
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	// Read one line, with a timeout handled by the caller process.
	// stdio MCP servers reply with one JSON object per line.
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp error: %s", resp.Error.Message)
	}
	return resp.Result, nil
}

func (c *Client) notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	data, _ := json.Marshal(msg)
	_, err := c.stdin.Write(append(data, '\n'))
	return err
}

// ListTools returns the tools exposed by the server.
func (c *Client) ListTools() ([]Tool, error) {
	raw, err := c.request("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return parsed.Tools, nil
}

// CallTool calls one tool with JSON arguments.
func (c *Client) CallTool(name string, args map[string]any) (string, error) {
	raw, err := c.request("tools/call", map[string]any{
		"name": name, "arguments": args,
	})
	if err != nil {
		return "", err
	}
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return string(raw), nil
	}
	var out bytes.Buffer
	for _, item := range parsed.Content {
		out.WriteString(item.Text)
		out.WriteString("\n")
	}
	return out.String(), nil
}

// Close stops the MCP server process.
func (c *Client) Close() error {
	_ = c.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		_ = c.cmd.Process.Kill()
		return fmt.Errorf("mcp server did not exit in time")
	}
}

// CallHTTP is a minimal HTTP MCP call, untested against a live
// server. It posts a JSON-RPC request and returns the raw result.
func CallHTTP(url, method string, params any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return parsed.Result, nil
}
