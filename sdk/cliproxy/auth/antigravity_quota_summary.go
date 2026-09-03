package auth

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
)

const (
	// antigravityQuotaSummaryTTL bounds how long a stored quota summary hint stays
	// authoritative when no fresh upstream response refreshes it.
	antigravityQuotaSummaryTTL = 15 * time.Minute

	// antigravityQuotaDepletedEpsilon treats a remaining fraction at or below this
	// value as a fully depleted bucket. Only fully depleted buckets skip selection.
	antigravityQuotaDepletedEpsilon = 1e-4
)

// antigravityQuotaSkipEnabled stores the feature flag so the zero value keeps
// proactive skipping disabled until the server explicitly opts in via
// SetAntigravityQuotaSkipEnabled. This prevents the executor from firing
// quota-summary refresh requests in tests and other contexts that never load
// the config wiring.
var antigravityQuotaSkipEnabledFlag atomic.Bool

// SetAntigravityQuotaSkipEnabled toggles proactive skipping of antigravity auths
// whose upstream quota summary reports a fully depleted bucket for the requested
// model. Disabled until the server startup wiring calls this with true.
func SetAntigravityQuotaSkipEnabled(enabled bool) {
	antigravityQuotaSkipEnabledFlag.Store(enabled)
}

// AntigravityQuotaSkipEnabled reports the current runtime state of the proactive
// quota-skip flag. It is the single source of truth consulted by both the
// selection gate and the executor retry-after path, so hot-reload toggles apply
// everywhere consistently.
func AntigravityQuotaSkipEnabled() bool {
	return antigravityQuotaSkipEnabledFlag.Load()
}

// AntigravityQuotaBucketHint mirrors one bucket of the upstream quota summary.
type AntigravityQuotaBucketHint struct {
	ID                string    `json:"id"`
	Label             string    `json:"label,omitempty"`
	Window            string    `json:"window,omitempty"`
	RemainingFraction float64   `json:"remaining_fraction"`
	ResetAt           time.Time `json:"reset_at,omitempty"`
}

// AntigravityQuotaGroupHint mirrors one model group of the upstream quota summary.
type AntigravityQuotaGroupHint struct {
	Label       string                       `json:"label"`
	Description string                       `json:"description,omitempty"`
	Models      []string                     `json:"models,omitempty"`
	Buckets     []AntigravityQuotaBucketHint `json:"buckets"`
}

// AntigravityQuotaSummaryHint stores the latest known upstream quota summary for
// one auth. ModelDepletion maps a lowercase model key (plus "claude"/"gemini"
// heuristic markers) to the reset deadline of its depleted quota group.
type AntigravityQuotaSummaryHint struct {
	Groups         []AntigravityQuotaGroupHint `json:"groups"`
	ModelDepletion map[string]time.Time        `json:"model_depletion"`
	UpdatedAt      time.Time                   `json:"updated_at"`
}

var (
	antigravityQuotaSummaryByAuth sync.Map
	// antigravityQuotaGroupModelsRe extracts the model list from a group
	// description like "Models within this group: Gemini Flash, Gemini Pro".
	antigravityQuotaGroupModelsRe = regexp.MustCompile(`(?i)^models\s+within\s+this\s+group:\s*(.+)$`)
)

// antigravityQuotaFamily maps upstream quota group family names to the model ID
// patterns they cover. Upstream reports family-level names like "Gemini Flash"
// in the group description; the proxy needs to know which of its model IDs
// (e.g. "gemini-3-flash", "claude-sonnet-4-6") belong to each family so it can
// proactively skip depleted auths for the right models.
//
// UpstreamNames are the lowercased names as they appear in the description.
// Matchers are substrings that ALL must be present in the lowercased canonical
// model ID for the model to belong to the family. When a new model is added to
// internal/registry/models/models.json, verify it matches exactly one family
// (the completeness test in antigravity_quota_summary_test.go enforces this).
type antigravityQuotaFamily struct {
	upstreamNames []string
	matchers      []string
}

var antigravityQuotaFamilies = []antigravityQuotaFamily{
	{upstreamNames: []string{"gemini flash"}, matchers: []string{"gemini", "flash"}},
	{upstreamNames: []string{"gemini pro"}, matchers: []string{"gemini", "pro"}},
	{upstreamNames: []string{"claude opus"}, matchers: []string{"claude", "opus"}},
	{upstreamNames: []string{"claude sonnet"}, matchers: []string{"claude", "sonnet"}},
	{upstreamNames: []string{"gpt-oss"}, matchers: []string{"gpt-oss"}},
}

// familyForModel returns the quota family that the canonical model key belongs
// to, or nil if no family matches. A model matches when every matcher substring
// is present in the lowercased model key.
func familyForModel(modelKey string) *antigravityQuotaFamily {
	for i := range antigravityQuotaFamilies {
		if allSubstringsPresent(modelKey, antigravityQuotaFamilies[i].matchers) {
			return &antigravityQuotaFamilies[i]
		}
	}
	return nil
}

// familyForUpstreamName returns the quota family whose upstreamNames contain
// the given name (case-insensitive), or nil if no family matches.
func familyForUpstreamName(name string) *antigravityQuotaFamily {
	name = strings.ToLower(strings.TrimSpace(name))
	for i := range antigravityQuotaFamilies {
		for _, upstream := range antigravityQuotaFamilies[i].upstreamNames {
			if upstream == name {
				return &antigravityQuotaFamilies[i]
			}
		}
	}
	return nil
}

func allSubstringsPresent(haystack string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

// SetAntigravityQuotaSummary updates the latest known quota summary for an auth.
func SetAntigravityQuotaSummary(authID string, hint AntigravityQuotaSummaryHint) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if hint.UpdatedAt.IsZero() {
		hint.UpdatedAt = time.Now()
	}
	if _, homeMode, _ := homekv.CurrentKVClient(); homeMode {
		homekv.KVSetJSONBestEffort(context.Background(), antigravityQuotaSummaryKey(authID), hint, antigravityQuotaSummaryTTL)
		return
	}
	antigravityQuotaSummaryByAuth.Store(authID, hint)
}

// GetAntigravityQuotaSummary returns the latest known quota summary for an auth.
func GetAntigravityQuotaSummary(authID string) (AntigravityQuotaSummaryHint, bool) {
	hint, ok, err := GetAntigravityQuotaSummaryRequired(context.Background(), authID)
	if err == nil {
		return hint, ok
	}
	return AntigravityQuotaSummaryHint{}, false
}

// GetAntigravityQuotaSummaryRequired returns the latest known quota summary for request-time paths.
func GetAntigravityQuotaSummaryRequired(ctx context.Context, authID string) (AntigravityQuotaSummaryHint, bool, error) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return AntigravityQuotaSummaryHint{}, false, nil
	}
	var homeHint AntigravityQuotaSummaryHint
	homeMode, found, errGet := homekv.KVGetJSONRequired(ctx, antigravityQuotaSummaryKey(authID), &homeHint)
	if homeMode {
		return homeHint, found, errGet
	}
	value, ok := antigravityQuotaSummaryByAuth.Load(authID)
	if !ok {
		return AntigravityQuotaSummaryHint{}, false, nil
	}
	hint, ok := value.(AntigravityQuotaSummaryHint)
	if !ok {
		antigravityQuotaSummaryByAuth.Delete(authID)
		return AntigravityQuotaSummaryHint{}, false, nil
	}
	return hint, true, nil
}

// BuildAntigravityQuotaSummary derives per-family depletion deadlines from
// parsed upstream groups. A group is depleted when all buckets are exhausted;
// the deadline is the latest reset time among its depleted buckets. The
// ModelDepletion map is keyed by upstream family names (e.g. "gemini flash",
// "claude sonnet") resolved through the family table.
func BuildAntigravityQuotaSummary(groups []AntigravityQuotaGroupHint, now time.Time) AntigravityQuotaSummaryHint {
	hint := AntigravityQuotaSummaryHint{
		Groups:         groups,
		ModelDepletion: make(map[string]time.Time),
		UpdatedAt:      now,
	}
	if hint.UpdatedAt.IsZero() {
		hint.UpdatedAt = time.Now()
	}
	for _, group := range groups {
		// A group is only depleted when every bucket is exhausted; as long as any
		// bucket still holds quota, requests for its models can still be served.
		if len(group.Buckets) == 0 {
			continue
		}
		allDepleted := true
		var deadline time.Time
		for _, bucket := range group.Buckets {
			if bucket.RemainingFraction > antigravityQuotaDepletedEpsilon {
				allDepleted = false
				break
			}
			if bucket.ResetAt.After(deadline) {
				deadline = bucket.ResetAt
			}
		}
		if !allDepleted || deadline.IsZero() {
			continue
		}
		// Resolve each upstream family name from the group description to a
		// family entry and key the depletion map by the family's upstream names.
		for _, name := range antigravityQuotaGroupModels(group) {
			fam := familyForUpstreamName(name)
			if fam == nil {
				continue
			}
			for _, upstreamName := range fam.upstreamNames {
				if existing, ok := hint.ModelDepletion[upstreamName]; !ok || deadline.After(existing) {
					hint.ModelDepletion[upstreamName] = deadline
				}
			}
		}
	}
	return hint
}

// HintHasActiveDepletion reports whether the hint has at least one family whose
// depletion deadline is still in the future. Used by the cooldown-state snapshot
// path to avoid persisting empty or fully-expired hints.
func HintHasActiveDepletion(hint AntigravityQuotaSummaryHint, now time.Time) bool {
	for _, deadline := range hint.ModelDepletion {
		if !deadline.IsZero() && deadline.After(now) {
			return true
		}
	}
	return false
}

// DepletionDeadline reports when the model's quota bucket resets. Zero when the
// model is not known to be depleted. The request model is mapped to its quota
// family via the family table, then the family's upstream name is looked up in
// the ModelDepletion map.
func (h AntigravityQuotaSummaryHint) DepletionDeadline(model string) time.Time {
	modelKey := antigravityQuotaModelKey(model)
	if modelKey == "" {
		return time.Time{}
	}
	fam := familyForModel(modelKey)
	if fam == nil {
		return time.Time{}
	}
	for _, upstreamName := range fam.upstreamNames {
		if deadline, ok := h.ModelDepletion[upstreamName]; ok && !deadline.IsZero() {
			return deadline
		}
	}
	return time.Time{}
}

// antigravityQuotaSummaryBlocked reports whether selection should skip the auth
// for the model because the upstream quota summary marks its group fully depleted.
func antigravityQuotaSummaryBlocked(auth *Auth, model string, now time.Time) (bool, time.Time) {
	if !antigravityQuotaSkipEnabledFlag.Load() {
		return false, time.Time{}
	}
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
		return false, time.Time{}
	}
	modelKey := antigravityQuotaModelKey(model)
	if modelKey == "" {
		return false, time.Time{}
	}
	// Respect the cooling-disable precedence; when cooling is disabled the auth
	// must stay selectable exactly as before.
	if quotaCooldownDisabledForAuthWithConfig(auth, nil) {
		return false, time.Time{}
	}
	// Home owns cooldown state in Home mode; local instances must not block.
	if _, homeMode, _ := homekv.CurrentKVClient(); homeMode {
		return false, time.Time{}
	}
	hint, ok, errHint := GetAntigravityQuotaSummaryRequired(context.Background(), auth.ID)
	if errHint != nil || !ok {
		return false, time.Time{}
	}
	deadline := hint.DepletionDeadline(modelKey)
	if deadline.IsZero() || !deadline.After(now) {
		return false, time.Time{}
	}
	return true, deadline
}

func antigravityQuotaModelKey(model string) string {
	return strings.ToLower(canonicalModelKey(model))
}

// antigravityQuotaGroupModels extracts the upstream family names listed in a
// group's description (e.g. "Models within this group: Gemini Flash, Gemini
// Pro" → ["gemini flash", "gemini pro"]). The names are returned as-is for
// familyForUpstreamName to resolve; no model ID matching happens here.
func antigravityQuotaGroupModels(group AntigravityQuotaGroupHint) []string {
	if match := antigravityQuotaGroupModelsRe.FindStringSubmatch(strings.TrimSpace(group.Description)); match != nil {
		var names []string
		for _, raw := range strings.Split(match[1], ",") {
			name := strings.ToLower(strings.TrimSpace(raw))
			if name != "" {
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}

func antigravityQuotaSummaryKey(authID string) string {
	return "cpa:antigravity:quota-summary:" + strings.TrimSpace(authID)
}
