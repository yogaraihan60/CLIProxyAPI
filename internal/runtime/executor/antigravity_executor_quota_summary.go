package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	antigravityQuotaSummaryPath = "/v1internal:retrieveUserQuotaSummary"

	antigravityQuotaSummaryRefreshInterval = 10 * time.Minute
	antigravityQuotaSummaryRefreshTimeout  = 5 * time.Second
)

var antigravityQuotaHintRefreshByID sync.Map // auth.ID → *antigravityCreditsHintRefreshState

// antigravityQuotaSkipEnabled reports whether proactive quota-summary skipping is
// enabled. It honors both the config struct (startup value) and the runtime flag
// (hot-reload value), so a runtime disable applies to the executor paths too.
func antigravityQuotaSkipEnabled(cfg *config.Config) bool {
	if !cliproxyauth.AntigravityQuotaSkipEnabled() {
		return false
	}
	return cfg == nil || cfg.QuotaExceeded.AntigravityQuotaSkipEnabled()
}

// maybeRefreshAntigravityQuotaSummary refreshes the upstream quota summary hint for
// the auth at most once per refresh interval, asynchronously. Unlike the credits
// hint, quota buckets deplete and recover over time, so an existing hint does not
// stop the periodic refresh.
func (e *AntigravityExecutor) maybeRefreshAntigravityQuotaSummary(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) {
	if e == nil || auth == nil || !antigravityQuotaSkipEnabled(e.cfg) || antigravityCoolingDisabled(auth, e.cfg) {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}
	if strings.TrimSpace(accessToken) == "" {
		accessToken = metaStringValue(auth.Metadata, "access_token")
	}
	if strings.TrimSpace(accessToken) == "" {
		return
	}

	if client, homeMode, errClient := currentAntigravityKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("antigravity executor: home kv quota summary refresh lock failed prefix=cpa:antigravity:*: %v", errClient)
			return
		}
		written, errSetNX := client.KVSetNX(context.Background(), antigravityQuotaSummaryRefreshLockKey(authID), []byte("1"), antigravityQuotaSummaryRefreshInterval)
		if errSetNX != nil {
			log.Errorf("antigravity executor: home kv quota summary refresh lock failed prefix=cpa:antigravity:*: %v", errSetNX)
			return
		}
		if !written {
			return
		}
		refreshCtx, cancel := context.WithTimeout(antigravityAuxRefreshContext(ctx), antigravityQuotaSummaryRefreshTimeout)
		authCopy := auth.Clone()
		go func(auth *cliproxyauth.Auth, token string) {
			defer cancel()
			e.updateAntigravityQuotaSummary(refreshCtx, auth, token)
		}(authCopy, accessToken)
		return
	}

	state := &antigravityCreditsHintRefreshState{}
	if existing, loaded := antigravityQuotaHintRefreshByID.LoadOrStore(authID, state); loaded {
		if cast, ok := existing.(*antigravityCreditsHintRefreshState); ok && cast != nil {
			state = cast
		} else {
			antigravityQuotaHintRefreshByID.Delete(authID)
			antigravityQuotaHintRefreshByID.Store(authID, state)
		}
	}

	now := time.Now()
	if !state.mu.TryLock() {
		return
	}
	if !state.lastAttempt.IsZero() && now.Sub(state.lastAttempt) < antigravityQuotaSummaryRefreshInterval {
		state.mu.Unlock()
		return
	}
	state.lastAttempt = now

	refreshCtx, cancel := context.WithTimeout(antigravityAuxRefreshContext(ctx), antigravityQuotaSummaryRefreshTimeout)
	authCopy := auth.Clone()

	go func(state *antigravityCreditsHintRefreshState, auth *cliproxyauth.Auth, token string) {
		defer state.mu.Unlock()
		defer cancel()
		e.updateAntigravityQuotaSummary(refreshCtx, auth, token)
	}(state, authCopy, accessToken)
}

// antigravityAuxRefreshContext builds a background context for auxiliary metadata
// fetches, propagating the request round-tripper when present.
func antigravityAuxRefreshContext(ctx context.Context) context.Context {
	base := context.Background()
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			base = context.WithValue(base, "cliproxy.roundtripper", rt)
		}
	}
	return base
}

// updateAntigravityQuotaSummary fetches the upstream quota summary for the auth and
// stores the derived depletion deadlines as a selection hint.
func (e *AntigravityExecutor) updateAntigravityQuotaSummary(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}
	token := strings.TrimSpace(accessToken)
	if token == "" {
		token = metaStringValue(auth.Metadata, "access_token")
	}
	if token == "" {
		return
	}
	projectID := antigravityProjectIDFromAuth(auth)
	if projectID == "" {
		return
	}

	body, errMarshal := json.Marshal(map[string]any{"project": projectID})
	if errMarshal != nil {
		log.Debugf("antigravity executor: marshal quota summary request error: %v", errMarshal)
		return
	}

	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	baseURL := resolveAntigravityRequestBaseURL(auth)
	endpointURL := strings.TrimSuffix(baseURL, "/") + antigravityQuotaSummaryPath
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if errReq != nil {
		log.Debugf("antigravity executor: quota summary request build error: %v", errReq)
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))

	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		log.Debugf("antigravity executor: quota summary request error on %s: %v", baseURL, errDo)
		return
	}
	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errClose := httpResp.Body.Close(); errClose != nil {
		log.Errorf("antigravity executor: close quota summary response body error: %v", errClose)
	}
	if errRead != nil || httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		log.Debugf("antigravity executor: quota summary returned status %d, err=%v", httpResp.StatusCode, errRead)
		return
	}

	groups := parseAntigravityQuotaGroups(bodyBytes)
	if len(groups) == 0 {
		// Keep any previously stored hint rather than replacing it with an empty one.
		return
	}
	cliproxyauth.SetAntigravityQuotaSummary(auth.ID, cliproxyauth.BuildAntigravityQuotaSummary(groups, time.Now()))
}

// markAntigravityQuotaExhausted reacts to a fully exhausted upstream quota bucket by
// refreshing the quota summary hint synchronously. The request already failed with
// a 429, so blocking ~1s for the quota API call is negligible and lets the caller
// (withAntigravityQuotaRetryAfter) read a fresh hint with the exact bucket reset
// deadline instead of a stale one. This bypasses the 10-minute rate limit that
// gates the periodic proactive refresh — error-triggered refreshes always fire.
func (e *AntigravityExecutor) markAntigravityQuotaExhausted(ctx context.Context, auth *cliproxyauth.Auth) {
	if e == nil || auth == nil || !antigravityQuotaSkipEnabled(e.cfg) || antigravityCoolingDisabled(auth, e.cfg) {
		return
	}
	refreshCtx, cancel := context.WithTimeout(antigravityAuxRefreshContext(ctx), 3*time.Second)
	defer cancel()
	e.updateAntigravityQuotaSummary(refreshCtx, auth, "")
}

// withAntigravityQuotaRetryAfter attaches the true bucket reset deadline from the
// quota summary hint to a 429 status error, so the conductor schedules a precise
// cooldown instead of the exponential backoff ladder.
func (e *AntigravityExecutor) withAntigravityQuotaRetryAfter(auth *cliproxyauth.Auth, model string, err error) error {
	sErr, ok := err.(statusErr)
	if !ok || sErr.code != http.StatusTooManyRequests || sErr.retryAfter != nil || auth == nil {
		return err
	}
	if e != nil && !antigravityQuotaSkipEnabled(e.cfg) {
		return err
	}
	hint, found := cliproxyauth.GetAntigravityQuotaSummary(auth.ID)
	if !found {
		return err
	}
	deadline := hint.DepletionDeadline(model)
	if deadline.IsZero() || !deadline.After(time.Now()) {
		return err
	}
	wait := time.Until(deadline)
	sErr.retryAfter = &wait
	return sErr
}

func parseAntigravityQuotaGroups(body []byte) []cliproxyauth.AntigravityQuotaGroupHint {
	groups := gjson.GetBytes(body, "groups")
	if !groups.IsArray() {
		return nil
	}
	parsed := make([]cliproxyauth.AntigravityQuotaGroupHint, 0, len(groups.Array()))
	for _, group := range groups.Array() {
		groupHint := cliproxyauth.AntigravityQuotaGroupHint{
			Label:       strings.TrimSpace(group.Get("displayName").String()),
			Description: strings.TrimSpace(group.Get("description").String()),
		}
		buckets := group.Get("buckets")
		if buckets.IsArray() {
			for _, bucket := range buckets.Array() {
				resetAt := time.Time{}
				if raw := strings.TrimSpace(bucket.Get("resetTime").String()); raw != "" {
					if parsedTime, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
						resetAt = parsedTime
					}
				}
				groupHint.Buckets = append(groupHint.Buckets, cliproxyauth.AntigravityQuotaBucketHint{
					ID:                strings.TrimSpace(bucket.Get("bucketId").String()),
					Label:             strings.TrimSpace(bucket.Get("displayName").String()),
					Window:            strings.TrimSpace(bucket.Get("window").String()),
					RemainingFraction: bucket.Get("remainingFraction").Float(),
					ResetAt:           resetAt,
				})
			}
		}
		if len(groupHint.Buckets) == 0 {
			continue
		}
		parsed = append(parsed, groupHint)
	}
	return parsed
}

func antigravityQuotaSummaryRefreshLockKey(authID string) string {
	return "cpa:antigravity:quota-summary-refresh-lock:" + strings.TrimSpace(authID)
}
