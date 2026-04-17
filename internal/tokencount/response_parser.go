package tokencount

import (
	"encoding/json"
	"strings"
)

// Usage represents token usage extracted from an upstream response.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Found            bool
}

// ParseNonStreamingUsage extracts usage from a complete JSON response body.
// Tries both OpenAI and Anthropic formats.
func ParseNonStreamingUsage(body []byte) Usage {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return Usage{}
	}

	usageRaw, ok := raw["usage"]
	if !ok {
		return Usage{}
	}

	var usage map[string]json.RawMessage
	if json.Unmarshal(usageRaw, &usage) != nil {
		return Usage{}
	}

	u := Usage{}

	// OpenAI format: prompt_tokens, completion_tokens, total_tokens
	if v, ok := usage["prompt_tokens"]; ok {
		json.Unmarshal(v, &u.PromptTokens)
	}
	if v, ok := usage["completion_tokens"]; ok {
		json.Unmarshal(v, &u.CompletionTokens)
	}
	if v, ok := usage["total_tokens"]; ok {
		json.Unmarshal(v, &u.TotalTokens)
	}

	// Anthropic format: input_tokens, output_tokens
	if u.PromptTokens == 0 {
		if v, ok := usage["input_tokens"]; ok {
			json.Unmarshal(v, &u.PromptTokens)
		}
	}
	if u.CompletionTokens == 0 {
		if v, ok := usage["output_tokens"]; ok {
			json.Unmarshal(v, &u.CompletionTokens)
		}
	}

	if u.PromptTokens > 0 || u.CompletionTokens > 0 {
		u.Found = true
		if u.TotalTokens == 0 {
			u.TotalTokens = u.PromptTokens + u.CompletionTokens
		}
	}

	return u
}

// StreamingUsageParser incrementally processes SSE chunks to extract token usage.
type StreamingUsageParser struct {
	provider   provider
	model      string
	lastUsage  Usage
	contentBuf strings.Builder
}

// NewStreamingUsageParser creates a parser for streaming responses.
func NewStreamingUsageParser(providerType, model string) *StreamingUsageParser {
	return &StreamingUsageParser{
		provider: providerFromModel(model),
		model:    model,
	}
}

// ProcessChunk processes bytes from an SSE data line.
func (p *StreamingUsageParser) ProcessChunk(line []byte) {
	line = bytesTrimSSEPrefix(line)
	if len(line) == 0 || string(line) == "[DONE]" {
		return
	}

	var raw map[string]json.RawMessage
	if json.Unmarshal(line, &raw) != nil {
		return
	}

	// OpenAI streaming format
	// choices[].delta.content — accumulate text
	if choicesRaw, ok := raw["choices"]; ok {
		p.extractOpenAIContent(choicesRaw)
	}

	// OpenAI streaming usage (last chunk when stream_options.include_usage=true)
	if usageRaw, ok := raw["usage"]; ok {
		var u map[string]json.RawMessage
		if json.Unmarshal(usageRaw, &u) == nil {
			if v, ok := u["prompt_tokens"]; ok {
				json.Unmarshal(v, &p.lastUsage.PromptTokens)
			}
			if v, ok := u["completion_tokens"]; ok {
				json.Unmarshal(v, &p.lastUsage.CompletionTokens)
			}
			if v, ok := u["total_tokens"]; ok {
				json.Unmarshal(v, &p.lastUsage.TotalTokens)
			}
			if p.lastUsage.PromptTokens > 0 || p.lastUsage.CompletionTokens > 0 {
				p.lastUsage.Found = true
			}
		}
	}

	// Anthropic streaming format
	if typ, ok := raw["type"]; ok {
		var typeStr string
		json.Unmarshal(typ, &typeStr)
		switch typeStr {
		case "message_start":
			// {"type":"message_start","message":{"usage":{"input_tokens":N}}}
			if msgRaw, ok := raw["message"]; ok {
				var msg map[string]json.RawMessage
				if json.Unmarshal(msgRaw, &msg) == nil {
					if usageRaw, ok := msg["usage"]; ok {
						var u map[string]json.RawMessage
						if json.Unmarshal(usageRaw, &u) == nil {
							if v, ok := u["input_tokens"]; ok {
								json.Unmarshal(v, &p.lastUsage.PromptTokens)
							}
						}
					}
				}
			}
		case "content_block_delta":
			// {"type":"content_block_delta","delta":{"type":"text_delta","text":"..."}}
			if deltaRaw, ok := raw["delta"]; ok {
				var delta map[string]json.RawMessage
				if json.Unmarshal(deltaRaw, &delta) == nil {
					if t, ok := delta["text"]; ok {
						var s string
						if json.Unmarshal(t, &s) == nil {
							p.contentBuf.WriteString(s)
						}
					}
				}
			}
		case "message_delta":
			// {"type":"message_delta","usage":{"output_tokens":N}}
			if usageRaw, ok := raw["usage"]; ok {
				var u map[string]json.RawMessage
				if json.Unmarshal(usageRaw, &u) == nil {
					if v, ok := u["output_tokens"]; ok {
						json.Unmarshal(v, &p.lastUsage.CompletionTokens)
						p.lastUsage.Found = true
					}
				}
			}
		}
	}

	// OpenAI: accumulate delta content for fallback
	if deltaRaw, ok := raw["choices"]; !ok {
		_ = deltaRaw // already handled above
	} else {
		// content already extracted in extractOpenAIContent
	}
}

// Finalize returns final token counts. Falls back to estimation if no usage was found.
func (p *StreamingUsageParser) Finalize() TokenCounts {
	tc := TokenCounts{}

	if p.lastUsage.Found {
		tc.PromptTokens = p.lastUsage.PromptTokens
		tc.CompletionTokens = p.lastUsage.CompletionTokens
		tc.TotalTokens = p.lastUsage.TotalTokens
		if tc.TotalTokens == 0 {
			tc.TotalTokens = tc.PromptTokens + tc.CompletionTokens
		}
		tc.OutputEstimated = false
	} else {
		text := p.contentBuf.String()
		tc.CompletionTokens = estimateTokens(p.provider, text)
		if tc.CompletionTokens == 0 {
			tc.CompletionTokens = 1
		}
		tc.TotalTokens = tc.PromptTokens + tc.CompletionTokens
		tc.OutputEstimated = true
	}
	tc.PromptEstimated = true

	return tc
}

func (p *StreamingUsageParser) extractOpenAIContent(choicesRaw json.RawMessage) {
	var choices []map[string]json.RawMessage
	if json.Unmarshal(choicesRaw, &choices) != nil {
		return
	}
	for _, choice := range choices {
		if deltaRaw, ok := choice["delta"]; ok {
			var delta map[string]json.RawMessage
			if json.Unmarshal(deltaRaw, &delta) == nil {
				if t, ok := delta["content"]; ok {
					var s string
					if json.Unmarshal(t, &s) == nil {
						p.contentBuf.WriteString(s)
					}
				}
			}
		}
	}
}

func bytesTrimSSEPrefix(line []byte) []byte {
	s := string(line)
	s = strings.TrimPrefix(s, "data: ")
	s = strings.TrimPrefix(s, "data:")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return []byte(s)
}
