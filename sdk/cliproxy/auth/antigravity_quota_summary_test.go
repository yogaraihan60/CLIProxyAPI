package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearAntigravityQuotaSummary(authID string) {
	antigravityQuotaSummaryByAuth.Delete(authID)
}

// TestBuildAntigravityQuotaSummary_MapsDepletedGroupsFromDescriptions verifies
// that depleted groups produce family-keyed depletion entries and non-depleted
// groups (any bucket still has quota) do not.
func TestBuildAntigravityQuotaSummary_MapsDepletedGroupsFromDescriptions(t *testing.T) {
	reset := time.Now().Add(48 * time.Hour)
	groups := []AntigravityQuotaGroupHint{
		{
			Label:       "Gemini Models",
			Description: "Models within this group: Gemini Flash, Gemini Pro",
			Buckets: []AntigravityQuotaBucketHint{
				{ID: "gemini-weekly", Window: "weekly", RemainingFraction: 0, ResetAt: reset},
			},
		},
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Opus, Claude Sonnet, GPT-OSS",
			Buckets: []AntigravityQuotaBucketHint{
				{ID: "claude-5h", Window: "5h", RemainingFraction: 0, ResetAt: reset.Add(-time.Hour)},
				{ID: "claude-weekly", Window: "weekly", RemainingFraction: 0.5, ResetAt: reset},
			},
		},
	}

	hint := BuildAntigravityQuotaSummary(groups, time.Now())

	// Gemini group is fully depleted → both families keyed.
	if deadline, ok := hint.ModelDepletion["gemini flash"]; !ok || !deadline.Equal(reset) {
		t.Fatalf("gemini flash depletion = %v (%t), want %v", deadline, ok, reset)
	}
	if deadline, ok := hint.ModelDepletion["gemini pro"]; !ok || !deadline.Equal(reset) {
		t.Fatalf("gemini pro depletion = %v (%t), want %v", deadline, ok, reset)
	}
	// Claude/GPT group is not depleted (weekly bucket still has 50%).
	if _, ok := hint.ModelDepletion["claude sonnet"]; ok {
		t.Fatal("claude sonnet should not be depleted")
	}
	if _, ok := hint.ModelDepletion["claude opus"]; ok {
		t.Fatal("claude opus should not be depleted")
	}
	if _, ok := hint.ModelDepletion["gpt-oss"]; ok {
		t.Fatal("gpt-oss should not be depleted")
	}
}

// TestBuildAntigravityQuotaSummary_UsesLatestResetAmongDepletedBuckets verifies
// that when all buckets are depleted, the deadline is the latest reset time.
func TestBuildAntigravityQuotaSummary_UsesLatestResetAmongDepletedBuckets(t *testing.T) {
	sooner := time.Now().Add(2 * time.Hour)
	later := time.Now().Add(24 * time.Hour)
	groups := []AntigravityQuotaGroupHint{
		{
			Label:       "Gemini Models",
			Description: "Models within this group: Gemini Flash",
			Buckets: []AntigravityQuotaBucketHint{
				{ID: "b-5h", Window: "5h", RemainingFraction: 0, ResetAt: sooner},
				{ID: "b-weekly", Window: "weekly", RemainingFraction: 0, ResetAt: later},
			},
		},
	}

	hint := BuildAntigravityQuotaSummary(groups, time.Now())

	if deadline := hint.DepletionDeadline("gemini-3-flash"); !deadline.Equal(later) {
		t.Fatalf("depletion deadline = %v, want %v", deadline, later)
	}
}

// TestBuildAntigravityQuotaSummary_UnknownFamilyNameIgnored verifies that
// upstream family names not in the family table are silently skipped rather
// than crashing or producing unmatched depletion entries.
func TestBuildAntigravityQuotaSummary_UnknownFamilyNameIgnored(t *testing.T) {
	reset := time.Now().Add(12 * time.Hour)
	groups := []AntigravityQuotaGroupHint{
		{
			Label:       "Experimental Models",
			Description: "Models within this group: Future Model X",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b", Window: "weekly", RemainingFraction: 0, ResetAt: reset}},
		},
	}

	hint := BuildAntigravityQuotaSummary(groups, time.Now())

	if len(hint.ModelDepletion) != 0 {
		t.Fatalf("unknown family should produce no depletion entries, got %d: %v", len(hint.ModelDepletion), hint.ModelDepletion)
	}
}

func TestAntigravityQuotaSummaryBlocked_SkipsDepletedAuth(t *testing.T) {
	SetAntigravityQuotaSkipEnabled(true)
	t.Cleanup(func() { SetAntigravityQuotaSkipEnabled(false) })
	auth := &Auth{ID: "ag-skip-1", Provider: "antigravity"}
	t.Cleanup(func() { clearAntigravityQuotaSummary(auth.ID) })
	reset := time.Now().Add(24 * time.Hour)
	SetAntigravityQuotaSummary(auth.ID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b", Window: "weekly", RemainingFraction: 0, ResetAt: reset}},
		},
	}, time.Now()))

	blocked, next := antigravityQuotaSummaryBlocked(auth, "claude-sonnet-4-6", time.Now())
	if !blocked || !next.Equal(reset) {
		t.Fatalf("blocked=%v next=%v, want blocked with deadline %v", blocked, next, reset)
	}

	blocked, _, _ = isAuthBlockedForModel(auth, "claude-sonnet-4-6", time.Now())
	if !blocked {
		t.Fatal("isAuthBlockedForModel should skip the depleted auth")
	}

	blocked, _ = antigravityQuotaSummaryBlocked(auth, "gemini-3-flash", time.Now())
	if blocked {
		t.Fatal("model outside the depleted family must stay selectable")
	}
}

func TestAntigravityQuotaSummaryBlocked_NotBlockedWhenFeatureDisabled(t *testing.T) {
	auth := &Auth{ID: "ag-skip-2", Provider: "antigravity"}
	t.Cleanup(func() {
		clearAntigravityQuotaSummary(auth.ID)
		SetAntigravityQuotaSkipEnabled(true)
	})
	SetAntigravityQuotaSkipEnabled(false)
	SetAntigravityQuotaSummary(auth.ID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: time.Now().Add(time.Hour)}},
		},
	}, time.Now()))

	blocked, _ := antigravityQuotaSummaryBlocked(auth, "claude-sonnet-4-6", time.Now())
	if blocked {
		t.Fatal("feature disabled must keep the auth selectable")
	}
}

func TestAntigravityQuotaSummaryBlocked_NotBlockedWhenCoolingDisabled(t *testing.T) {
	SetAntigravityQuotaSkipEnabled(true)
	t.Cleanup(func() {
		SetAntigravityQuotaSkipEnabled(false)
		clearAntigravityQuotaSummary("ag-skip-3")
	})
	auth := &Auth{
		ID:       "ag-skip-3",
		Provider: "antigravity",
		Metadata: map[string]any{"disable-cooling": true},
	}
	t.Cleanup(func() { clearAntigravityQuotaSummary(auth.ID) })
	SetAntigravityQuotaSummary(auth.ID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: time.Now().Add(time.Hour)}},
		},
	}, time.Now()))

	blocked, _ := antigravityQuotaSummaryBlocked(auth, "claude-sonnet-4-6", time.Now())
	if blocked {
		t.Fatal("cooling-disabled auth must stay selectable")
	}
}

func TestAntigravityQuotaSummaryBlocked_IgnoresOtherProvidersAndExpiredDeadlines(t *testing.T) {
	SetAntigravityQuotaSkipEnabled(true)
	t.Cleanup(func() { SetAntigravityQuotaSkipEnabled(false) })
	geminiAuth := &Auth{ID: "ag-skip-4", Provider: "gemini-cli"}
	t.Cleanup(func() { clearAntigravityQuotaSummary(geminiAuth.ID) })
	SetAntigravityQuotaSummary(geminiAuth.ID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: time.Now().Add(time.Hour)}},
		},
	}, time.Now()))

	if blocked, _ := antigravityQuotaSummaryBlocked(geminiAuth, "claude-sonnet-4-6", time.Now()); blocked {
		t.Fatal("non-antigravity provider must not consult the quota summary")
	}

	antigravityAuth := &Auth{ID: "ag-skip-5", Provider: "antigravity"}
	t.Cleanup(func() { clearAntigravityQuotaSummary(antigravityAuth.ID) })
	SetAntigravityQuotaSummary(antigravityAuth.ID, BuildAntigravityQuotaSummary([]AntigravityQuotaGroupHint{
		{
			Label:       "Claude and GPT models",
			Description: "Models within this group: Claude Sonnet",
			Buckets:     []AntigravityQuotaBucketHint{{ID: "b", RemainingFraction: 0, ResetAt: time.Now().Add(-time.Minute)}},
		},
	}, time.Now()))

	if blocked, _ := antigravityQuotaSummaryBlocked(antigravityAuth, "claude-sonnet-4-6", time.Now()); blocked {
		t.Fatal("expired deadline must not block the auth")
	}
}

// TestDepletionDeadline_GeminiFamilyMatching verifies that upstream family-level
// names like "Gemini Flash" and "Gemini Pro" correctly match versioned request
// models like "gemini-3-flash" and "gemini-3.1-pro-low" through the family table.
func TestDepletionDeadline_GeminiFamilyMatching(t *testing.T) {
	reset := time.Now().Add(24 * time.Hour)
	hint := AntigravityQuotaSummaryHint{
		ModelDepletion: map[string]time.Time{
			"gemini flash": reset,
			"gemini pro":   reset.Add(time.Hour),
		},
	}

	flashModels := []string{
		"gemini-3-flash", "gemini-3-flash-agent", "gemini-3.1-flash-image",
		"gemini-3.1-flash-lite", "gemini-3.5-flash-low", "gemini-3.5-flash-extra-low",
		"gemini-3.6-flash-high", "gemini-3.7-flash-high",
	}
	for _, model := range flashModels {
		if deadline := hint.DepletionDeadline(model); !deadline.Equal(reset) {
			t.Fatalf("flash model %q: deadline = %v, want %v", model, deadline, reset)
		}
	}

	proModels := []string{"gemini-pro-agent", "gemini-3.1-pro-low"}
	for _, model := range proModels {
		if deadline := hint.DepletionDeadline(model); !deadline.Equal(reset.Add(time.Hour)) {
			t.Fatalf("pro model %q: deadline = %v, want %v", model, deadline, reset.Add(time.Hour))
		}
	}

	nonGemini := []string{"grok-4.6", "gpt-5", "claude-sonnet-4-6", "gemini-3-image"}
	for _, model := range nonGemini {
		if deadline := hint.DepletionDeadline(model); !deadline.IsZero() {
			t.Fatalf("non-Gemini model %q matched depletion, got %v", model, deadline)
		}
	}
}

// TestAntigravityQuotaFamilyTableCompleteness verifies that every antigravity
// model in internal/registry/models/models.json maps to exactly one quota
// family. This catches new models that need to be added to the family table.
func TestAntigravityQuotaFamilyTableCompleteness(t *testing.T) {
	modelsJSON, errRead := os.ReadFile(filepath.Join("..", "..", "..", "internal", "registry", "models", "models.json"))
	if errRead != nil {
		t.Fatalf("read models.json: %v", errRead)
	}

	var parsed struct {
		Antigravity []struct {
			ID string `json:"id"`
		} `json:"antigravity"`
	}
	if errUnmarshal := json.Unmarshal(modelsJSON, &parsed); errUnmarshal != nil {
		t.Fatalf("parse models.json: %v", errUnmarshal)
	}

	if len(parsed.Antigravity) == 0 {
		t.Fatal("no antigravity models found in models.json")
	}

	for _, model := range parsed.Antigravity {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		modelKey := strings.ToLower(modelID)
		fam := familyForModel(modelKey)
		if fam == nil {
			t.Errorf("model %q does not match any quota family — add it to antigravityQuotaFamilies", modelID)
		}
	}
}
