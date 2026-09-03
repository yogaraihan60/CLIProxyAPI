package util

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestApplyCustomHeadersFromAttrs_StaticHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
	attrs := map[string]string{
		"header:X-Custom-Static": "static-value",
		"header:Host":            "custom.host.com",
	}

	ApplyCustomHeadersFromAttrs(req, attrs)

	if got := req.Header.Get("X-Custom-Static"); got != "static-value" {
		t.Errorf("X-Custom-Static = %q, want %q", got, "static-value")
	}
	if got := req.Host; got != "custom.host.com" {
		t.Errorf("req.Host = %q, want %q", got, "custom.host.com")
	}
}

func TestApplyCustomHeadersFromAttrs_MagicVariable(t *testing.T) {
	t.Run("present in clientHeaders sets header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		attrs := map[string]string{
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Target-Session":         "$X-Claude-Code-Session-Id",
			"header:Static-Header":            "static-123",
		}
		clientHeaders := http.Header{
			"Abc":                      []string{"session-abc-456"},
			"X-Claude-Code-Session-Id": []string{"claude-code-uuid-789"},
		}

		ApplyCustomHeadersFromAttrs(req, attrs, clientHeaders)

		if got := req.Header.Get("X-Claude-Code-Session-Id"); got != "session-abc-456" {
			t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "session-abc-456")
		}
		if got := req.Header.Get("X-Target-Session"); got != "claude-code-uuid-789" {
			t.Errorf("X-Target-Session = %q, want %q", got, "claude-code-uuid-789")
		}
		if got := req.Header.Get("Static-Header"); got != "static-123" {
			t.Errorf("Static-Header = %q, want %q", got, "static-123")
		}
	})

	t.Run("absent in clientHeaders does not set header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		attrs := map[string]string{
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Other":                  "$NONEXISTENT",
			"header:Static-Header":            "static-123",
		}
		clientHeaders := http.Header{
			"Other-Header": []string{"some-value"},
		}

		ApplyCustomHeadersFromAttrs(req, attrs, clientHeaders)

		if _, exists := req.Header["X-Claude-Code-Session-Id"]; exists {
			t.Errorf("expected X-Claude-Code-Session-Id to be omitted when $ABC is absent in clientHeaders, got %q", req.Header.Get("X-Claude-Code-Session-Id"))
		}
		if _, exists := req.Header["X-Other"]; exists {
			t.Errorf("expected X-Other to be omitted when $NONEXISTENT is absent in clientHeaders, got %q", req.Header.Get("X-Other"))
		}
		if got := req.Header.Get("Static-Header"); got != "static-123" {
			t.Errorf("Static-Header = %q, want %q", got, "static-123")
		}
	})

	t.Run("nil clientHeaders does not set variable headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		attrs := map[string]string{
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:Static-Header":            "static-123",
		}

		ApplyCustomHeadersFromAttrs(req, attrs)

		if _, exists := req.Header["X-Claude-Code-Session-Id"]; exists {
			t.Errorf("expected X-Claude-Code-Session-Id to be omitted with nil clientHeaders, got %q", req.Header.Get("X-Claude-Code-Session-Id"))
		}
		if got := req.Header.Get("Static-Header"); got != "static-123" {
			t.Errorf("Static-Header = %q, want %q", got, "static-123")
		}
	})

	t.Run("fallback to gin context in request context", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(w)
		ginReq := httptest.NewRequest(http.MethodPost, "/", nil)
		ginReq.Header.Set("ABC", "from-gin-ctx-123")
		ginCtx.Request = ginReq

		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		req = req.WithContext(ginCtx)

		attrs := map[string]string{
			"header:X-Claude-Code-Session-Id": "$ABC",
		}

		ApplyCustomHeadersFromAttrs(req, attrs)

		if got := req.Header.Get("X-Claude-Code-Session-Id"); got != "from-gin-ctx-123" {
			t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "from-gin-ctx-123")
		}
	})
}

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
		"X-Api-Key":         []string{"cpa-inbound-key"},
		"X-Goog-Api-Key":    []string{"goog-inbound-key"},
		"Accept-Encoding":   []string{"br"},
		"X-Session-Hash":    []string{"abc123"},
	}
	ForwardClientHeaders(upstream, client)

	for _, blocked := range []string{"Connection", "Authorization", "Cookie", "Content-Length", "Content-Type", "Host", "Transfer-Encoding", "X-Api-Key", "X-Goog-Api-Key", "Accept-Encoding"} {
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
