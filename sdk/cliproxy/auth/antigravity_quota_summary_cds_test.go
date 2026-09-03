package auth

import (
	"context"
	"testing"
	"time"
)

// TestAntigravityQuotaSummaryCooldownRecord_SnapshotAndRestore verifies the full
// .cds round-trip for the antigravity quota summary hint: a depleted hint stored
// on an auth is captured by cooldownStateRecordsForAuthLocked, then restored via
// RestoreCooldownStates so the proactive skip survives restarts.
func TestAntigravityQuotaSummaryCooldownRecord_SnapshotAndRestore(t *testing.T) {
	SetAntigravityQuotaSkipEnabled(true)
	t.Cleanup(func() { SetAntigravityQuotaSkipEnabled(false) })
	authID := "ag-cds-roundtrip"
	t.Cleanup(func() { clearAntigravityQuotaSummary(authID) })

	reset := time.Now().Add(2 * time.Hour)
	hint := BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b-weekly", Window: "weekly", RemainingFraction: 0, ResetAt: reset}},
		},
	}, time.Now())
	SetAntigravityQuotaSummary(authID, hint)

	auth := &Auth{ID: authID, Provider: "antigravity", Status: StatusActive}

	// --- Snapshot: cooldownStateRecordsForAuthLocked must include the hint record ---
	now := time.Now()
	records := (&Manager{}).cooldownStateRecordsForAuthLocked(auth, now)

	var hintRecord *CooldownStateRecord
	for i := range records {
		if records[i].AntigravityQuotaSummary != nil {
			hintRecord = &records[i]
			break
		}
	}
	if hintRecord == nil {
		t.Fatal("expected a quota summary cooldown record, got none")
	}
	if hintRecord.Reason != "antigravity_quota_summary" {
		t.Fatalf("reason = %q, want %q", hintRecord.Reason, "antigravity_quota_summary")
	}
	if hintRecord.AntigravityQuotaSummary == nil {
		t.Fatal("hint record carries nil AntigravityQuotaSummary")
	}
	if deadline := hintRecord.AntigravityQuotaSummary.DepletionDeadline("claude-sonnet-4-6"); !deadline.Equal(reset) {
		t.Fatalf("restored hint depletion deadline = %v, want %v", deadline, reset)
	}

	// --- Restore: clear the in-memory hint, then rehydrate from the record ---
	clearAntigravityQuotaSummary(authID)
	if _, ok := GetAntigravityQuotaSummary(authID); ok {
		t.Fatal("hint should be cleared before restore")
	}

	store := &recordingCooldownStateStore{}
	store.load = []CooldownStateRecord{*hintRecord}

	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() error: %v", errRegister)
	}

	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() error: %v", errRestore)
	}

	restored, ok := GetAntigravityQuotaSummary(authID)
	if !ok {
		t.Fatal("hint should be restored after RestoreCooldownStates")
	}
	if deadline := restored.DepletionDeadline("claude-sonnet-4-6"); !deadline.Equal(reset) {
		t.Fatalf("restored depletion deadline = %v, want %v", deadline, reset)
	}

	// The restored hint should make the auth skip at selection time.
	if blocked, _ := antigravityQuotaSummaryBlocked(auth, "claude-sonnet-4-6", time.Now()); !blocked {
		t.Fatal("restored hint should block the depleted auth")
	}
}

// TestAntigravityQuotaSummaryCooldownRecord_StaleHintNotRestored ensures a hint
// whose UpdatedAt is older than the TTL window is dropped during restore, so
// stale persistence data does not override a fresh upstream refresh.
func TestAntigravityQuotaSummaryCooldownRecord_StaleHintNotRestored(t *testing.T) {
	authID := "ag-cds-stale"
	t.Cleanup(func() { clearAntigravityQuotaSummary(authID) })

	staleHint := AntigravityQuotaSummaryHint{
		ModelDepletion: map[string]time.Time{"claude sonnet": time.Now().Add(time.Hour)},
		UpdatedAt:      time.Now().Add(-(antigravityQuotaSummaryTTL + time.Minute)),
	}

	store := &recordingCooldownStateStore{}
	store.load = []CooldownStateRecord{
		{
			Provider:                "antigravity",
			AuthID:                  authID,
			NextRetryAfter:          time.Now().Add(time.Hour),
			Reason:                  "antigravity_quota_summary",
			AntigravityQuotaSummary: &staleHint,
		},
	}

	auth := &Auth{ID: authID, Provider: "antigravity", Status: StatusActive}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() error: %v", errRegister)
	}

	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() error: %v", errRestore)
	}

	if _, ok := GetAntigravityQuotaSummary(authID); ok {
		t.Fatal("stale hint should not be restored")
	}
}

// TestAntigravityQuotaSummaryCooldownRecord_NonAntigravityExcluded ensures
// non-antigravity auths never produce a quota summary cooldown record.
func TestAntigravityQuotaSummaryCooldownRecord_NonAntigravityExcluded(t *testing.T) {
	authID := "claude-cds-no-hint"
	t.Cleanup(func() { clearAntigravityQuotaSummary(authID) })

	// Even if a hint somehow exists for a non-antigravity auth, the snapshot
	// must not include it.
	SetAntigravityQuotaSummary(authID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: time.Now().Add(time.Hour)}},
		},
	}, time.Now()))

	auth := &Auth{ID: authID, Provider: "claude", Status: StatusActive}
	records := (&Manager{}).cooldownStateRecordsForAuthLocked(auth, time.Now())

	for _, record := range records {
		if record.AntigravityQuotaSummary != nil {
			t.Fatal("non-antigravity auth must not produce a quota summary cooldown record")
		}
	}
}

// TestAntigravityQuotaSummaryCooldownRecord_NoActiveDepletionExcluded ensures
// an antigravity auth whose hint has no future depletion deadline does not
// produce a cooldown record — empty/healthy hints should not be persisted.
func TestAntigravityQuotaSummaryCooldownRecord_NoActiveDepletionExcluded(t *testing.T) {
	authID := "ag-cds-healthy"
	t.Cleanup(func() { clearAntigravityQuotaSummary(authID) })

	// Hint with no depleted models (all buckets have quota remaining).
	SetAntigravityQuotaSummary(authID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0.8, ResetAt: time.Now().Add(time.Hour)}},
		},
	}, time.Now()))

	auth := &Auth{ID: authID, Provider: "antigravity", Status: StatusActive}
	records := (&Manager{}).cooldownStateRecordsForAuthLocked(auth, time.Now())

	for _, record := range records {
		if record.AntigravityQuotaSummary != nil {
			t.Fatal("auth with no active depletion must not produce a quota summary cooldown record")
		}
	}
}

// TestAntigravityQuotaSummaryEqual covers the equality helper used by the
// change-detection diff that decides whether to write .cds files.
func TestAntigravityQuotaSummaryEqual(t *testing.T) {
	now := time.Now()
	hint1 := &AntigravityQuotaSummaryHint{
		UpdatedAt:      now,
		ModelDepletion: map[string]time.Time{"claude-sonnet-4-5": now.Add(time.Hour)},
	}
	hint2 := &AntigravityQuotaSummaryHint{
		UpdatedAt:      now,
		ModelDepletion: map[string]time.Time{"claude-sonnet-4-5": now.Add(time.Hour)},
	}
	hintDifferent := &AntigravityQuotaSummaryHint{
		UpdatedAt:      now,
		ModelDepletion: map[string]time.Time{"claude sonnet": now.Add(2 * time.Hour)},
	}

	if !antigravityQuotaSummaryEqual(hint1, hint2) {
		t.Fatal("identical hints should be equal")
	}
	if antigravityQuotaSummaryEqual(hint1, hintDifferent) {
		t.Fatal("hints with different deadlines should not be equal")
	}
	if !antigravityQuotaSummaryEqual(nil, nil) {
		t.Fatal("nil hints should be equal")
	}
	if antigravityQuotaSummaryEqual(hint1, nil) {
		t.Fatal("non-nil and nil hints should not be equal")
	}
}
