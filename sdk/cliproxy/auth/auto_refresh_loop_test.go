package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type testRefreshEvaluator struct{}

func (testRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return false }

func setRefreshLeadFactory(t *testing.T, provider string, factory func() *time.Duration) {
	t.Helper()
	key := strings.ToLower(strings.TrimSpace(provider))
	refreshLeadMu.Lock()
	prev, hadPrev := refreshLeadFactories[key]
	if factory == nil {
		delete(refreshLeadFactories, key)
	} else {
		refreshLeadFactories[key] = factory
	}
	refreshLeadMu.Unlock()
	t.Cleanup(func() {
		refreshLeadMu.Lock()
		if hadPrev {
			refreshLeadFactories[key] = prev
		} else {
			delete(refreshLeadFactories, key)
		}
		refreshLeadMu.Unlock()
	})
}

func TestNextRefreshCheckAt_DisabledUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "disabled-schedule", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "a1",
		Provider: "disabled-schedule",
		Disabled: true,
		Status:   StatusDisabled,
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}

	// A disabled auth must never be scheduled for refresh, even when it has a
	// valid expiry and a configured refresh lead. This prevents the
	// auto-refresh loop from infinitely retrying refresh on dead credentials.
	if _, ok := nextRefreshCheckAt(now, auth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false for disabled auth")
	}
}

func TestNextRefreshCheckAt_APIKeyUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	auth := &Auth{ID: "a1", Provider: "test", Attributes: map[string]string{"api_key": "k"}}
	if _, ok := nextRefreshCheckAt(now, auth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false")
	}
}

func TestNextRefreshCheckAt_NextRefreshAfterGate(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	nextAfter := now.Add(30 * time.Minute)
	auth := &Auth{
		ID:               "a1",
		Provider:         "test",
		NextRefreshAfter: nextAfter,
		Metadata:         map[string]any{"email": "x@example.com"},
	}
	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	if !got.Equal(nextAfter) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, nextAfter)
	}
}

func TestNextRefreshCheckAt_PreferredInterval_PicksEarliestCandidate(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(20 * time.Minute)
	auth := &Auth{
		ID:              "a1",
		Provider:        "test",
		LastRefreshedAt: now,
		Metadata: map[string]any{
			"email":                    "x@example.com",
			"expires_at":               expiry.Format(time.RFC3339),
			"refresh_interval_seconds": 900, // 15m
		},
	}
	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-15 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_ProviderLead_Expiry(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "provider-lead-expiry", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "a1",
		Provider: "provider-lead-expiry",
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}

	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-lead)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_RefreshEvaluatorFallback(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	interval := 15 * time.Minute
	auth := &Auth{
		ID:       "a1",
		Provider: "test",
		Metadata: map[string]any{"email": "x@example.com"},
		Runtime:  testRefreshEvaluator{},
	}
	got, ok := nextRefreshCheckAt(now, auth, interval)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := now.Add(interval)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

// deletedAccountWorkerExecutor simulates a refresh that always fails with a
// permanent invalid_grant (deleted account). Used to verify the auto-refresh
// worker does not re-queue the auth after disabling it.
type deletedAccountWorkerExecutor struct {
	schedulerProviderTestExecutor
	mu        sync.Mutex
	callCount int
}

func (e *deletedAccountWorkerExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.callCount++
	e.mu.Unlock()
	return nil, errors.New(`bad response status code 400, message: {"error":"invalid_grant","error_description":"Account has been deleted"}`)
}

func (e *deletedAccountWorkerExecutor) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.callCount
}

func TestWorker_DoesNotRequeueDisabledAuth(t *testing.T) {
	exec := &deletedAccountWorkerExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
	}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "worker-disabled-loop",
		Provider: "antigravity",
		Metadata: map[string]any{
			"email": "deleted@example.com",
		},
	}
	ctx := context.Background()
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	loop := newAuthAutoRefreshLoop(manager, time.Second, 1)
	loopCtx, loopCancel := context.WithCancel(ctx)
	defer loopCancel()

	// Simulate what happens when the loop dispatches a refresh job for this
	// auth: the worker calls refreshAuth, which disables the auth, then the
	// worker must NOT re-queue it.
	loop.jobs <- auth.ID
	go loop.worker(loopCtx)

	// Give the worker time to process the job.
	time.Sleep(200 * time.Millisecond)

	// The auth should have been refreshed exactly once (the job we sent).
	if got := exec.calls(); got != 1 {
		t.Fatalf("Refresh called %d times, want 1", got)
	}

	// The auth must be disabled.
	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("expected auth to still exist")
	}
	if updated.Status != StatusDisabled {
		t.Fatalf("Status = %q, want %q", updated.Status, StatusDisabled)
	}

	// The dirty set must NOT contain the auth ID — if it does, applyDirty
	// would re-queue it and the loop would repeat forever.
	loop.mu.Lock()
	_, isDirty := loop.dirty[auth.ID]
	loop.mu.Unlock()
	if isDirty {
		t.Fatal("auth was re-queued to dirty set after being disabled; this causes an infinite refresh loop")
	}
}
