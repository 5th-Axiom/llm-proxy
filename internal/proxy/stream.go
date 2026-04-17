package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"llm-proxy/internal/tokencount"
)

func writeUpstreamResponse(w http.ResponseWriter, req *http.Request, resp *http.Response, tc *tokencount.TokenContext) error {
	copyResponseHeaders(w.Header(), resp.Header)

	streaming := isStreaming(req, resp)
	if streaming {
		w.Header().Set("X-Accel-Buffering", "no")
	}

	w.WriteHeader(resp.StatusCode)

	writer := io.Writer(w)
	if streaming {
		if flusher, ok := w.(http.Flusher); ok {
			fw := &flushWriter{writer: w, flusher: flusher}
			if tc != nil && tc.Enabled && tc.Parser != nil {
				writer = &tokenCountingWriter{writer: fw, parser: tc.Parser}
			} else {
				writer = fw
			}
		}
	}

	if tc != nil && tc.Enabled && !streaming {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		usage := tokencount.ParseNonStreamingUsage(bodyBytes)
		if usage.Found {
			tc.Counts.CompletionTokens = usage.CompletionTokens
			tc.Counts.TotalTokens = usage.TotalTokens
		} else {
			rc := tokencount.ParseRequestContent(nil)
			_ = rc
			tc.Counts.CompletionTokens = tokencount.EstimateCompletionTokens(tc.ProviderType, tc.Model, string(bodyBytes))
			tc.Counts.TotalTokens = tc.Counts.PromptTokens + tc.Counts.CompletionTokens
			tc.Counts.OutputEstimated = true
		}
		_, err = io.CopyBuffer(writer, bytes.NewReader(bodyBytes), make([]byte, 32*1024))
		return err
	}

	_, err := io.CopyBuffer(writer, resp.Body, make([]byte, 32*1024))
	if streaming {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if tc != nil && tc.Enabled && tc.Parser != nil {
			counts := tc.Parser.Finalize()
			tc.Counts.CompletionTokens = counts.CompletionTokens
			tc.Counts.TotalTokens = counts.TotalTokens
			tc.Counts.OutputEstimated = counts.OutputEstimated
		}
	}
	return err
}

func isStreaming(req *http.Request, resp *http.Response) bool {
	if strings.Contains(strings.ToLower(req.Header.Get("Accept")), "text/event-stream") {
		return true
	}
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

type flushWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

func (w *flushWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err == nil {
		w.flusher.Flush()
	}
	return n, err
}

type tokenCountingWriter struct {
	writer io.Writer
	parser *tokencount.StreamingUsageParser
}

func (w *tokenCountingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err != nil {
		return n, err
	}
	w.parser.ProcessChunk(p)
	return n, nil
}
