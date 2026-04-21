package tokencount

import (
	"context"
	"strings"
)

// TokenCounts holds token counting results for a single request.
type TokenCounts struct {
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	PromptEstimated  bool `json:"prompt_estimated"`
	OutputEstimated  bool `json:"output_estimated"`
}

// TokenContext holds per-request token counting state.
type TokenContext struct {
	ProviderName string
	ProviderType string // "openai" or "anthropic"
	Model        string
	Counts       TokenCounts
	Parser       *StreamingUsageParser
	Enabled      bool
}

type ctxKey struct{}

// WithContext stores a TokenContext in the context.
func WithContext(ctx context.Context, tc *TokenContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, tc)
}

// FromContext retrieves a TokenContext from the context.
func FromContext(ctx context.Context) *TokenContext {
	if tc, ok := ctx.Value(ctxKey{}).(*TokenContext); ok {
		return tc
	}
	return nil
}

// CountPromptTokens estimates prompt tokens from a request body.
func CountPromptTokens(providerType string, body []byte) int {
	rc := ParseRequestContent(body)

	allText := strings.Join(rc.Texts, "\n")

	p := providerFromProviderType(providerType, rc.Model)

	// Try tiktoken for OpenAI models
	if p == providerOpenAI {
		if n := countTokensAccurate(rc.Model, allText); n >= 0 {
			tokens := n + rc.Tools*8 + len(rc.Texts)*3 + 3
			if tokens < 1 {
				tokens = 1
			}
			return tokens
		}
	}

	// Fallback to estimation
	return CountPromptFromRequest(rc, providerType)
}

// EstimateCompletionTokens estimates output tokens from response text.
func EstimateCompletionTokens(providerType, model string, text string) int {
	p := providerFromProviderType(providerType, model)
	n := estimateTokens(p, text)
	if n < 1 && text != "" {
		n = 1
	}
	return n
}

func providerFromProviderType(providerType, model string) provider {
	// Model name takes precedence so a Claude model routed through an
	// OpenAI-compatible endpoint still uses Claude multipliers.
	if model != "" {
		return providerFromModel(model)
	}
	if providerType == "anthropic" {
		return providerClaude
	}
	return providerOpenAI
}
