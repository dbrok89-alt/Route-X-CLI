// Package agent is the core of Route-X: a provider interface, a
// deterministic local provider (offline, no API key), an optional
// Gemini provider, tool dispatch, and background subagents.
// It mirrors agy's model: one main agent that can spawn subagents
// in parallel and review every edit as a diff before applying it.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"route-x/internal/checkpoint"
	"route-x/internal/config"
	"route-x/internal/loop"
	"route-x/internal/lsp"
	"route-x/internal/memory"
	"route-x/internal/session"
	"route-x/internal/skills"
	"route-x/internal/tools"
	"route-x/internal/web"
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
	APIKey  string
	Model   string
	BaseURL string // default https://generativelanguage.googleapis.com
}

func (g GeminiProvider) Name() string { return "gemini:" + g.Model }

// endpoint returns the generateContent URL for this model.
func (g GeminiProvider) endpoint() string {
	base := strings.TrimSuffix(g.BaseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	return fmt.Sprintf("%s/v1beta/models/%s:generateContent", base, g.Model)
}

func (g GeminiProvider) Complete(ctx context.Context, prompt, workspaceContext string) (string, error) {
	if g.APIKey == "" {
		return "", fmt.Errorf("gemini provider needs GEMINI_API_KEY (or config.api_key)")
	}
	// The API key travels in a header, never in the URL, so it
	// cannot leak into logs, shell history or error messages.
	url := g.endpoint()
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

// geminiContent converts a loop message to a Gemini content block.
// Gemini roles are "user" and "model"; tool results are
// functionResponse parts in a user turn.
func geminiContent(m loop.Message) map[string]any {
	role := "user"
	if m.Role == "assistant" {
		role = "model"
	}
	parts := []map[string]any{}
	if m.Role == "tool" {
		parts = append(parts, map[string]any{
			"functionResponse": map[string]any{
				"name":     m.ToolName,
				"response": map[string]any{"result": m.Content},
			},
		})
		return map[string]any{"role": role, "parts": parts}
	}
	if m.Content != "" {
		parts = append(parts, map[string]any{"text": m.Content})
	}
	for _, tc := range m.ToolCalls {
		parts = append(parts, map[string]any{
			"functionCall": map[string]any{
				"name": tc.Name,
				"args": tc.Arguments,
			},
		})
	}
	return map[string]any{"role": role, "parts": parts}
}

// CompleteTools sends the conversation plus function declarations
// to Gemini and returns the model's next message, which may carry
// function calls.
func (g GeminiProvider) CompleteTools(ctx context.Context, messages []loop.Message, tools []loop.ToolDef) (loop.Message, loop.Usage, error) {
	if g.APIKey == "" {
		return loop.Message{}, loop.Usage{}, fmt.Errorf("gemini provider needs GEMINI_API_KEY (or config.api_key)")
	}
	contents := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		contents = append(contents, geminiContent(m))
	}
	decls := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
		})
	}
	url := g.endpoint()
	body, _ := json.Marshal(map[string]any{
		"contents": contents,
		"tools":    []map[string]any{{"functionDeclarations": decls}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	defer resp.Body.Close()
	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	if parsed.Error != nil {
		return loop.Message{}, loop.Usage{}, fmt.Errorf("gemini API error: %s", parsed.Error.Message)
	}
	if len(parsed.Candidates) == 0 {
		return loop.Message{}, loop.Usage{}, fmt.Errorf("gemini returned no candidates")
	}
	out := loop.Message{Role: "assistant"}
	for i, part := range parsed.Candidates[0].Content.Parts {
		if part.Text != "" {
			out.Content += part.Text
		}
		if part.FunctionCall != nil {
			out.ToolCalls = append(out.ToolCalls, loop.ToolCall{
				ID:        fmt.Sprintf("gemini-call-%d", i),
				Name:      part.FunctionCall.Name,
				Arguments: part.FunctionCall.Args,
			})
		}
	}
	return out, loop.Usage{
		InputTokens:  parsed.UsageMetadata.PromptTokenCount,
		OutputTokens: parsed.UsageMetadata.CandidatesTokenCount,
	}, nil
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

// openAIMessage converts a loop message to the OpenAI wire shape.
func openAIMessage(m loop.Message) map[string]any {
	out := map[string]any{"role": m.Role}
	if m.Content != "" {
		out["content"] = m.Content
	}
	if m.ToolCallID != "" {
		out["tool_call_id"] = m.ToolCallID
		// OpenAI tool messages carry the result in content.
		if _, ok := out["content"]; !ok {
			out["content"] = ""
		}
	}
	if len(m.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(m.ToolCalls))
		for _, c := range m.ToolCalls {
			args, _ := json.Marshal(c.Arguments)
			calls = append(calls, map[string]any{
				"id":   c.ID,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": string(args),
				},
			})
		}
		out["tool_calls"] = calls
	}
	return out
}

// CompleteTools sends the conversation plus tool definitions and
// returns the model's next message, which may contain tool calls.
func (p OpenAICompatibleProvider) CompleteTools(ctx context.Context, messages []loop.Message, tools []loop.ToolDef) (loop.Message, loop.Usage, error) {
	base := strings.TrimSuffix(p.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	msgs := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, openAIMessage(m))
	}
	toolDefs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		toolDefs = append(toolDefs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	body, _ := json.Marshal(map[string]any{
		"model":    p.Model,
		"messages": msgs,
		"tools":    toolDefs,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	defer resp.Body.Close()
	var parsed struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	if parsed.Error != nil {
		return loop.Message{}, loop.Usage{}, fmt.Errorf("openai-compatible API error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return loop.Message{}, loop.Usage{}, fmt.Errorf("openai-compatible API returned no choices")
	}
	msg := parsed.Choices[0].Message
	out := loop.Message{Role: "assistant", Content: msg.Content}
	for _, tc := range msg.ToolCalls {
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return loop.Message{}, loop.Usage{}, fmt.Errorf("bad tool arguments from model for %s: %w", tc.Function.Name, err)
		}
		out.ToolCalls = append(out.ToolCalls, loop.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: args})
	}
	return out, loop.Usage{InputTokens: parsed.Usage.PromptTokens, OutputTokens: parsed.Usage.CompletionTokens}, nil
}

// readSSE reads a server-sent-events body, calling fn for each
// (event, data) pair. The "[DONE]" sentinel ends the stream.
func readSSE(r io.Reader, fn func(event string, data []byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var event string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			event = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(line[len("event:"):])
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(line[len("data:"):])
			if data == "[DONE]" {
				return nil
			}
			if err := fn(event, []byte(data)); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

// StreamTools is the streaming variant of CompleteTools for
// OpenAI-compatible endpoints: text deltas flow to onToken as they
// arrive, and tool calls are accumulated by index and returned
// whole at the end of the step.
func (p OpenAICompatibleProvider) StreamTools(ctx context.Context, messages []loop.Message, tools []loop.ToolDef, onToken func(string)) (loop.Message, loop.Usage, error) {
	base := strings.TrimSuffix(p.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	msgs := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, openAIMessage(m))
	}
	toolDefs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		toolDefs = append(toolDefs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	body, _ := json.Marshal(map[string]any{
		"model":    p.Model,
		"messages": msgs,
		"tools":    toolDefs,
		"stream":   true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	defer resp.Body.Close()
	out := loop.Message{Role: "assistant"}
	var usage loop.Usage
	type partialCall struct {
		id   string
		name string
		args strings.Builder
	}
	calls := map[int]*partialCall{}
	err = readSSE(resp.Body, func(_ string, data []byte) error {
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				out.Content += c.Delta.Content
				if onToken != nil {
					onToken(c.Delta.Content)
				}
			}
			for _, tc := range c.Delta.ToolCalls {
				partial, ok := calls[tc.Index]
				if !ok {
					partial = &partialCall{}
					calls[tc.Index] = partial
				}
				if tc.ID != "" {
					partial.id = tc.ID
				}
				if tc.Function.Name != "" {
					partial.name = tc.Function.Name
				}
				partial.args.WriteString(tc.Function.Arguments)
			}
		}
		return nil
	})
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	for i := 0; i < len(calls); i++ {
		partial := calls[i]
		if partial == nil {
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(partial.args.String()), &args); err != nil {
			return loop.Message{}, loop.Usage{}, fmt.Errorf("bad streamed tool arguments for %s: %w", partial.name, err)
		}
		out.ToolCalls = append(out.ToolCalls, loop.ToolCall{ID: partial.id, Name: partial.name, Arguments: args})
	}
	return out, usage, nil
}

// AnthropicProvider calls the Claude Messages API. Untested live
// without an ANTHROPIC_API_KEY; the request shape follows the
// public Anthropic API docs. BaseURL defaults to the real API
// endpoint and exists so the request can be tested against a
// local server instead of the live API.
type AnthropicProvider struct {
	APIKey  string
	Model   string
	BaseURL string // default https://api.anthropic.com
}

func (p AnthropicProvider) Name() string { return "anthropic:" + p.Model }

func (p AnthropicProvider) endpoint() string {
	base := strings.TrimSuffix(p.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return base + "/v1/messages"
}

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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
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

// anthropicMessage converts a loop message to the Anthropic wire
// shape. Tool results become tool_result content blocks; assistant
// tool calls become tool_use blocks.
func anthropicMessage(m loop.Message) map[string]any {
	role := m.Role
	if role == "tool" {
		role = "user"
	}
	content := []map[string]any{}
	if m.Role == "tool" {
		content = append(content, map[string]any{
			"type":        "tool_result",
			"tool_use_id": m.ToolCallID,
			"content":     m.Content,
		})
		return map[string]any{"role": role, "content": content}
	}
	if m.Content != "" {
		content = append(content, map[string]any{"type": "text", "text": m.Content})
	}
	for _, tc := range m.ToolCalls {
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Name,
			"input": tc.Arguments,
		})
	}
	return map[string]any{"role": role, "content": content}
}

// CompleteTools sends the conversation plus tool definitions to the
// Anthropic Messages API and returns the model's next message.
func (p AnthropicProvider) CompleteTools(ctx context.Context, messages []loop.Message, tools []loop.ToolDef) (loop.Message, loop.Usage, error) {
	if p.APIKey == "" {
		return loop.Message{}, loop.Usage{}, fmt.Errorf("anthropic provider needs ANTHROPIC_API_KEY (or config.api_key)")
	}
	msgs := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, anthropicMessage(m))
	}
	toolDefs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		toolDefs = append(toolDefs, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Parameters,
		})
	}
	body, _ := json.Marshal(map[string]any{
		"model":      p.Model,
		"max_tokens": 4096,
		"tools":      toolDefs,
		"messages":   msgs,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	defer resp.Body.Close()
	var parsed struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	if parsed.Error != nil {
		return loop.Message{}, loop.Usage{}, fmt.Errorf("anthropic API error: %s", parsed.Error.Message)
	}
	out := loop.Message{Role: "assistant"}
	for _, c := range parsed.Content {
		switch c.Type {
		case "text":
			out.Content += c.Text
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, loop.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Input})
		}
	}
	return out, loop.Usage{InputTokens: parsed.Usage.InputTokens, OutputTokens: parsed.Usage.OutputTokens}, nil
}

// StreamTools is the streaming variant of CompleteTools for the
// Anthropic Messages API: text deltas flow to onToken as they
// arrive, tool_use input JSON is accumulated from
// input_json_delta events and parsed at content_block_stop.
func (p AnthropicProvider) StreamTools(ctx context.Context, messages []loop.Message, tools []loop.ToolDef, onToken func(string)) (loop.Message, loop.Usage, error) {
	if p.APIKey == "" {
		return loop.Message{}, loop.Usage{}, fmt.Errorf("anthropic provider needs ANTHROPIC_API_KEY (or config.api_key)")
	}
	msgs := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, anthropicMessage(m))
	}
	toolDefs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		toolDefs = append(toolDefs, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Parameters,
		})
	}
	body, _ := json.Marshal(map[string]any{
		"model":      p.Model,
		"max_tokens": 4096,
		"tools":      toolDefs,
		"messages":   msgs,
		"stream":     true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	defer resp.Body.Close()
	out := loop.Message{Role: "assistant"}
	var usage loop.Usage
	var cur *loop.ToolCall
	var curArgs strings.Builder
	err = readSSE(resp.Body, func(event string, data []byte) error {
		var ev struct {
			Type string `json:"type"`
			// content_block_start
			ContentBlock struct {
				Type  string         `json:"type"`
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content_block"`
			// content_block_delta
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			// message_delta
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return err
		}
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				cur = &loop.ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
				curArgs.Reset()
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				out.Content += ev.Delta.Text
				if onToken != nil {
					onToken(ev.Delta.Text)
				}
			case "input_json_delta":
				curArgs.WriteString(ev.Delta.PartialJSON)
			}
		case "content_block_stop":
			if cur != nil {
				var args map[string]any
				if s := curArgs.String(); s != "" {
					if err := json.Unmarshal([]byte(s), &args); err != nil {
						return fmt.Errorf("bad streamed tool arguments for %s: %w", cur.Name, err)
					}
				}
				cur.Arguments = args
				out.ToolCalls = append(out.ToolCalls, *cur)
				cur = nil
			}
		case "message_delta":
			if ev.Usage != nil {
				usage.InputTokens = ev.Usage.InputTokens
				usage.OutputTokens = ev.Usage.OutputTokens
			}
		}
		_ = event
		return nil
	})
	if err != nil {
		return loop.Message{}, loop.Usage{}, err
	}
	return out, usage, nil
}

// SubAgent is one background agent, like agy's /agent tasks.
type SubAgent struct {
	ID       string
	Task     string
	Status   string // running | done | failed
	Result   string
	Started  string
	Finished string
	done     chan struct{}
}

// SubagentRecord is the on-disk form of a finished subagent, so
// /agents still shows work from earlier runs after a restart.
type SubagentRecord struct {
	ID       string `json:"id"`
	Task     string `json:"task"`
	Status   string `json:"status"`
	Result   string `json:"result"`
	Started  string `json:"started"`
	Finished string `json:"finished"`
}

func subagentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".route-x", "subagents")
}

// persistSubagent writes one finished subagent to disk.
func (a *Agent) persistSubagent(sa *SubAgent) {
	_ = a
	dir := subagentsDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, _ := json.Marshal(SubagentRecord{
		ID: sa.ID, Task: sa.Task, Status: sa.Status, Result: sa.Result,
		Started: sa.Started, Finished: sa.Finished,
	})
	_ = os.WriteFile(filepath.Join(dir, sa.ID+".json"), data, 0o644)
}

// LoadPersistedSubagents reads finished subagents from earlier
// runs, newest first. Missing directory means none yet.
func (a *Agent) LoadPersistedSubagents() []SubagentRecord {
	_ = a
	dir := subagentsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []SubagentRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rec SubagentRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	// Newest first by finished time (ISO timestamps sort).
	sort.Slice(out, func(i, j int) bool { return out[i].Finished > out[j].Finished })
	return out
}

// Agent holds the runtime state for one Route-X run.
// TodoItem is one entry of the session todo list. Status is
// pending, in_progress, or completed.
type TodoItem struct {
	Text   string
	Status string
}

// renderTodos renders the session todo list for the model/user.
func renderTodos(todos []TodoItem) string {
	if len(todos) == 0 {
		return "No todos."
	}
	var b strings.Builder
	for i, t := range todos {
		mark := "[ ]"
		switch t.Status {
		case "in_progress":
			mark = "[~]"
		case "completed":
			mark = "[x]"
		}
		fmt.Fprintf(&b, "%d. %s %s\n", i+1, mark, t.Text)
	}
	return b.String()
}

type Agent struct {
	Config   config.Config
	Workdir  string
	Approve  tools.ApproveFunc
	LastDiff string
	// LastUsage is the token accounting of the most recent
	// agentic run (RunAgent). Zero when the run did not use a
	// tool-calling provider.
	LastUsage loop.Usage
	// TotalUsage accumulates every agentic run in this process,
	// for /usage and cost estimates.
	TotalUsage loop.Usage
	// ToolProviderOverride, when set, replaces provider() for the
	// agentic loop. Production leaves it nil; tests inject a
	// scripted provider here so RunAgent can be tested without
	// a live API key.
	ToolProviderOverride loop.ToolProvider
	// ProviderOverride, when set, replaces provider() for plain
	// completions (Compact). Tests inject a scripted provider.
	ProviderOverride Provider
	// LastCheckpointID is the id of the checkpoint created before
	// the first write/edit of this agent's lifetime, or "" when
	// no checkpoint was created (or needed).
	LastCheckpointID string
	checkpointDone   bool

	// Todos is the session todo list managed by the todowrite
	// tool (OpenCode-style).
	Todos []TodoItem
	// CurrentPlan holds the steps recorded by the plan tool.
	CurrentPlan []string
	// TaskDepth counts nested task-tool subagents: 0 is the top
	// agent, a subagent spawned by the task tool gets 1 and may
	// not spawn further tasks.
	TaskDepth int
	// QuestionFunc answers a question-tool call. Nil in print
	// mode (questions are refused there); the REPL sets an
	// interactive prompt that reads from stdin.
	QuestionFunc func(question string, options []string) (string, error)
	// WebSearcher is the searcher used by the websearch tool.
	// When nil, one is built lazily from EXA_API_KEY.
	WebSearcher *web.Searcher
	// LSPCommand overrides the language server command for the
	// lsp tool (tests set it to the fake server). Otherwise the
	// ROUTE_X_LSP_COMMAND env var, then config.lsp_server.
	LSPCommand string
	lspClient  *lsp.Client
	lspMu      sync.Mutex

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
	if a.ProviderOverride != nil {
		return a.ProviderOverride
	}
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
		// BaseURL stays empty here: config.BaseURL is the
		// OpenAI-compatible endpoint, not an Anthropic one.
		// endpoint() then defaults to https://api.anthropic.com.
		return AnthropicProvider{APIKey: key, Model: a.Config.Model}
	case "intern":
		// Shanghai AI Lab's Intern discovery API, OpenAI-compatible
		// (verified live: /v1/models answers 401 invalid_api_key
		// without a key, i.e. the OpenAI error envelope). Default
		// model is Atria-Dawn (agentic, 512K context, text-only).
		// A config that still holds a placeholder model is
		// remapped — none of "", "local" or "gemini-2.0-flash"
		// is an Intern model name, so this can never clobber a
		// real Intern model the user chose. A custom BaseURL
		// overrides the default endpoint.
		base := a.Config.BaseURL
		if base == "" {
			base = config.InternBaseURL
		}
		model := a.Config.Model
		if config.IsGenericModel(model) {
			model = config.DefaultModelFor("intern")
		}
		return OpenAICompatibleProvider{APIKey: key, Model: model, BaseURL: base}
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
	if plugs := skills.LoadPlugins(a.Workdir); len(plugs) > 0 {
		ctx += "\nPlugins (extra commands, follow their instructions when asked):\n"
		for _, p := range plugs {
			first := p.Content
			if i := strings.Index(first, "\n"); i >= 0 {
				first = first[:i]
			}
			ctx += "- " + p.Name + ": " + strings.TrimSpace(first) + "\n"
		}
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
// The match is exact on the trimmed command, not a substring:
// a substring match would auto-approve a chained command such as
// "go vet ./...; rm -rf /" just because it contains an allowed
// entry. The deny list and the shell blocklist still apply.
func (a *Agent) IsAllowed(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	for _, pat := range a.Config.Allow {
		if pat != "" && cmd == strings.TrimSpace(pat) {
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

// ToolDefs is the tool registry the model may call in the
// agentic loop: read, search, list, write, edit, run.
func ToolDefs() []loop.ToolDef {
	str := map[string]any{"type": "string"}
	return []loop.ToolDef{
		{Name: "read", Description: "Read a file and return its contents.",
			Parameters: loop.Schema(map[string]any{"path": str}, "path")},
		{Name: "search", Description: "Search for a text pattern (regex) in the workspace files.",
			Parameters: loop.Schema(map[string]any{"pattern": str}, "pattern")},
		{Name: "list", Description: "List files in the workspace (first 50).",
			Parameters: loop.Schema(map[string]any{})},
		{Name: "write", Description: "Create or overwrite a file with the given content. Shows a diff and follows the approval mode.",
			Parameters: loop.Schema(map[string]any{"path": str, "content": str}, "path", "content")},
		{Name: "edit", Description: "Replace one exact piece of text (old_text) in a file with new_text. Prefer this over rewriting a whole file.",
			Parameters: loop.Schema(map[string]any{"path": str, "old_text": str, "new_text": str}, "path", "old_text", "new_text")},
		{Name: "run", Description: "Run a shell command in the workspace. Dangerous commands are blocked by the sandbox and deny list.",
			Parameters: loop.Schema(map[string]any{"command": str}, "command")},
		{Name: "glob", Description: "Find files by pattern, e.g. **/*.go or src/*.ts. A ** segment matches any number of directories.",
			Parameters: loop.Schema(map[string]any{"pattern": str}, "pattern")},
		{Name: "apply_patch", Description: "Apply a multi-file patch. Format: lines starting with '*** Add File: <path>', '*** Update File: <path>', '*** Delete File: <path>' or '*** Rename File: <old> -> <new>', followed by the file's full new content for Add/Update. Add refuses an existing file; Update/Delete/Rename need the file to exist.",
			Parameters: loop.Schema(map[string]any{"patch": str}, "patch")},
		{Name: "skill", Description: "Load a skill (a Markdown file in .route-x/skills/) by name and return its content.",
			Parameters: loop.Schema(map[string]any{"name": str}, "name")},
		{Name: "todowrite", Description: "Manage the session todo list. action: add (needs text), start (needs index), done (needs index), list, clear. Index is 1-based.",
			Parameters: loop.Schema(map[string]any{"action": str, "text": str, "index": map[string]any{"type": "integer"}}, "action")},
		{Name: "question", Description: "Ask the user a question in the middle of a task and wait for the answer. Only works in interactive mode; refused in print mode. options is an optional list of choices.",
			Parameters: loop.Schema(map[string]any{"question": str, "options": map[string]any{"type": "array", "items": str}}, "question")},
		{Name: "task", Description: "Delegate a self-contained subtask to a fresh subagent and wait for its final answer. Subagents cannot spawn further tasks.",
			Parameters: loop.Schema(map[string]any{"prompt": str}, "prompt")},
		{Name: "plan", Description: "Record a plan (list of steps) before executing it. The steps are returned and stay visible to you for the rest of the session.",
			Parameters: loop.Schema(map[string]any{"steps": map[string]any{"type": "array", "items": str}}, "steps")},
		{Name: "webfetch", Description: "Fetch a web page and return its text (HTML is stripped). Private/loopback hosts are refused.",
			Parameters: loop.Schema(map[string]any{"url": str}, "url")},
		{Name: "websearch", Description: "Search the web and return up to 5 results with title, URL and snippet. Uses Exa when EXA_API_KEY is set, otherwise DuckDuckGo (no key needed).",
			Parameters: loop.Schema(map[string]any{"query": str}, "query")},
		{Name: "lsp", Description: "Ask the configured language server: definition, references, hover, or symbols for a file position. Needs an LSP server (set lsp_server in config, e.g. gopls). line and character are 0-based.",
			Parameters: loop.Schema(map[string]any{"operation": str, "file": str, "line": map[string]any{"type": "integer"}, "character": map[string]any{"type": "integer"}}, "operation", "file")},
	}
}

// toolProvider returns the active tool-calling provider, or nil
// when the active provider cannot call tools (local provider).
func (a *Agent) toolProvider() loop.ToolProvider {
	if a.ToolProviderOverride != nil {
		return a.ToolProviderOverride
	}
	switch p := a.provider().(type) {
	case GeminiProvider:
		return p
	case OpenAICompatibleProvider:
		return p
	case AnthropicProvider:
		return p
	}
	return nil
}

// ensureCheckpoint snapshots the workspace once per agent
// lifetime, before the first write or edit, so /checkpoint can
// undo the run. A failed snapshot never blocks the task.
func (a *Agent) ensureCheckpoint() {
	if a.checkpointDone {
		return
	}
	a.checkpointDone = true
	if id, err := checkpoint.Create(a.Workdir); err == nil {
		a.LastCheckpointID = id
	}
}

// ExecuteTool runs one model-requested tool call under the same
// sandbox and approval gates as the keyword dispatch. It never
// bypasses CanWrite, CanRun, or the approval gate.
func (a *Agent) ExecuteTool(ctx context.Context, call loop.ToolCall) (string, error) {
	_ = ctx
	switch call.Name {
	case "read":
		path, ok := loop.StrArg(call.Arguments, "path")
		if !ok {
			return "", fmt.Errorf("read needs a path argument")
		}
		content, err := tools.ReadFile(path)
		if err != nil {
			return "", err
		}
		return content, nil
	case "search":
		pattern, ok := loop.StrArg(call.Arguments, "pattern")
		if !ok {
			return "", fmt.Errorf("search needs a pattern argument")
		}
		matches, err := tools.Search(a.Workdir, pattern)
		if err != nil {
			return "", err
		}
		if len(matches) == 0 {
			return fmt.Sprintf("No matches for %q.", pattern), nil
		}
		var b strings.Builder
		for _, m := range matches {
			fmt.Fprintf(&b, "%s:%d: %s\n", m.File, m.Line, m.Text)
		}
		return b.String(), nil
	case "list":
		files := tools.ListFiles(a.Workdir, 50)
		return strings.Join(files, "\n"), nil
	case "write":
		path, ok := loop.StrArg(call.Arguments, "path")
		if !ok {
			return "", fmt.Errorf("write needs a path argument")
		}
		content, ok := loop.StrArg(call.Arguments, "content")
		if !ok {
			return "", fmt.Errorf("write needs a content argument")
		}
		if ok, reason := a.CanWrite(path); !ok {
			return "", fmt.Errorf("write blocked: %s", reason)
		}
		a.ensureCheckpoint()
		diff, written, err := tools.WriteFile(path, content, a.Config.Mode, a.Approve)
		if err != nil {
			return "", err
		}
		a.LastDiff = diff
		if a.Config.Mode == "plan" {
			return fmt.Sprintf("Plan mode — would write %s, not written:\n%s", path, diff), nil
		}
		if !written {
			return fmt.Sprintf("Write rejected, diff was:\n%s", diff), nil
		}
		return fmt.Sprintf("Wrote %s:\n%s", path, diff), nil
	case "edit":
		path, ok := loop.StrArg(call.Arguments, "path")
		if !ok {
			return "", fmt.Errorf("edit needs a path argument")
		}
		oldText, ok := loop.StrArg(call.Arguments, "old_text")
		if !ok {
			return "", fmt.Errorf("edit needs an old_text argument")
		}
		newText, ok := loop.StrArg(call.Arguments, "new_text")
		if !ok {
			return "", fmt.Errorf("edit needs a new_text argument")
		}
		if ok, reason := a.CanWrite(path); !ok {
			return "", fmt.Errorf("edit blocked: %s", reason)
		}
		a.ensureCheckpoint()
		diff, written, err := tools.EditFile(path, oldText, newText, a.Config.Mode, a.Approve)
		if err != nil {
			return "", err
		}
		a.LastDiff = diff
		if a.Config.Mode == "plan" {
			return fmt.Sprintf("Plan mode — would edit %s, not written:\n%s", path, diff), nil
		}
		if !written {
			return fmt.Sprintf("Edit rejected, diff was:\n%s", diff), nil
		}
		return fmt.Sprintf("Edited %s:\n%s", path, diff), nil
	case "run":
		cmd, ok := loop.StrArg(call.Arguments, "command")
		if !ok {
			return "", fmt.Errorf("run needs a command argument")
		}
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
		return out, nil
	case "glob":
		pattern, ok := loop.StrArg(call.Arguments, "pattern")
		if !ok {
			return "", fmt.Errorf("glob needs a pattern argument")
		}
		files, err := tools.Glob(a.Workdir, pattern)
		if err != nil {
			return "", err
		}
		if len(files) == 0 {
			return fmt.Sprintf("No files match %q.", pattern), nil
		}
		return strings.Join(files, "\n"), nil
	case "apply_patch":
		patch, ok := loop.StrArg(call.Arguments, "patch")
		if !ok {
			return "", fmt.Errorf("apply_patch needs a patch argument")
		}
		ops, err := tools.ParsePatch(patch)
		if err != nil {
			return "", err
		}
		// Every touched path goes through the sandbox first.
		for _, op := range ops {
			if ok, reason := a.CanWrite(op.Path); !ok {
				return "", fmt.Errorf("apply_patch blocked: %s", reason)
			}
			if op.Kind == "rename" {
				if ok, reason := a.CanWrite(op.Dest); !ok {
					return "", fmt.Errorf("apply_patch blocked: %s", reason)
				}
			}
		}
		a.ensureCheckpoint()
		diff, written, err := tools.ApplyPatch(ops, a.Config.Mode, a.Approve)
		if err != nil {
			return "", err
		}
		a.LastDiff = diff
		if a.Config.Mode == "plan" {
			return fmt.Sprintf("Plan mode — would apply patch, not written:\n%s", diff), nil
		}
		if !written {
			return fmt.Sprintf("Patch rejected, diff was:\n%s", diff), nil
		}
		return fmt.Sprintf("Patch applied:\n%s", diff), nil
	case "skill":
		name, ok := loop.StrArg(call.Arguments, "name")
		if !ok {
			return "", fmt.Errorf("skill needs a name argument")
		}
		for _, s := range skills.Load(a.Workdir) {
			if strings.EqualFold(s.Name, name) {
				return s.Content, nil
			}
		}
		var names []string
		for _, s := range skills.Load(a.Workdir) {
			names = append(names, s.Name)
		}
		return "", fmt.Errorf("skill %q not found (available: %s)", name, strings.Join(names, ", "))
	case "todowrite":
		action, ok := loop.StrArg(call.Arguments, "action")
		if !ok {
			return "", fmt.Errorf("todowrite needs an action argument")
		}
		switch action {
		case "add":
			text, ok := loop.StrArg(call.Arguments, "text")
			if !ok || strings.TrimSpace(text) == "" {
				return "", fmt.Errorf("todowrite add needs a text argument")
			}
			a.Todos = append(a.Todos, TodoItem{Text: text, Status: "pending"})
		case "start", "done":
			idx, ok := loop.IntArg(call.Arguments, "index")
			if !ok || idx < 1 || idx > len(a.Todos) {
				return "", fmt.Errorf("todowrite %s needs an index between 1 and %d", action, len(a.Todos))
			}
			if action == "start" {
				a.Todos[idx-1].Status = "in_progress"
			} else {
				a.Todos[idx-1].Status = "completed"
			}
		case "list":
		case "clear":
			a.Todos = nil
		default:
			return "", fmt.Errorf("todowrite: unknown action %q (add|start|done|list|clear)", action)
		}
		return renderTodos(a.Todos), nil
	case "question":
		if a.QuestionFunc == nil {
			return "", fmt.Errorf("question tool is unavailable in non-interactive mode (print mode cannot ask the user)")
		}
		q, ok := loop.StrArg(call.Arguments, "question")
		if !ok || strings.TrimSpace(q) == "" {
			return "", fmt.Errorf("question needs a question argument")
		}
		options, _ := loop.StrListArg(call.Arguments, "options")
		answer, err := a.QuestionFunc(q, options)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("User answered: %s", answer), nil
	case "task":
		if a.TaskDepth >= 1 {
			return "", fmt.Errorf("subagents cannot spawn further tasks (max depth reached)")
		}
		prompt, ok := loop.StrArg(call.Arguments, "prompt")
		if !ok || strings.TrimSpace(prompt) == "" {
			return "", fmt.Errorf("task needs a prompt argument")
		}
		sub := New(a.Config, a.Workdir, a.Approve)
		sub.TaskDepth = a.TaskDepth + 1
		answer, err := sub.RunAgent(ctx, prompt, nil)
		if err != nil {
			return "", fmt.Errorf("subagent failed: %w", err)
		}
		return answer, nil
	case "plan":
		steps, ok := loop.StrListArg(call.Arguments, "steps")
		if !ok || len(steps) == 0 {
			return "", fmt.Errorf("plan needs a non-empty steps argument")
		}
		a.CurrentPlan = steps
		var b strings.Builder
		fmt.Fprintf(&b, "Plan recorded (%d steps):\n", len(steps))
		for i, s := range steps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, s)
		}
		return b.String(), nil
	case "webfetch":
		u, ok := loop.StrArg(call.Arguments, "url")
		if !ok || strings.TrimSpace(u) == "" {
			return "", fmt.Errorf("webfetch needs a url argument")
		}
		return web.Fetch(u)
	case "websearch":
		q, ok := loop.StrArg(call.Arguments, "query")
		if !ok || strings.TrimSpace(q) == "" {
			return "", fmt.Errorf("websearch needs a query argument")
		}
		searcher := a.WebSearcher
		if searcher == nil {
			searcher = &web.Searcher{ExaAPIKey: os.Getenv("EXA_API_KEY")}
		}
		results, err := searcher.Search(ctx, q)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for i, r := range results {
			fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, r.Snippet)
		}
		return b.String(), nil
	case "lsp":
		client, err := a.lsp()
		if err != nil {
			return "", err
		}
		op, ok := loop.StrArg(call.Arguments, "operation")
		if !ok {
			return "", fmt.Errorf("lsp needs an operation argument (definition|references|hover|symbols)")
		}
		file, ok := loop.StrArg(call.Arguments, "file")
		if !ok {
			return "", fmt.Errorf("lsp needs a file argument")
		}
		line, _ := loop.IntArg(call.Arguments, "line")
		char, _ := loop.IntArg(call.Arguments, "character")
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		switch op {
		case "definition":
			locs, err := client.Definition(lctx, file, line, char)
			if err != nil {
				return "", err
			}
			return joinLocations(locs), nil
		case "references":
			locs, err := client.References(lctx, file, line, char)
			if err != nil {
				return "", err
			}
			return joinLocations(locs), nil
		case "hover":
			text, err := client.Hover(lctx, file, line, char)
			if err != nil {
				return "", err
			}
			if text == "" {
				return "(no hover info)", nil
			}
			return text, nil
		case "symbols":
			syms, err := client.DocumentSymbol(lctx, file)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			for _, s := range syms {
				fmt.Fprintf(&b, "%s (kind %d) line %d\n", s.Name, s.Kind, s.Line+1)
			}
			return b.String(), nil
		default:
			return "", fmt.Errorf("lsp: unknown operation %q (definition|references|hover|symbols)", op)
		}
	}
	return "", fmt.Errorf("unknown tool %q", call.Name)
}

// lspCommand resolves the language server command.
func (a *Agent) lspCommand() string {
	if a.LSPCommand != "" {
		return a.LSPCommand
	}
	if cmd := os.Getenv("ROUTE_X_LSP_COMMAND"); cmd != "" {
		return cmd
	}
	return a.Config.LSPServer
}

// lsp lazily starts (and caches) the language server client.
func (a *Agent) lsp() (*lsp.Client, error) {
	a.lspMu.Lock()
	defer a.lspMu.Unlock()
	if a.lspClient != nil {
		return a.lspClient, nil
	}
	cmd := a.lspCommand()
	if strings.TrimSpace(cmd) == "" {
		return nil, fmt.Errorf("no LSP server configured: set lsp_server in ~/.route-x/config.json (e.g. \"gopls\") or ROUTE_X_LSP_COMMAND")
	}
	client, err := lsp.Start(cmd, a.Workdir)
	if err != nil {
		return nil, err
	}
	a.lspClient = client
	return client, nil
}

func joinLocations(locs []lsp.Location) string {
	if len(locs) == 0 {
		return "(no results)"
	}
	var b strings.Builder
	for _, l := range locs {
		fmt.Fprintln(&b, l.String())
	}
	return strings.TrimRight(b.String(), "\n")
}

// HasToolProvider reports whether the active provider can call
// tools (gemini / openai / anthropic / ollama). The local provider
// cannot; the caller falls back to the keyword dispatch in Run.
func (a *Agent) HasToolProvider() bool { return a.toolProvider() != nil }

// RunAgent handles one user prompt with the agentic loop: the
// model drives tools until it answers. It returns an error when
// the active provider cannot call tools (local provider) — the
// caller then falls back to the keyword dispatch in Run.
func (a *Agent) RunAgent(ctx context.Context, prompt string, onEvent func(loop.Event)) (string, error) {
	provider := a.toolProvider()
	if provider == nil {
		return "", fmt.Errorf("provider %q cannot call tools; use the keyword dispatch", a.Config.Provider)
	}
	system := "You are Route-X, a terminal coding agent. " + a.workspaceContext() +
		"\nUse the tools to inspect and change the workspace. " +
		"When a task is done, answer with a short summary. " +
		"Never claim a file was changed or a command passed unless a tool result proves it."
	messages := []loop.Message{
		{Role: "user", Content: system + "\n\nUser request:\n" + prompt},
	}
	answer, usage, err := loop.Run(ctx, provider, messages, ToolDefs(), a.ExecuteTool, loop.DefaultMaxSteps, onEvent)
	if err != nil {
		return "", err
	}
	a.LastUsage = usage
	a.TotalUsage.InputTokens += usage.InputTokens
	a.TotalUsage.OutputTokens += usage.OutputTokens
	return answer, nil
}

// Compact replaces a long session history with a short summary,
// like OpenCode's /compact: the provider summarises the
// conversation into objectives, decisions and next steps, and the
// summary becomes the session's first message. Sessions with
// fewer than 4 messages have nothing worth compacting.
func (a *Agent) Compact(ctx context.Context, sess *session.Session) error {
	if len(sess.Messages) < 4 {
		return fmt.Errorf("nothing to compact: only %d message(s)", len(sess.Messages))
	}
	var b strings.Builder
	for _, m := range sess.Messages {
		fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.Content)
	}
	prompt := "Summarise this coding session into a short checkpoint " +
		"the agent can continue from. Cover: (1) the goal, " +
		"(2) decisions made, (3) what was changed, " +
		"(4) what is still open. Keep it under 500 words.\n\n" + b.String()
	summary, err := a.provider().Complete(ctx, prompt, "")
	if err != nil {
		return fmt.Errorf("compact failed: %w", err)
	}
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("compact failed: provider returned an empty summary")
	}
	sess.Messages = []session.Message{{
		Role:    "agent",
		Content: "Session summary (compacted from " + fmt.Sprint(len(sess.Messages)) + " messages):\n" + summary,
		Time:    time.Now().Format(time.RFC3339),
	}}
	return nil
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
			a.ensureCheckpoint()
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
	sa := &SubAgent{ID: id, Task: task, Status: "running", Started: time.Now().Format(time.RFC3339), done: make(chan struct{})}
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
		if len(matches) == 0 {
			sa.Result = fmt.Sprintf("Subagent %s finished: no matches for %q.", id, keyword)
		} else {
			sa.Result = fmt.Sprintf("Subagent %s finished: %d match(es) for %q, first at %s:%d.",
				id, len(matches), keyword, matches[0].File, matches[0].Line)
		}
		sa.Status = "done"
		sa.Finished = time.Now().Format(time.RFC3339)
		a.mu.Unlock()
		// Persist so /agents shows it after a restart.
		a.persistSubagent(sa)
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
