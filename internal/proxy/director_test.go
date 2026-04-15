package proxy

import "testing"

func TestBuildUpstreamURLWithoutVersionedBasePath(t *testing.T) {
	target, err := buildUpstreamURL("https://api.openai.com", "/v1/chat/completions", "stream=true")
	if err != nil {
		t.Fatalf("buildUpstreamURL() error = %v", err)
	}

	if got := target.String(); got != "https://api.openai.com/v1/chat/completions?stream=true" {
		t.Fatalf("target.String() = %q, want %q", got, "https://api.openai.com/v1/chat/completions?stream=true")
	}
}

func TestBuildUpstreamURLWithVersionedBasePath(t *testing.T) {
	target, err := buildUpstreamURL("https://api.openai.com/v1", "/v1/chat/completions", "")
	if err != nil {
		t.Fatalf("buildUpstreamURL() error = %v", err)
	}

	if got := target.String(); got != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("target.String() = %q, want %q", got, "https://api.openai.com/v1/chat/completions")
	}
}

func TestBuildUpstreamURLWithVendorSpecificVersionedBasePath(t *testing.T) {
	target, err := buildUpstreamURL("https://open.bigmodel.cn/api/coding/paas/v4", "/v1/chat/completions", "")
	if err != nil {
		t.Fatalf("buildUpstreamURL() error = %v", err)
	}

	if got := target.String(); got != "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions" {
		t.Fatalf("target.String() = %q, want %q", got, "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions")
	}
}
