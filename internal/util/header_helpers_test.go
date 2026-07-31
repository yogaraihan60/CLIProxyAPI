package util

import (
	"net/http"
	"testing"
)

func TestForwardClientHeaders_CopiesAllowedHeaders(t *testing.T) {
	upstream := &http.Request{Header: http.Header{}}
	client := http.Header{
		"X-Session-Hash": []string{"abc123"},
		"X-Trace-Id":     []string{"trace-1"},
	}
	ForwardClientHeaders(upstream, client)

	if got := upstream.Header.Get("X-Session-Hash"); got != "abc123" {
		t.Fatalf("X-Session-Hash = %q, want %q", got, "abc123")
	}
	if got := upstream.Header.Get("X-Trace-Id"); got != "trace-1" {
		t.Fatalf("X-Trace-Id = %q, want %q", got, "trace-1")
	}
}

func TestForwardClientHeaders_SkipsHopByHopAndSensitive(t *testing.T) {
	upstream := &http.Request{Header: http.Header{}}
	client := http.Header{
		"Connection":        []string{"keep-alive"},
		"Authorization":     []string{"Bearer secret"},
		"Cookie":            []string{"session=xyz"},
		"Content-Length":    []string{"123"},
		"Content-Type":      []string{"application/json"},
		"Host":              []string{"example.com"},
		"Transfer-Encoding": []string{"chunked"},
		"X-Session-Hash":    []string{"abc123"},
	}
	ForwardClientHeaders(upstream, client)

	for _, blocked := range []string{"Connection", "Authorization", "Cookie", "Content-Length", "Content-Type", "Host", "Transfer-Encoding"} {
		if upstream.Header.Get(blocked) != "" {
			t.Fatalf("blocked header %q was forwarded: %q", blocked, upstream.Header.Get(blocked))
		}
	}
	if got := upstream.Header.Get("X-Session-Hash"); got != "abc123" {
		t.Fatalf("X-Session-Hash = %q, want %q", got, "abc123")
	}
}

func TestForwardClientHeaders_DoesNotOverwriteExecutorHeaders(t *testing.T) {
	upstream := &http.Request{Header: http.Header{}}
	upstream.Header.Set("Authorization", "Bearer upstream-key")
	upstream.Header.Set("User-Agent", "cli-proxy-openai-compat")
	client := http.Header{
		"Authorization":  []string{"Bearer client-key"},
		"User-Agent":     []string{"client-ua"},
		"X-Session-Hash": []string{"abc123"},
	}
	ForwardClientHeaders(upstream, client)

	if got := upstream.Header.Get("Authorization"); got != "Bearer upstream-key" {
		t.Fatalf("Authorization overwritten: %q, want %q", got, "Bearer upstream-key")
	}
	if got := upstream.Header.Get("User-Agent"); got != "cli-proxy-openai-compat" {
		t.Fatalf("User-Agent overwritten: %q, want %q", got, "cli-proxy-openai-compat")
	}
	if got := upstream.Header.Get("X-Session-Hash"); got != "abc123" {
		t.Fatalf("X-Session-Hash = %q, want %q", got, "abc123")
	}
}

func TestForwardClientHeaders_ConnectionScopedHeadersSkipped(t *testing.T) {
	upstream := &http.Request{Header: http.Header{}}
	client := http.Header{
		"Connection":   []string{"X-Custom-Hop"},
		"X-Custom-Hop": []string{"hop-value"},
		"X-Keep":       []string{"keep-value"},
	}
	ForwardClientHeaders(upstream, client)

	if upstream.Header.Get("X-Custom-Hop") != "" {
		t.Fatalf("Connection-scoped header X-Custom-Hop was forwarded: %q", upstream.Header.Get("X-Custom-Hop"))
	}
	if got := upstream.Header.Get("X-Keep"); got != "keep-value" {
		t.Fatalf("X-Keep = %q, want %q", got, "keep-value")
	}
}

func TestForwardClientHeaders_NilOrEmptyNoOp(t *testing.T) {
	ForwardClientHeaders(nil, http.Header{"X-Test": []string{"v"}})
	upstream := &http.Request{Header: http.Header{}}
	ForwardClientHeaders(upstream, nil)
	ForwardClientHeaders(upstream, http.Header{})
	if len(upstream.Header) != 0 {
		t.Fatalf("expected no headers, got %v", upstream.Header)
	}
}
