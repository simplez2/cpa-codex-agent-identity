// Package quota contains the narrowly-scoped compatibility policy used when
// stock CLIProxyAPI asks ChatGPT for Codex quota information.
package quota

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Operation identifies one explicitly supported quota request.
type Operation string

const (
	OperationUsage              Operation = "usage"
	OperationResetCreditDetails Operation = "reset_credit_details"
	OperationResetCreditConsume Operation = "reset_credit_consume"
)

// Target is a validated quota request resolved onto the configured upstream.
type Target struct {
	URL       *url.URL
	Operation Operation
}

type allowedRequest struct {
	method    string
	operation Operation
}

var allowedRequests = map[string]allowedRequest{
	"/backend-api/wham/usage": {
		method: http.MethodGet, operation: OperationUsage,
	},
	"/backend-api/wham/rate-limit-reset-credits": {
		method: http.MethodGet, operation: OperationResetCreditDetails,
	},
	"/backend-api/wham/rate-limit-reset-credits/consume": {
		method: http.MethodPost, operation: OperationResetCreditConsume,
	},
}

// Resolve accepts only the exact ChatGPT quota URLs and methods used by CPA.
// It rejects alternate hosts, ports, credentials, queries, and fragments so
// this management compatibility path cannot become an arbitrary HTTP proxy.
func Resolve(upstreamOrigin *url.URL, method, rawURL string) (Target, bool) {
	if upstreamOrigin == nil || upstreamOrigin.Scheme == "" || upstreamOrigin.Host == "" {
		return Target{}, false
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.User != nil || parsed.Opaque != "" || parsed.Port() != "" ||
		!strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "chatgpt.com") ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return Target{}, false
	}
	allowed, ok := allowedRequests[parsed.Path]
	if !ok || allowed.method != strings.ToUpper(strings.TrimSpace(method)) {
		return Target{}, false
	}
	target := *upstreamOrigin
	target.Path = parsed.Path
	target.RawPath = ""
	target.RawQuery = ""
	target.ForceQuery = false
	target.Fragment = ""
	return Target{URL: &target, Operation: allowed.operation}, true
}

// ResetCreditDetailsFromUsage returns the subset expected by CPA when the
// detailed endpoint is access-controlled but the usage summary is available.
func ResetCreditDetailsFromUsage(body []byte) ([]byte, bool) {
	var usage struct {
		RateLimitResetCredits *struct {
			AvailableCount           int64  `json:"available_count"`
			ApplicableAvailableCount *int64 `json:"applicable_available_count"`
		} `json:"rate_limit_reset_credits"`
	}
	if json.Unmarshal(body, &usage) != nil || usage.RateLimitResetCredits == nil ||
		usage.RateLimitResetCredits.AvailableCount < 0 ||
		(usage.RateLimitResetCredits.ApplicableAvailableCount != nil &&
			*usage.RateLimitResetCredits.ApplicableAvailableCount < 0) {
		return nil, false
	}
	result, err := json.Marshal(struct {
		Credits                  []any  `json:"credits"`
		AvailableCount           int64  `json:"available_count"`
		ApplicableAvailableCount *int64 `json:"applicable_available_count,omitempty"`
	}{
		// The official Codex app-server uses null when only the summary count is
		// known. An empty array would incorrectly claim that the detail endpoint
		// succeeded and returned no available credits.
		Credits:                  nil,
		AvailableCount:           usage.RateLimitResetCredits.AvailableCount,
		ApplicableAvailableCount: usage.RateLimitResetCredits.ApplicableAvailableCount,
	})
	return result, err == nil
}
