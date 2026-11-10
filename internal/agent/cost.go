package agent

import (
	"strings"

	"route-x/internal/loop"
)

// modelPrice is the per-1M-token price in USD. These are public
// list prices as of 2026 and change over time; CostUSD is an
// estimate, not a bill. Unknown models return ok=false.
type modelPrice struct {
	Input, Output float64
}

var priceTable = map[string]modelPrice{
	"gemini-2.0-flash":  {0.10, 0.40},
	"gemini-1.5-flash":  {0.075, 0.30},
	"gpt-4o":            {2.50, 10.00},
	"gpt-4o-mini":       {0.15, 0.60},
	"claude-sonnet-4":   {3.00, 15.00},
	"claude-3-5-sonnet": {3.00, 15.00},
	"claude-3-5-haiku":  {0.80, 4.00},
	"llama3.1":          {0, 0},
	"llama3.2":          {0, 0},
}

// CostUSD estimates the dollar cost of usage for a model.
// It returns ok=false when the model has no known price (local
// models are free and report $0 with ok=true).
func CostUSD(model string, usage loop.Usage) (cost float64, ok bool) {
	// Local providers run on the user's own machine: free.
	lower := strings.ToLower(model)
	if strings.Contains(lower, "local") || strings.Contains(lower, "ollama") || strings.HasPrefix(lower, "llama") {
		return 0, true
	}
	p, found := priceTable[lower]
	if !found {
		// Try prefix match so "claude-sonnet-4-20250514" and
		// "gpt-4o-2024-11-20" resolve to their base price.
		for name, price := range priceTable {
			if strings.HasPrefix(lower, name) {
				p, found = price, true
				break
			}
		}
	}
	if !found {
		return 0, false
	}
	return float64(usage.InputTokens)/1e6*p.Input + float64(usage.OutputTokens)/1e6*p.Output, true
}
