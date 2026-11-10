package agent

import (
	"testing"

	"route-x/internal/loop"
)

func TestCostUSDKnownModel(t *testing.T) {
	// 1M input + 1M output tokens of gpt-4o = 2.50 + 10.00.
	cost, ok := CostUSD("gpt-4o", loop.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if !ok || cost != 12.50 {
		t.Fatalf("cost = %v, ok=%v", cost, ok)
	}
}

func TestCostUSDVersionedModel(t *testing.T) {
	cost, ok := CostUSD("claude-sonnet-4-20250514", loop.Usage{InputTokens: 1_000_000, OutputTokens: 0})
	if !ok || cost != 3.00 {
		t.Fatalf("cost = %v, ok=%v", cost, ok)
	}
}

func TestCostUSDLocalIsFree(t *testing.T) {
	cost, ok := CostUSD("local", loop.Usage{InputTokens: 999_999, OutputTokens: 999_999})
	if !ok || cost != 0 {
		t.Fatalf("cost = %v, ok=%v", cost, ok)
	}
}

func TestCostUSDUnknownModel(t *testing.T) {
	if _, ok := CostUSD("some-future-model-9", loop.Usage{InputTokens: 100, OutputTokens: 100}); ok {
		t.Fatal("unknown model should report ok=false, not a made-up price")
	}
}
