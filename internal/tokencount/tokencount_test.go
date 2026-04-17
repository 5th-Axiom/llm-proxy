package tokencount

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		min      int
		max      int
		provider provider
	}{
		{"empty", "", 0, 0, providerOpenAI},
		{"english", "hello world", 1, 5, providerOpenAI},
		{"chinese", "你好世界", 1, 8, providerOpenAI},
		{"mixed", "hello 你好 world 世界", 2, 12, providerOpenAI},
		{"claude english", "hello world", 1, 6, providerClaude},
		{"gemini chinese", "你好世界", 1, 6, providerGemini},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.provider, tt.text)
			if got < tt.min || got > tt.max {
				t.Errorf("estimateTokens(%v, %q) = %d, want [%d, %d]", tt.provider, tt.text, got, tt.min, tt.max)
			}
		})
	}
}

func TestParseRequestContentOpenAI(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4o",
		"stream": true,
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello!"}
		],
		"tools": [{"type": "function", "function": {"name": "get_weather"}}]
	}`)

	rc := ParseRequestContent(body)
	if rc.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", rc.Model, "gpt-4o")
	}
	if !rc.Stream {
		t.Error("Stream = false, want true")
	}
	if rc.Tools != 1 {
		t.Errorf("Tools = %d, want 1", rc.Tools)
	}
	if len(rc.Texts) != 2 {
		t.Fatalf("len(Texts) = %d, want 2", len(rc.Texts))
	}
	if rc.Texts[0] != "You are helpful." {
		t.Errorf("Texts[0] = %q", rc.Texts[0])
	}
	if rc.Texts[1] != "Hello!" {
		t.Errorf("Texts[1] = %q", rc.Texts[1])
	}
}

func TestParseRequestContentAnthropic(t *testing.T) {
	body := json.RawMessage(`{
		"model": "claude-sonnet-4-20250514",
		"system": "You are a coding assistant.",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "Write a function"}
			]}
		]
	}`)

	rc := ParseRequestContent(body)
	if rc.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q", rc.Model)
	}
	if len(rc.Texts) != 2 {
		t.Fatalf("len(Texts) = %d, want 2 (system + message)", len(rc.Texts))
	}
	if rc.Texts[0] != "You are a coding assistant." {
		t.Errorf("Texts[0] (system) = %q", rc.Texts[0])
	}
	if rc.Texts[1] != "Write a function" {
		t.Errorf("Texts[1] = %q", rc.Texts[1])
	}
}

func TestParseNonStreamingUsageOpenAI(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-123",
		"usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
	}`)

	u := ParseNonStreamingUsage(body)
	if !u.Found {
		t.Fatal("Found = false, want true")
	}
	if u.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", u.PromptTokens)
	}
	if u.CompletionTokens != 20 {
		t.Errorf("CompletionTokens = %d, want 20", u.CompletionTokens)
	}
	if u.TotalTokens != 30 {
		t.Errorf("TotalTokens = %d, want 30", u.TotalTokens)
	}
}

func TestParseNonStreamingUsageAnthropic(t *testing.T) {
	body := json.RawMessage(`{
		"id": "msg_123",
		"usage": {"input_tokens": 15, "output_tokens": 25}
	}`)

	u := ParseNonStreamingUsage(body)
	if !u.Found {
		t.Fatal("Found = false, want true")
	}
	if u.PromptTokens != 15 {
		t.Errorf("PromptTokens = %d, want 15", u.PromptTokens)
	}
	if u.CompletionTokens != 25 {
		t.Errorf("CompletionTokens = %d, want 25", u.CompletionTokens)
	}
	if u.TotalTokens != 40 {
		t.Errorf("TotalTokens = %d, want 40", u.TotalTokens)
	}
}

func TestStreamingUsageParserOpenAI(t *testing.T) {
	p := NewStreamingUsageParser("openai", "gpt-4o")

	p.ProcessChunk([]byte(`data: {"choices":[{"delta":{"content":"Hello"}}]}`))
	p.ProcessChunk([]byte(`data: {"choices":[{"delta":{"content":" world"}}]}`))
	p.ProcessChunk([]byte(`data: {"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
	p.ProcessChunk([]byte(`data: [DONE]`))

	tc := p.Finalize()
	if tc.PromptTokens != 5 {
		t.Errorf("PromptTokens = %d, want 5", tc.PromptTokens)
	}
	if tc.CompletionTokens != 2 {
		t.Errorf("CompletionTokens = %d, want 2", tc.CompletionTokens)
	}
	if tc.OutputEstimated {
		t.Error("OutputEstimated = true, want false (usage was found)")
	}
}

func TestStreamingUsageParserAnthropic(t *testing.T) {
	p := NewStreamingUsageParser("anthropic", "claude-sonnet-4-20250514")

	p.ProcessChunk([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}`))
	p.ProcessChunk([]byte(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hi there"}}`))
	p.ProcessChunk([]byte(`data: {"type":"message_delta","usage":{"output_tokens":3}}`))

	tc := p.Finalize()
	if tc.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", tc.PromptTokens)
	}
	if tc.CompletionTokens != 3 {
		t.Errorf("CompletionTokens = %d, want 3", tc.CompletionTokens)
	}
	if tc.OutputEstimated {
		t.Error("OutputEstimated = true, want false")
	}
}

func TestStreamingUsageParserFallback(t *testing.T) {
	p := NewStreamingUsageParser("openai", "gpt-4o")

	// No usage in stream, only content
	p.ProcessChunk([]byte(`data: {"choices":[{"delta":{"content":"Hello world, this is a test."}}]}`))
	p.ProcessChunk([]byte(`data: [DONE]`))

	tc := p.Finalize()
	if tc.CompletionTokens == 0 {
		t.Error("CompletionTokens = 0, want > 0 (fallback estimation)")
	}
	if !tc.OutputEstimated {
		t.Error("OutputEstimated = false, want true (no usage found)")
	}
}

func TestContextRoundTrip(t *testing.T) {
	tc := &TokenContext{
		ProviderName: "test",
		Enabled:      true,
		Counts:       TokenCounts{PromptTokens: 10, CompletionTokens: 20},
	}

	ctx := WithContext(context.Background(), tc)
	got := FromContext(ctx)
	if got == nil {
		t.Fatal("FromContext returned nil")
	}
	if got.ProviderName != "test" {
		t.Errorf("ProviderName = %q, want %q", got.ProviderName, "test")
	}
	if got.Counts.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", got.Counts.PromptTokens)
	}
}

func TestFromContextNil(t *testing.T) {
	got := FromContext(context.Background())
	if got != nil {
		t.Error("FromContext on empty context should return nil")
	}
}
