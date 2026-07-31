package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// TestOpenAICompatExecutorForwardsClientHeaders verifies that inbound client
// request headers (e.g. X-Session-Hash) are forwarded to the upstream provider
// for OpenAI-compatible (API-key-only) providers, while hop-by-hop and
// security-sensitive headers are stripped.
func TestOpenAICompatExecutorForwardsClientHeaders(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test-key",
	}}
	payload := []byte(`{"model":"custom-openai","messages":[{"role":"user","content":"hi"}]}`)
	clientHeaders := http.Header{
		"X-Session-Hash": []string{"session-abc-123"},
		"X-Trace-Id":     []string{"trace-9"},
		"Authorization":  []string{"Bearer client-secret"}, // must NOT override upstream auth
		"Cookie":         []string{"sess=xyz"},             // must be stripped
		"Content-Type":   []string{"text/plain"},           // must NOT override upstream
	}
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-openai",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
		Headers:      clientHeaders,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gotHeaders.Get("X-Session-Hash"); got != "session-abc-123" {
		t.Fatalf("X-Session-Hash forwarded = %q, want %q", got, "session-abc-123")
	}
	if got := gotHeaders.Get("X-Trace-Id"); got != "trace-9" {
		t.Fatalf("X-Trace-Id forwarded = %q, want %q", got, "trace-9")
	}
	// Security-sensitive headers must not be forwarded.
	if gotHeaders.Get("Cookie") != "" {
		t.Fatalf("Cookie must not be forwarded, got %q", gotHeaders.Get("Cookie"))
	}
	// Executor-managed Authorization must win over the client's.
	if got := gotHeaders.Get("Authorization"); got != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want %q (executor credential must win)", got, "Bearer test-key")
	}
	// Executor-managed Content-Type must win over the client's.
	if got := gotHeaders.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q (executor must win)", got, "application/json")
	}
}

// TestOpenAICompatExecutorConfigHeadersWinOverClientHeaders verifies that
// headers configured in the provider's `headers:` block take precedence over
// inbound client headers.
func TestOpenAICompatExecutorConfigHeadersWinOverClientHeaders(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":              server.URL + "/v1",
		"api_key":               "test-key",
		"header:X-Session-Hash": "config-value",
	}}
	payload := []byte(`{"model":"custom-openai","messages":[{"role":"user","content":"hi"}]}`)
	clientHeaders := http.Header{
		"X-Session-Hash": []string{"client-value"},
	}
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-openai",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
		Headers:      clientHeaders,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gotHeaders.Get("X-Session-Hash"); got != "config-value" {
		t.Fatalf("X-Session-Hash = %q, want %q (config headers must win)", got, "config-value")
	}
}
