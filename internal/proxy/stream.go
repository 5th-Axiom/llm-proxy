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
	var tcWriter *tokenCountingWriter
	if streaming {
		if flusher, ok := w.(http.Flusher); ok {
			fw := &flushWriter{writer: w, flusher: flusher}
			if tc != nil && tc.Enabled && tc.Parser != nil {
				tcWriter = &tokenCountingWriter{writer: fw, parser: tc.Parser}
				writer = tcWriter
			} else {
				writer = fw
			}
		}
	}

	if tc != nil && tc.Enabled && !streaming {
		var captured bytes.Buffer
		tee := io.TeeReader(resp.Body, &captured)
		if _, err := io.CopyBuffer(writer, tee, make([]byte, 32*1024)); err != nil {
			return err
		}
		bodyBytes := captured.Bytes()
		usage := tokencount.ParseNonStreamingUsage(bodyBytes)
		if usage.Found {
			// Trust upstream-reported prompt tokens over our request-body
			// estimate; different tokenizers (Qwen, Claude, etc.) drift from
			// tiktoken's cl100k_base by several tokens per message.
			if usage.PromptTokens > 0 {
				tc.Counts.PromptTokens = usage.PromptTokens
				tc.Counts.PromptEstimated = false
			}
			tc.Counts.CompletionTokens = usage.CompletionTokens
			tc.Counts.TotalTokens = tc.Counts.PromptTokens + tc.Counts.CompletionTokens
		} else {
			tc.Counts.CompletionTokens = tokencount.EstimateCompletionTokens(tc.ProviderType, tc.Model, string(bodyBytes))
			tc.Counts.TotalTokens = tc.Counts.PromptTokens + tc.Counts.CompletionTokens
			tc.Counts.OutputEstimated = true
		}
		return nil
	}

	_, err := io.CopyBuffer(writer, resp.Body, make([]byte, 32*1024))
	if streaming {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if tcWriter != nil {
			tcWriter.flushBuffer()
		}
		if tc != nil && tc.Enabled && tc.Parser != nil {
			counts := tc.Parser.Finalize()
			tc.Counts.CompletionTokens = counts.CompletionTokens
			tc.Counts.OutputEstimated = counts.OutputEstimated
			// If the upstream reported prompt tokens in the SSE stream, trust
			// that over our request-body estimate; otherwise keep the
			// body-parse estimate we already populated upfront.
			if counts.PromptTokens > 0 {
				tc.Counts.PromptTokens = counts.PromptTokens
			}
			tc.Counts.TotalTokens = tc.Counts.PromptTokens + tc.Counts.CompletionTokens
		}
	}
	return err
}

func isStreaming(req *http.Request, resp *http.Response) bool {
	if clientAcceptsStream(req) {
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

// tokenCountingWriter forwards bytes verbatim to the client and, in parallel,
// splits the stream into SSE lines before handing them to the usage parser.
// Upstream reads arrive in arbitrary 32KB chunks that can split an event or
// pack many events together, so we must re-line-break before parsing.
type tokenCountingWriter struct {
	writer io.Writer
	parser *tokencount.StreamingUsageParser
	buf    []byte
}

func (w *tokenCountingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err != nil {
		return n, err
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) > 0 {
			w.parser.ProcessChunk(line)
		}
		w.buf = w.buf[i+1:]
	}
	return n, nil
}

// flushBuffer processes any trailing bytes that did not end with a newline.
// SSE streams usually terminate with \n\n, but upstreams can close mid-line;
// we still want to feed whatever we have to the parser.
func (w *tokenCountingWriter) flushBuffer() {
	if len(w.buf) == 0 {
		return
	}
	w.parser.ProcessChunk(w.buf)
	w.buf = w.buf[:0]
}
