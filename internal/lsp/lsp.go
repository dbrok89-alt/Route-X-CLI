// Package lsp is a minimal Language Server Protocol client over
// stdio (JSON-RPC 2.0 with Content-Length framing), stdlib only.
// It covers the operations a coding agent actually needs:
// definition, references, hover, and document symbols.
//
// It is intentionally small: no incremental sync, no diagnostics
// handling, no workspace symbol search. If no server binary is
// configured, the tool reports that instead of pretending.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Location is a file position range.
type Location struct {
	URI   string
	Line  int
	Char  int
	EndLn int
	EndCh int
}

func (l Location) String() string {
	return fmt.Sprintf("%s:%d:%d", l.URI, l.Line+1, l.Char+1)
}

// Symbol is one document symbol.
type Symbol struct {
	Name string
	Kind int
	Line int
}

// Client talks to one language server process.
type Client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	reader  *bufio.Reader
	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcResponse
	closed  bool
}

type rpcResponse struct {
	Result json.RawMessage
	Error  *rpcError
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("lsp error %d: %s", e.Code, e.Message) }

// Start spawns command (first word is the binary, rest are args)
// and runs the initialize handshake for rootPath.
func Start(command, rootPath string) (*Client, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty lsp command")
	}
	cmd := exec.Command(fields[0], fields[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot start lsp server %q: %w", fields[0], err)
	}
	c := &Client{cmd: cmd, stdin: stdin, reader: bufio.NewReader(stdout), pending: map[int]chan rpcResponse{}}
	go c.readLoop()
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		abs = rootPath
	}
	params := map[string]any{
		"processId":    nil,
		"rootUri":      "file://" + abs,
		"capabilities": map[string]any{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.Call(ctx, "initialize", params); err != nil {
		c.Close()
		return nil, fmt.Errorf("lsp initialize: %w", err)
	}
	_ = c.Notify("initialized", map[string]any{})
	return c, nil
}

// Close shuts the server down.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = c.Call(ctx, "shutdown", nil)
	_ = c.Notify("exit", nil)
	_ = c.stdin.Close()
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = c.cmd.Process.Kill()
	}
}

func (c *Client) readLoop() {
	for {
		msg, err := readMessage(c.reader)
		if err != nil {
			return
		}
		var envelope struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal(msg, &envelope); err != nil {
			continue
		}
		if envelope.ID != nil && envelope.Method == "" {
			// A response to one of our requests.
			c.mu.Lock()
			ch, ok := c.pending[*envelope.ID]
			if ok {
				delete(c.pending, *envelope.ID)
			}
			c.mu.Unlock()
			if ok {
				ch <- rpcResponse{Result: envelope.Result, Error: envelope.Error}
			}
			continue
		}
		if envelope.ID != nil && envelope.Method != "" {
			// A server request (e.g. client/registerCapability):
			// answer with a null result so the server continues.
			_ = c.send(map[string]any{"jsonrpc": "2.0", "id": *envelope.ID, "result": nil})
		}
		// Notifications are ignored.
	}
}

func readMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			n, err := strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
			if err != nil {
				return nil, fmt.Errorf("bad content-length %q", line)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("missing content-length header")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (c *Client) send(msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("lsp client closed")
	}
	_, err = fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n%s", len(data), data)
	return err
}

// Call sends a request and waits for its response.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.send(msg); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("lsp %s timed out", method)
	}
}

// Notify sends a notification (no response expected).
func (c *Client) Notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.send(msg)
}

func fileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file://" + (&url.URL{Path: filepath.ToSlash(abs)}).EscapedPath()
}

func (c *Client) positionParams(file string, line, char int) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": fileURI(file)},
		"position":     map[string]any{"line": line, "character": char},
	}
}

func parseLocations(raw json.RawMessage) ([]Location, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// Single object or array of objects (Location or LocationLink).
	var arr []json.RawMessage
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, err
		}
	} else {
		arr = []json.RawMessage{raw}
	}
	var out []Location
	for _, item := range arr {
		var loc struct {
			URI   string `json:"uri"`
			Range struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
				End struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"end"`
			} `json:"range"`
			TargetURI   string `json:"targetUri"`
			TargetRange struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
				End struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"end"`
			} `json:"targetRange"`
		}
		if err := json.Unmarshal(item, &loc); err != nil {
			continue
		}
		uri, rng := loc.URI, loc.Range
		if loc.TargetURI != "" {
			uri, rng = loc.TargetURI, loc.TargetRange
		}
		if uri == "" {
			continue
		}
		out = append(out, Location{URI: uri, Line: rng.Start.Line, Char: rng.Start.Character, EndLn: rng.End.Line, EndCh: rng.End.Character})
	}
	return out, nil
}

// Definition returns the definition location(s) of the symbol at
// the given 0-based line/character.
func (c *Client) Definition(ctx context.Context, file string, line, char int) ([]Location, error) {
	raw, err := c.Call(ctx, "textDocument/definition", c.positionParams(file, line, char))
	if err != nil {
		return nil, err
	}
	return parseLocations(raw)
}

// References returns all references of the symbol at the position.
func (c *Client) References(ctx context.Context, file string, line, char int) ([]Location, error) {
	params := c.positionParams(file, line, char)
	params["context"] = map[string]any{"includeDeclaration": true}
	raw, err := c.Call(ctx, "textDocument/references", params)
	if err != nil {
		return nil, err
	}
	return parseLocations(raw)
}

// Hover returns the hover text at the position.
func (c *Client) Hover(ctx context.Context, file string, line, char int) (string, error) {
	raw, err := c.Call(ctx, "textDocument/hover", c.positionParams(file, line, char))
	if err != nil {
		return "", err
	}
	var hover struct {
		Contents any `json:"contents"`
	}
	if err := json.Unmarshal(raw, &hover); err != nil {
		return "", err
	}
	switch v := hover.Contents.(type) {
	case string:
		return v, nil
	case map[string]any:
		if s, ok := v["value"].(string); ok {
			return s, nil
		}
	case []any:
		var parts []string
		for _, item := range v {
			switch it := item.(type) {
			case string:
				parts = append(parts, it)
			case map[string]any:
				if s, ok := it["value"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n"), nil
	}
	return "", nil
}

// DocumentSymbol returns the top-level symbols of a file.
func (c *Client) DocumentSymbol(ctx context.Context, file string) ([]Symbol, error) {
	raw, err := c.Call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": fileURI(file)},
	})
	if err != nil {
		return nil, err
	}
	var items []struct {
		Name  string `json:"name"`
		Kind  int    `json:"kind"`
		Range struct {
			Start struct {
				Line int `json:"line"`
			} `json:"start"`
		} `json:"range"`
		SelectionRange struct {
			Start struct {
				Line int `json:"line"`
			} `json:"start"`
		} `json:"selectionRange"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	var out []Symbol
	for _, it := range items {
		out = append(out, Symbol{Name: it.Name, Kind: it.Kind, Line: it.SelectionRange.Start.Line})
	}
	return out, nil
}
