package proxy

import (
	"io"
	"net/http"
	"strings"
)

func writeUpstreamResponse(w http.ResponseWriter, req *http.Request, resp *http.Response) error {
	copyResponseHeaders(w.Header(), resp.Header)

	streaming := isStreaming(req, resp)
	if streaming {
		w.Header().Set("X-Accel-Buffering", "no")
	}

	w.WriteHeader(resp.StatusCode)

	writer := io.Writer(w)
	if streaming {
		if flusher, ok := w.(http.Flusher); ok {
			writer = &flushWriter{writer: w, flusher: flusher}
		}
	}

	_, err := io.CopyBuffer(writer, resp.Body, make([]byte, 32*1024))
	if streaming {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
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
