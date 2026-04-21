package proxy

import (
	"bytes"
	"testing"

	"llm-proxy/internal/tokencount"
)

// TestTokenCountingWriterSplitsMultiEventChunks verifies that when upstream
// packs multiple SSE events into a single Write, the parser still sees each
// one as a distinct line.
func TestTokenCountingWriterSplitsMultiEventChunks(t *testing.T) {
	parser := tokencount.NewStreamingUsageParser("openai", "gpt-4o")
	var sink bytes.Buffer
	w := &tokenCountingWriter{writer: &sink, parser: parser}

	chunk := []byte(`data: {"choices":[{"delta":{"content":"Hello"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":" world"}}]}` + "\n\n" +
		`data: {"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\n" +
		`data: [DONE]` + "\n\n")

	if _, err := w.Write(chunk); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	w.flushBuffer()

	if sink.String() != string(chunk) {
		t.Fatalf("client payload mismatch: got %q, want %q", sink.String(), string(chunk))
	}

	tc := parser.Finalize()
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

// TestTokenCountingWriterRejoinsSplitEvent verifies that an SSE event split
// across two Writes (as happens with small MTUs or slow upstreams) still
// parses correctly once the closing newline arrives.
func TestTokenCountingWriterRejoinsSplitEvent(t *testing.T) {
	parser := tokencount.NewStreamingUsageParser("anthropic", "claude-sonnet-4-20250514")
	var sink bytes.Buffer
	w := &tokenCountingWriter{writer: &sink, parser: parser}

	part1 := []byte(`data: {"type":"message_start","message":{"usa`)
	part2 := []byte(`ge":{"input_tokens":10}}}` + "\n\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":3}}` + "\n\n")

	if _, err := w.Write(part1); err != nil {
		t.Fatalf("Write(part1) error = %v", err)
	}
	if _, err := w.Write(part2); err != nil {
		t.Fatalf("Write(part2) error = %v", err)
	}
	w.flushBuffer()

	tc := parser.Finalize()
	if tc.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", tc.PromptTokens)
	}
	if tc.CompletionTokens != 3 {
		t.Errorf("CompletionTokens = %d, want 3", tc.CompletionTokens)
	}
}

// TestTokenCountingWriterHandlesCRLF ensures CRLF line endings are stripped
// before feeding the line to the JSON parser.
func TestTokenCountingWriterHandlesCRLF(t *testing.T) {
	parser := tokencount.NewStreamingUsageParser("openai", "gpt-4o")
	var sink bytes.Buffer
	w := &tokenCountingWriter{writer: &sink, parser: parser}

	chunk := []byte("data: {\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":4,\"total_tokens\":11}}\r\n\r\ndata: [DONE]\r\n\r\n")
	if _, err := w.Write(chunk); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	w.flushBuffer()

	tc := parser.Finalize()
	if tc.PromptTokens != 7 || tc.CompletionTokens != 4 {
		t.Errorf("usage = %+v, want prompt=7 completion=4", tc)
	}
}
