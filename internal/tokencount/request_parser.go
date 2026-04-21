package tokencount

import (
	"encoding/json"
	"strings"
)

// RequestContent holds extracted text from an LLM API request body.
type RequestContent struct {
	Model  string
	Texts  []string // accumulated text content from messages
	Tools  int
	Stream bool
}

// ParseRequestContent extracts text content from an OpenAI or Anthropic request body.
func ParseRequestContent(body []byte) RequestContent {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return RequestContent{}
	}

	rc := RequestContent{}

	if v, ok := raw["model"]; ok {
		json.Unmarshal(v, &rc.Model)
	}

	if v, ok := raw["stream"]; ok {
		json.Unmarshal(v, &rc.Stream)
	}

	// tools array length
	if v, ok := raw["tools"]; ok {
		var tools []json.RawMessage
		if json.Unmarshal(v, &tools) == nil {
			rc.Tools = len(tools)
		}
	}

	// Anthropic: top-level "system" field
	if v, ok := raw["system"]; ok {
		rc.Texts = appendSystemText(rc.Texts, v)
	}

	// messages array (OpenAI and Anthropic)
	if v, ok := raw["messages"]; ok {
		var messages []json.RawMessage
		if json.Unmarshal(v, &messages) == nil {
			for _, msg := range messages {
				rc.Texts = appendMessageTexts(rc.Texts, msg)
			}
		}
	}

	return rc
}

func appendSystemText(texts []string, raw json.RawMessage) []string {
	// system can be a string or an array of content blocks
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return append(texts, s)
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if t, ok := b["text"]; ok {
				var ts string
				if json.Unmarshal(t, &ts) == nil && ts != "" {
					texts = append(texts, ts)
				}
			}
		}
	}
	return texts
}

func appendMessageTexts(texts []string, msg json.RawMessage) []string {
	var m map[string]json.RawMessage
	if json.Unmarshal(msg, &m) != nil {
		return texts
	}

	contentRaw, ok := m["content"]
	if !ok {
		return texts
	}

	// content as string
	var s string
	if json.Unmarshal(contentRaw, &s) == nil {
		if s != "" {
			return append(texts, s)
		}
		return texts
	}

	// content as array of blocks
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(contentRaw, &blocks) != nil {
		return texts
	}

	for _, b := range blocks {
		// OpenAI: {"type":"text","text":"..."}
		// Anthropic: {"type":"text","text":"..."}
		if t, ok := b["text"]; ok {
			var ts string
			if json.Unmarshal(t, &ts) == nil && ts != "" {
				texts = append(texts, ts)
			}
		}
		// OpenAI image_url: skip (no text to count)
		// Anthropic: type "image" has source.base64_data — skip
	}

	return texts
}

// ExtractModelFromRequest is a convenience function that returns just the model name.
func ExtractModelFromRequest(body []byte) string {
	rc := ParseRequestContent(body)
	return rc.Model
}

// CountPromptFromRequest estimates prompt tokens from a parsed request.
func CountPromptFromRequest(rc RequestContent, providerType string) int {
	allText := strings.Join(rc.Texts, "\n")
	tokens := estimateTokens(providerFromModel(rc.Model), allText)

	// formatting overhead: ~3 tokens per message, ~8 per tool definition
	tokens += len(rc.Texts) * 3
	tokens += rc.Tools * 8
	tokens += 3 // base overhead

	if tokens < 1 {
		tokens = 1
	}
	return tokens
}
