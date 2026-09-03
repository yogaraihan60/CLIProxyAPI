package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestWithAntigravityQuotaRetryAfter_NonQuotaRateLimitPreservesRetryAfter is the
// edge case the user asked for: when the 429 is a short per-request rate limit
// (NOT quota depletion) the upstream body already carries a retry-after, and
// withAntigravityQuotaRetryAfter must NOT overwrite it with the quota-summary
// deadline. It also covers the related no-hint and non-depleted-model cases.
func TestWithAntigravityQuotaRetryAfter_NonQuotaRateLimitPreservesRetryAfter(t *testing.T) {
	cliproxyauth.SetAntigravityQuotaSkipEnabled(true)
	t.Cleanup(func() { cliproxyauth.SetAntigravityQuotaSkipEnabled(false) })
	e := NewAntigravityExecutor(&config.Config{})

	// Each subtest uses a unique auth ID so stored hints never bleed across cases.
	t.Run("existing short retryAfter is preserved", func(t *testing.T) {
		authID := "ag-edge-short-retry"
		auth := &cliproxyauth.Auth{ID: authID, Provider: "antigravity"}
		// Seed a depleted quota summary that would otherwise inject a long wait.
		farReset := time.Now().Add(2 * time.Hour)
		cliproxyauth.SetAntigravityQuotaSummary(authID, cliproxyauth.BuildAntigravityQuotaSummary([]cliproxyauth.AntigravityQuotaGroupHint{
			{
				Label:       "Claude and GPT models",
				Description: "Models within this group: Claude Sonnet",
				Buckets:     []cliproxyauth.AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: farReset}},
			},
		}, time.Now()))

		// A short non-quota rate limit carries its own retryAfter of 2s.
		shortWait := 2 * time.Second
		original := statusErr{code: http.StatusTooManyRequests, msg: "rate limited", retryAfter: &shortWait}
		result := e.withAntigravityQuotaRetryAfter(auth, "claude-sonnet-4-6", original)
		sErr, ok := result.(statusErr)
		if !ok {
			t.Fatalf("result is not statusErr: %T", result)
		}
		if sErr.retryAfter == nil || *sErr.retryAfter != shortWait {
			got := "nil"
			if sErr.retryAfter != nil {
				got = sErr.retryAfter.String()
			}
			t.Fatalf("non-quota retryAfter overwritten: got %s, want %s", got, shortWait)
		}
	})

	t.Run("no quota summary hint leaves error untouched", func(t *testing.T) {
		authID := "ag-edge-no-hint"
		auth := &cliproxyauth.Auth{ID: authID, Provider: "antigravity"}
		// Deliberately do NOT seed a hint.
		original := statusErr{code: http.StatusTooManyRequests, msg: "rate limited"}
		result := e.withAntigravityQuotaRetryAfter(auth, "claude-sonnet-4-6", original)
		sErr, ok := result.(statusErr)
		if !ok {
			t.Fatalf("result is not statusErr: %T", result)
		}
		if sErr.retryAfter != nil {
			t.Fatalf("error without hint should have nil retryAfter, got %v", *sErr.retryAfter)
		}
	})

	t.Run("model not depleted in hint leaves error untouched", func(t *testing.T) {
		authID := "ag-edge-not-depleted"
		auth := &cliproxyauth.Auth{ID: authID, Provider: "antigravity"}
		// Hint depletes Gemini Pro but the request is for claude-sonnet-4-6.
		cliproxyauth.SetAntigravityQuotaSummary(authID, cliproxyauth.BuildAntigravityQuotaSummary([]cliproxyauth.AntigravityQuotaGroupHint{
			{
				Label:       "Gemini Models",
				Description: "Models within this group: Gemini Pro",
				Buckets:     []cliproxyauth.AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: time.Now().Add(time.Hour)}},
			},
		}, time.Now()))

		original := statusErr{code: http.StatusTooManyRequests, msg: "rate limited"}
		result := e.withAntigravityQuotaRetryAfter(auth, "claude-sonnet-4-6", original)
		sErr, ok := result.(statusErr)
		if !ok {
			t.Fatalf("result is not statusErr: %T", result)
		}
		if sErr.retryAfter != nil {
			t.Fatalf("non-depleted model should have nil retryAfter, got %v", *sErr.retryAfter)
		}
	})

	t.Run("non-429 status is never touched", func(t *testing.T) {
		authID := "ag-edge-non-429"
		auth := &cliproxyauth.Auth{ID: authID, Provider: "antigravity"}
		cliproxyauth.SetAntigravityQuotaSummary(authID, cliproxyauth.BuildAntigravityQuotaSummary([]cliproxyauth.AntigravityQuotaGroupHint{
			{
				Label:       "Claude and GPT models",
				Description: "Models within this group: Claude Sonnet",
				Buckets:     []cliproxyauth.AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: time.Now().Add(time.Hour)}},
			},
		}, time.Now()))

		original := statusErr{code: http.StatusServiceUnavailable, msg: "upstream down"}
		result := e.withAntigravityQuotaRetryAfter(auth, "claude-sonnet-4-6", original)
		sErr, ok := result.(statusErr)
		if !ok {
			t.Fatalf("result is not statusErr: %T", result)
		}
		if sErr.retryAfter != nil {
			t.Fatalf("non-429 status should never get a retryAfter, got %v", *sErr.retryAfter)
		}
	})

	t.Run("feature disabled leaves error untouched", func(t *testing.T) {
		cliproxyauth.SetAntigravityQuotaSkipEnabled(false)
		t.Cleanup(func() { cliproxyauth.SetAntigravityQuotaSkipEnabled(true) })

		authID := "ag-edge-disabled"
		auth := &cliproxyauth.Auth{ID: authID, Provider: "antigravity"}
		cliproxyauth.SetAntigravityQuotaSummary(authID, cliproxyauth.BuildAntigravityQuotaSummary([]cliproxyauth.AntigravityQuotaGroupHint{
			{
				Label:       "Claude and GPT models",
				Description: "Models within this group: Claude Sonnet",
				Buckets:     []cliproxyauth.AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: time.Now().Add(time.Hour)}},
			},
		}, time.Now()))

		original := statusErr{code: http.StatusTooManyRequests, msg: "rate limited"}
		result := e.withAntigravityQuotaRetryAfter(auth, "claude-sonnet-4-6", original)
		sErr, ok := result.(statusErr)
		if !ok {
			t.Fatalf("result is not statusErr: %T", result)
		}
		if sErr.retryAfter != nil {
			t.Fatalf("feature disabled should keep retryAfter nil, got %v", *sErr.retryAfter)
		}
	})

	t.Run("depleted model gets precise deadline when no existing retryAfter", func(t *testing.T) {
		authID := "ag-edge-positive"
		auth := &cliproxyauth.Auth{ID: authID, Provider: "antigravity"}
		reset := time.Now().Add(90 * time.Minute)
		cliproxyauth.SetAntigravityQuotaSummary(authID, cliproxyauth.BuildAntigravityQuotaSummary([]cliproxyauth.AntigravityQuotaGroupHint{
			{
				Label:       "Claude and GPT models",
				Description: "Models within this group: Claude Sonnet",
				Buckets:     []cliproxyauth.AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: reset}},
			},
		}, time.Now()))

		original := statusErr{code: http.StatusTooManyRequests, msg: "quota exhausted"}
		result := e.withAntigravityQuotaRetryAfter(auth, "claude-sonnet-4-6", original)
		sErr, ok := result.(statusErr)
		if !ok {
			t.Fatalf("result is not statusErr: %T", result)
		}
		if sErr.retryAfter == nil {
			t.Fatal("depleted model with no existing retryAfter should get a precise deadline")
		}
		// The injected wait should be ~90 minutes, not a short backoff.
		if *sErr.retryAfter > 80*time.Minute {
			// ok
			return
		}
		t.Fatalf("injected retryAfter = %v, want ~90 minutes", *sErr.retryAfter)
	})
}

// TestMarkAntigravityQuotaExhausted_SyncRefresh verifies that the 429-triggered
// quota-summary refresh is synchronous: after markAntigravityQuotaExhausted
// returns, the hint is already updated (not pending in an async goroutine), so
// withAntigravityQuotaRetryAfter can read a fresh deadline on the very next call.
func TestMarkAntigravityQuotaExhausted_SyncRefresh(t *testing.T) {
	cliproxyauth.SetAntigravityQuotaSkipEnabled(true)
	t.Cleanup(func() { cliproxyauth.SetAntigravityQuotaSkipEnabled(false) })

	authID := "ag-sync-refresh"

	// Seed a stale hint that shows no depletion, so the sync refresh must
	// replace it with the real depletion from the mock upstream.
	cliproxyauth.SetAntigravityQuotaSummary(authID, cliproxyauth.AntigravityQuotaSummaryHint{
		ModelDepletion: map[string]time.Time{},
		UpdatedAt:      time.Now().Add(-1 * time.Hour),
	})

	reset := time.Now().Add(2 * time.Hour).Truncate(0) // strip monotonic for RFC3339 round-trip comparison
	quotaBody := `{"groups":[{"displayName":"Claude and GPT models","description":"Models within this group: Claude Sonnet","buckets":[{"bucketId":"b-weekly","remainingFraction":0,"resetTime":"` + reset.Format(time.RFC3339Nano) + `"}]}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "retrieveUserQuotaSummary") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(quotaBody))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	auth := &cliproxyauth.Auth{
		ID:         authID,
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": srv.URL},
		Metadata: map[string]any{
			"access_token": "test-token",
			"project_id":   "test-project",
		},
	}

	e := NewAntigravityExecutor(cfg)

	ctx := context.Background()
	e.markAntigravityQuotaExhausted(ctx, auth)

	// The hint must be updated synchronously — no goroutine to wait for.
	hint, ok := cliproxyauth.GetAntigravityQuotaSummary(authID)
	if !ok {
		t.Fatal("expected quota summary hint after sync refresh, got none")
	}
	deadline := hint.DepletionDeadline("claude-sonnet-4-6")
	if deadline.IsZero() {
		t.Fatal("expected non-zero depletion deadline for claude-sonnet-4-6 after sync refresh")
	}
	// Compare wall-clock time (RFC3339 round-trip drops the monotonic component).
	if !deadline.Equal(reset) {
		t.Fatalf("depletion deadline = %v, want %v (from mock upstream)", deadline, reset)
	}
}
