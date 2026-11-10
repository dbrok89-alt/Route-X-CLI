// Package loop is the agentic loop: the model drives tools, reads
// their results, and repeats until it answers or hits a step cap.
// This is the piece v0.2.1 was missing — providers were single-shot
// text completions and the "agent" was keyword dispatch. The loop is
// provider-agnostic: each provider only has to translate between
// these types and its own wire format (tools / tool_calls /
// functionCall), and the executor enforces the sandbox and approval
// gates exactly like the keyword dispatch does.
package loop

import (
	"context"
	"fmt"
	"strings"
)

// ToolDef is one tool the model may call, described as a JSON
// schema the provider sends to the model.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// Message is one turn in the conversation. A model message may
// carry ToolCalls; a tool message carries one ToolCall's result
// (ToolCallID links them, ToolName names the tool — Gemini's
// functionResponse needs the name, not just the id).
type Message struct {
	Role       string     `json:"role"` // user | assistant | tool
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
}

// Usage is token accounting returned by a provider step.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ToolProvider is a model that can call tools. CompleteTools sends
// the conversation plus the tool definitions and returns the
// model's next message: either text, tool calls, or both.
type ToolProvider interface {
	CompleteTools(ctx context.Context, messages []Message, tools []ToolDef) (Message, Usage, error)
}

// Streamer is the optional streaming variant of ToolProvider.
// When a provider implements it, Run calls StreamTools instead of
// CompleteTools so text tokens flow to onToken as they arrive.
// Tool calls are still returned whole at the end of the step.
type Streamer interface {
	ToolProvider
	StreamTools(ctx context.Context, messages []Message, tools []ToolDef, onToken func(string)) (Message, Usage, error)
}

// Executor runs one tool call and returns its result as text.
// It is the only place that touches the filesystem or the shell,
// so sandbox and approval enforcement lives here (or in the tools
// it calls), never in the loop itself.
type Executor func(ctx context.Context, call ToolCall) (string, error)

// Event is emitted by Run for progress reporting (streaming text,
// tool start/finish). The caller decides how to display it.
type Event struct {
	Kind    string // text | tool_start | tool_result | done
	Text    string
	Tool    string
	ToolID  string
	Step    int
	Usage   Usage
	IsError bool
}

// DefaultMaxSteps caps one agentic run so a confused model cannot
// loop forever. OpenCode uses an agent step cap the same way.
const DefaultMaxSteps = 10

// Run executes the agentic loop. It returns the model's final text
// answer. A step that returns only text ends the loop; a step with
// tool calls executes each call (in order) and feeds the results
// back as tool messages. If the model still calls tools at the cap,
// Run stops and reports that the cap was hit.
func Run(ctx context.Context, provider ToolProvider, messages []Message, tools []ToolDef, exec Executor, maxSteps int, onEvent func(Event)) (string, Usage, error) {
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	var total Usage
	msgs := append([]Message(nil), messages...)
	emit := func(e Event) {
		if onEvent != nil {
			onEvent(e)
		}
	}
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", total, err
		}
		var reply Message
		var usage Usage
		var err error
		if streamer, ok := provider.(Streamer); ok {
			reply, usage, err = streamer.StreamTools(ctx, msgs, tools, func(token string) {
				emit(Event{Kind: "text", Text: token, Step: step, IsError: false})
			})
		} else {
			reply, usage, err = provider.CompleteTools(ctx, msgs, tools)
		}
		if err != nil {
			return "", total, fmt.Errorf("step %d: %w", step, err)
		}
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		msgs = append(msgs, reply)
		if _, streamed := provider.(Streamer); !streamed && reply.Content != "" {
			// Non-streaming providers: emit the whole answer now.
			// Streamers already emitted it token by token.
			emit(Event{Kind: "text", Text: reply.Content, Step: step, Usage: usage})
		}
		if len(reply.ToolCalls) == 0 {
			emit(Event{Kind: "done", Text: reply.Content, Step: step, Usage: total})
			return reply.Content, total, nil
		}
		for _, call := range reply.ToolCalls {
			emit(Event{Kind: "tool_start", Tool: call.Name, ToolID: call.ID, Step: step})
			result, err := exec(ctx, call)
			if err != nil {
				// A failed tool call is data for the model, not a
				// fatal error: the model sees the error text and
				// can retry or change approach.
				result = "error: " + err.Error()
				emit(Event{Kind: "tool_result", Tool: call.Name, ToolID: call.ID, Step: step, Text: result, IsError: true})
			} else {
				emit(Event{Kind: "tool_result", Tool: call.Name, ToolID: call.ID, Step: step, Text: result})
			}
			msgs = append(msgs, Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: call.ID,
				ToolName:   call.Name,
			})
		}
	}
	return "", total, fmt.Errorf("step cap reached after %d steps without a final answer", maxSteps)
}

// Schema is a helper for building a tool parameter schema:
// Schema("object", map of properties, required names).
func Schema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

// StrArg reads a string argument, returning "" when absent or not
// a string. Models sometimes send numbers for string fields; be
// strict instead of guessing — a missing argument is the model's
// mistake and the executor reports it.
func StrArg(args map[string]any, name string) (string, bool) {
	v, ok := args[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// Describe renders the tool list for a plain-text provider prompt.
func Describe(tools []ToolDef) string {
	var b strings.Builder
	for _, t := range tools {
		fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Description)
	}
	return b.String()
}

// IntArg reads an integer argument (JSON numbers arrive as
// float64). Returns false when absent or not a number.
func IntArg(args map[string]any, name string) (int, bool) {
	v, ok := args[name]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// StrListArg reads a string-list argument: either a JSON array of
// strings or a single string (treated as a one-item list).
func StrListArg(args map[string]any, name string) ([]string, bool) {
	v, ok := args[name]
	if !ok {
		return nil, false
	}
	if s, ok := v.(string); ok {
		return []string{s}, true
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}
