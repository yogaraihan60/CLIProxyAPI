package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadAntigravityAuthsFromDisk reads every antigravity-*.json file under the
// repository auths/ directory and returns runtime Auth values keyed by their
// stable ID, mirroring how the conductor materialises on-disk credentials: the
// on-disk "type" becomes Provider and the whole JSON payload is attached as
// Metadata so auth.ID derivation (email-based for antigravity OAuth) still
// works. The test repository root is resolved relative to this test file.
func loadAntigravityAuthsFromDisk(t *testing.T) map[string]*Auth {
	t.Helper()
	repoRoot := filepath.Join("..", "..", "..")
	authsDir := filepath.Join(repoRoot, "auths")
	entries, errReadDir := os.ReadDir(authsDir)
	if errReadDir != nil {
		t.Fatalf("read auths dir %s: %v", authsDir, errReadDir)
	}
	auths := make(map[string]*Auth)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "antigravity-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, errReadFile := os.ReadFile(filepath.Join(authsDir, entry.Name()))
		if errReadFile != nil {
			t.Fatalf("read auth file %s: %v", entry.Name(), errReadFile)
		}
		var metadata map[string]any
		if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
			t.Fatalf("unmarshal auth file %s: %v", entry.Name(), errUnmarshal)
		}
		provider, _ := metadata["type"].(string)
		provider = strings.TrimSpace(strings.ToLower(provider))
		if provider == "" {
			provider = "antigravity"
		}
		email, _ := metadata["email"].(string)
		email = strings.TrimSpace(email)
		id := email
		if id == "" {
			id = strings.TrimSuffix(entry.Name(), ".json")
		}
		auth := &Auth{
			ID:       id,
			Provider: provider,
			Metadata: metadata,
		}
		// The on-disk "disabled" flag maps to Auth.Disabled at load time.
		if rawDisabled, ok := metadata["disabled"].(bool); ok {
			auth.Disabled = rawDisabled
		}
		// Drop stale access tokens: isAuthBlockedForModel also blocks auths whose
		// access token expired, and on-disk tokens age out between server runs.
		// The quota-skip checks this file exercises are token-independent.
		delete(metadata, "access_token")
		auths[id] = auth
		t.Cleanup(func() { clearAntigravityQuotaSummary(id) })
	}
	if len(auths) == 0 {
		t.Fatal("no antigravity auth files found under auths/")
	}
	return auths
}

// TestAntigravityQuotaSummarySkip_RealAuthsDepleted verifies that real antigravity
// auths from auths/ are skipped at selection time once their stored quota summary
// reports the requested model's group as fully depleted, while a second real auth
// whose summary still has quota remains selectable. This exercises the same
// isAuthBlockedForModel -> antigravityQuotaSummaryBlocked path the conductor uses.
func TestAntigravityQuotaSummarySkip_RealAuthsDepleted(t *testing.T) {
	SetAntigravityQuotaSkipEnabled(true)
	t.Cleanup(func() { SetAntigravityQuotaSkipEnabled(false) })
	auths := loadAntigravityAuthsFromDisk(t)
	if len(auths) < 2 {
		t.Skip("need at least two antigravity auths on disk to compare skip vs. select")
	}

	// Pick two auths: deplete the first for Claude Sonnet, keep the second healthy.
	// Skip disabled auths for the healthy slot since disabled takes precedence over
	// quota-skip in isAuthBlockedForModel and would produce a false negative.
	var depleted, healthy *Auth
	for _, auth := range auths {
		if depleted == nil {
			depleted = auth
		} else if healthy == nil && !auth.Disabled {
			healthy = auth
			break
		}
	}
	if healthy == nil {
		t.Skip("need at least one enabled antigravity auth on disk for the healthy case")
	}

	reset := time.Now().Add(2 * time.Hour)
	SetAntigravityQuotaSummary(depleted.ID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet, Claude Opus",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b-weekly", Window: "weekly", RemainingFraction: 0, ResetAt: reset}},
		},
	}, time.Now()))

	// Healthy auth: group still holds quota, so it must stay selectable.
	SetAntigravityQuotaSummary(healthy.ID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b-weekly", Window: "weekly", RemainingFraction: 0.8, ResetAt: reset}},
		},
	}, time.Now()))

	now := time.Now()

	if blocked, next := antigravityQuotaSummaryBlocked(depleted, "claude-sonnet-4-6", now); !blocked || !next.Equal(reset) {
		t.Fatalf("depleted auth %q: blocked=%v next=%v, want blocked with %v", depleted.ID, blocked, next, reset)
	}
	if blocked, _, _ := isAuthBlockedForModel(depleted, "claude-sonnet-4-6", now); !blocked {
		t.Fatalf("isAuthBlockedForModel should skip depleted auth %q", depleted.ID)
	}

	if blocked, _ := antigravityQuotaSummaryBlocked(healthy, "claude-sonnet-4-6", now); blocked {
		t.Fatalf("healthy auth %q must stay selectable, got blocked", healthy.ID)
	}
	if blocked, _, _ := isAuthBlockedForModel(healthy, "claude-sonnet-4-6", now); blocked {
		t.Fatalf("isAuthBlockedForModel should keep healthy auth %q selectable", healthy.ID)
	}

	// A model outside the depleted group must not skip the depleted auth.
	if blocked, _ := antigravityQuotaSummaryBlocked(depleted, "gemini-2.5-pro", now); blocked {
		t.Fatal("depleted auth must stay selectable for a model outside its depleted group")
	}
}

// TestAntigravityQuotaSummarySkip_PartiallyDepletedGroupStaysSelectable guards the
// "all buckets depleted" semantics: when one bucket still holds quota, the group
// is NOT depleted and the auth must remain selectable. This is the edge case the
// BuildAntigravityQuotaSummary fix addressed (any-bucket vs all-buckets).
func TestAntigravityQuotaSummarySkip_PartiallyDepletedGroupStaysSelectable(t *testing.T) {
	SetAntigravityQuotaSkipEnabled(true)
	t.Cleanup(func() { SetAntigravityQuotaSkipEnabled(false) })
	auths := loadAntigravityAuthsFromDisk(t)
	var auth *Auth
	for _, a := range auths {
		auth = a
		break
	}
	reset := time.Now().Add(6 * time.Hour)
	SetAntigravityQuotaSummary(auth.ID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets: []AntigravityQuotaBucketHint{
				{ID: "b-5h", Window: "5h", RemainingFraction: 0, ResetAt: time.Now().Add(time.Hour)},
				{ID: "b-weekly", Window: "weekly", RemainingFraction: 0.5, ResetAt: reset},
			},
		},
	}, time.Now()))

	if blocked, _ := antigravityQuotaSummaryBlocked(auth, "claude-sonnet-4-6", time.Now()); blocked {
		t.Fatal("group with a non-depleted bucket must stay selectable")
	}
}

// TestAntigravityQuotaSummarySkip_DisabledAuthStaysBlocked ensures the quota-skip
// gate never overrides a disabled auth: auths/ contains a mix of disabled and
// enabled credentials, and a disabled auth must stay blocked regardless of any
// stored quota summary.
func TestAntigravityQuotaSummarySkip_DisabledAuthStaysBlocked(t *testing.T) {
	SetAntigravityQuotaSkipEnabled(true)
	t.Cleanup(func() { SetAntigravityQuotaSkipEnabled(false) })
	auths := loadAntigravityAuthsFromDisk(t)
	var disabled *Auth
	for _, auth := range auths {
		if rawDisabled, ok := auth.Metadata["disabled"].(bool); ok && rawDisabled {
			disabled = auth
			break
		}
	}
	if disabled == nil {
		t.Skip("no disabled antigravity auth on disk to exercise the disabled precedence")
	}
	// Seed a quota summary that would otherwise block the auth for a model.
	SetAntigravityQuotaSummary(disabled.ID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: time.Now().Add(time.Hour)}},
		},
	}, time.Now()))

	blocked, reason, _ := isAuthBlockedForModel(disabled, "claude-sonnet-4-6", time.Now())
	if !blocked {
		t.Fatal("disabled auth must stay blocked")
	}
	if reason != blockReasonDisabled {
		t.Fatalf("blocked reason = %v, want blockReasonDisabled", reason)
	}
}
