package loop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// scriptedProvider returns a fixed script of replies, one per call,
// and records every message list it was given so the test can
// assert the loop fed tool results back.
type scriptedProvider struct {
	script  []Message
	seen    [][]Message
	usages  []Usage
	callErr error
}

func (p *scriptedProvider) CompleteTools(_ context.Context, messages []Message, _ []ToolDef) (Message, Usage, error) {
	p.seen = append(p.seen, append([]Message(nil), messages...))
	if p.callErr != nil {
		return Message{}, Usage{}, p.callErr
	}
	if len(p.script) == 0 {
		return Message{}, Usage{}, errors.New("script exhausted")
	}
	reply := p.script[0]
	p.script = p.script[1:]
	return reply, Usage{InputTokens: 10, OutputTokens: 5}, nil
}

func TestRunTwoStepTask(t *testing.T) {
	// The model reads a file, then writes a new one from what it
	// read, then answers. The loop must feed the read result back
	// before the second step.
	provider := &scriptedProvider{script: []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.txt"}}}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c2", Name: "write", Arguments: map[string]any{"path": "b.txt", "content": "hello"}}}},
		{Role: "assistant", Content: "done"},
	}}
	var executed []string
	exec := func(_ context.Context, call ToolCall) (string, error) {
		executed = append(executed, call.Name)
		if call.Name == "read" {
			return "file contents: hello", nil
		}
		return "wrote b.txt", nil
	}
	answer, usage, err := Run(context.Background(), provider,
		[]Message{{Role: "user", Content: "copy a.txt to b.txt"}},
		[]ToolDef{{Name: "read"}, {Name: "write"}},
		exec, 10, nil)
	if err != nil || answer != "done" {
		t.Fatalf("answer = %q, err=%v", answer, err)
	}
	if len(executed) != 2 || executed[0] != "read" || executed[1] != "write" {
		t.Fatalf("executed = %v", executed)
	}
	// The second provider call must have seen the tool result.
	if len(provider.seen) != 3 {
		t.Fatalf("provider calls = %d", len(provider.seen))
	}
	secondCall := provider.seen[1]
	found := false
	for _, m := range secondCall {
		if m.Role == "tool" && strings.Contains(m.Content, "file contents: hello") {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool result was not fed back: %+v", secondCall)
	}
	if usage.InputTokens != 30 || usage.OutputTokens != 15 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestRunToolErrorIsFedBack(t *testing.T) {
	provider := &scriptedProvider{script: []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "read", Arguments: map[string]any{"path": "missing.txt"}}}},
		{Role: "assistant", Content: "that file does not exist"},
	}}
	exec := func(_ context.Context, _ ToolCall) (string, error) {
		return "", fmt.Errorf("no such file")
	}
	answer, _, err := Run(context.Background(), provider,
		[]Message{{Role: "user", Content: "read missing.txt"}},
		[]ToolDef{{Name: "read"}}, exec, 10, nil)
	if err != nil || answer != "that file does not exist" {
		t.Fatalf("answer = %q, err=%v", answer, err)
	}
	// The error text must reach the model as a tool message.
	secondCall := provider.seen[1]
	found := false
	for _, m := range secondCall {
		if m.Role == "tool" && strings.Contains(m.Content, "error: no such file") {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool error was not fed back: %+v", secondCall)
	}
}

func TestRunRespectsStepCap(t *testing.T) {
	// A model that always calls tools must be stopped by the cap.
	provider := &scriptedProvider{script: []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "read"}}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c2", Name: "read"}}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c3", Name: "read"}}},
	}}
	exec := func(_ context.Context, _ ToolCall) (string, error) { return "x", nil }
	_, _, err := Run(context.Background(), provider,
		[]Message{{Role: "user", Content: "loop forever"}},
		[]ToolDef{{Name: "read"}}, exec, 2, nil)
	if err == nil || !strings.Contains(err.Error(), "step cap") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTextOnlyAnswerEndsImmediately(t *testing.T) {
	provider := &scriptedProvider{script: []Message{
		{Role: "assistant", Content: "hello"},
	}}
	calls := 0
	exec := func(_ context.Context, _ ToolCall) (string, error) { calls++; return "", nil }
	answer, _, err := Run(context.Background(), provider,
		[]Message{{Role: "user", Content: "hi"}},
		[]ToolDef{{Name: "read"}}, exec, 10, nil)
	if err != nil || answer != "hello" || calls != 0 || len(provider.seen) != 1 {
		t.Fatalf("answer=%q err=%v calls=%d providerCalls=%d", answer, err, calls, len(provider.seen))
	}
}

func TestRunProviderErrorAborts(t *testing.T) {
	provider := &scriptedProvider{callErr: errors.New("boom")}
	exec := func(_ context.Context, _ ToolCall) (string, error) { return "", nil }
	_, _, err := Run(context.Background(), provider,
		[]Message{{Role: "user", Content: "hi"}},
		[]ToolDef{}, exec, 10, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}
