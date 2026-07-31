package util

import (
	"net/http"
	"strings"
)

// ApplyCustomHeadersFromAttrs applies user-defined headers stored in the provided attributes map.
// Custom headers override built-in defaults when conflicts occur.
func ApplyCustomHeadersFromAttrs(r *http.Request, attrs map[string]string) {
	if r == nil {
		return
	}
	applyCustomHeaders(r, extractCustomHeaders(attrs))
}

func extractCustomHeaders(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	headers := make(map[string]string)
	for k, v := range attrs {
		if !strings.HasPrefix(k, "header:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(k, "header:"))
		if name == "" {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		headers[name] = val
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func applyCustomHeaders(r *http.Request, headers map[string]string) {
	if r == nil || len(headers) == 0 {
		return
	}
	for k, v := range headers {
		if k == "" || v == "" {
			continue
		}
		// net/http reads Host from req.Host (not req.Header) when writing
		// a real request, so we must mirror it there. Some callers pass
		// synthetic requests (e.g. &http.Request{Header: ...}) and only
		// consume r.Header afterwards, so keep the value in the header
		// map too.
		if http.CanonicalHeaderKey(k) == "Host" {
			r.Host = v
		}
		r.Header.Set(k, v)
	}
}

// forwardClientHeadersSkip lists headers that must not be forwarded from
// inbound client requests to upstream providers. These are hop-by-hop
// (RFC 7230 Section 6.1), security-sensitive, or CPA-managed headers.
var forwardClientHeadersSkip = map[string]struct{}{
	// Hop-by-hop (RFC 7230 Section 6.1)
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	// Security-sensitive / auth
	"Authorization": {},
	"Cookie":        {},
	"Set-Cookie":    {},
	// CPA-managed / transport
	"Content-Length":   {},
	"Content-Encoding": {},
	"Content-Type":     {},
	"Host":             {},
}

// ForwardClientHeaders copies inbound client request headers to the upstream
// request, skipping hop-by-hop, security-sensitive, and CPA-managed headers.
// Headers already set on the upstream request (by the executor or config) are
// preserved and not overwritten, so caller-managed headers always win.
func ForwardClientHeaders(upstream *http.Request, client http.Header) {
	if upstream == nil || len(client) == 0 {
		return
	}
	scoped := make(map[string]struct{})
	for _, rawValue := range client.Values("Connection") {
		for _, token := range strings.Split(rawValue, ",") {
			headerName := strings.TrimSpace(token)
			if headerName == "" {
				continue
			}
			scoped[http.CanonicalHeaderKey(headerName)] = struct{}{}
		}
	}
	for key, values := range client {
		canonicalKey := http.CanonicalHeaderKey(key)
		if _, skip := forwardClientHeadersSkip[canonicalKey]; skip {
			continue
		}
		if _, scopedHeader := scoped[canonicalKey]; scopedHeader {
			continue
		}
		// Don't overwrite headers already set by the executor or config.
		if upstream.Header.Get(key) != "" {
			continue
		}
		for _, v := range values {
			upstream.Header.Add(key, v)
		}
	}
}
