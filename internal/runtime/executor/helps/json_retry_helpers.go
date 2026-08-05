package helps

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// DeleteJSONField removes a top-level or nested JSON field from a payload.
func DeleteJSONField(body []byte, key string) []byte {
	if key == "" || len(body) == 0 {
		return body
	}
	updated, err := sjson.DeleteBytes(body, key)
	if err != nil {
		return body
	}
	return updated
}

// ParseRetryDelay extracts the retry delay from a Google API 429 error response.
func ParseRetryDelay(errorBody []byte) (*time.Duration, error) {
	details := gjson.GetBytes(errorBody, "error.details")
	if details.Exists() && details.IsArray() {
		for _, detail := range details.Array() {
			if detail.Get("@type").String() != "type.googleapis.com/google.rpc.RetryInfo" {
				continue
			}
			retryDelay := detail.Get("retryDelay").String()
			if retryDelay == "" {
				continue
			}
			duration, err := time.ParseDuration(retryDelay)
			if err != nil {
				return nil, fmt.Errorf("failed to parse duration")
			}
			return &duration, nil
		}

		for _, detail := range details.Array() {
			if detail.Get("@type").String() != "type.googleapis.com/google.rpc.ErrorInfo" {
				continue
			}
			quotaResetDelay := detail.Get("metadata.quotaResetDelay").String()
			if quotaResetDelay == "" {
				continue
			}
			duration, err := time.ParseDuration(quotaResetDelay)
			if err == nil {
				return &duration, nil
			}
		}
	}

	message := gjson.GetBytes(errorBody, "error.message").String()
	if message != "" {
		re := regexp.MustCompile(`after\s+(\d+)s\.?`)
		if matches := re.FindStringSubmatch(message); len(matches) > 1 {
			seconds, err := strconv.Atoi(matches[1])
			if err == nil {
				duration := time.Duration(seconds) * time.Second
				return &duration, nil
			}
		}
		reHuman := regexp.MustCompile(`after\s+((?:\d+h)?(?:\d+m)?(?:\d+s)?)\.?`)
		if matches := reHuman.FindStringSubmatch(strings.ToLower(message)); len(matches) > 1 {
			duration, err := time.ParseDuration(matches[1])
			if err == nil && duration > 0 {
				return &duration, nil
			}
		}
	}

	// Plain-text 429 bodies (no JSON envelope). Antigravity/CloudCode returns
	// bodies like "Individual quota reached. ... Resets in 150h3m44s." — parse
	// the "Resets in <duration>" suffix so the cooldown matches the actual
	// upstream reset window instead of falling back to the short backoff cap.
	if duration, ok := parsePlainTextResetDelay(errorBody); ok {
		return &duration, nil
	}

	return nil, fmt.Errorf("no RetryInfo found")
}

// parsePlainTextResetDelay extracts a "Resets in <duration>" or "resets in <duration>"
// marker from a plain-text error body. Supports Go duration syntax (e.g. "150h3m44s",
// "30m", "2h30m") and the "Xh Ym Zs" space-separated variant.
func parsePlainTextResetDelay(body []byte) (time.Duration, bool) {
	if len(body) == 0 {
		return 0, false
	}
	lower := strings.ToLower(string(body))
	re := regexp.MustCompile(`resets?\s+in\s+((?:\d+[hms]\s*)+)`)
	matches := re.FindStringSubmatch(lower)
	if len(matches) < 2 {
		return 0, false
	}
	raw := strings.TrimSpace(matches[1])
	// Normalize space-separated components (e.g. "150h 3m 44s") into Go format.
	normalized := strings.Join(strings.Fields(raw), "")
	duration, err := time.ParseDuration(normalized)
	if err != nil || duration <= 0 {
		return 0, false
	}
	return duration, true
}
