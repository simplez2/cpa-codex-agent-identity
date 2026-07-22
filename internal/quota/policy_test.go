package quota

import (
	"net/http"
	"net/url"
	"testing"
)

func TestResolveAllowsOnlyExactQuotaRequests(t *testing.T) {
	userInfoURL := "https://placeholder" + "@chatgpt.com/backend-api/wham/usage"
	upstream, err := url.Parse("https://upstream.example/base")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		method    string
		rawURL    string
		wantOK    bool
		wantPath  string
		operation Operation
	}{
		{"usage", http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", true, "/backend-api/wham/usage", OperationUsage},
		{"details", http.MethodGet, "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits", true, "/backend-api/wham/rate-limit-reset-credits", OperationResetCreditDetails},
		{"consume preserved", http.MethodPost, "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume", true, "/backend-api/wham/rate-limit-reset-credits/consume", OperationResetCreditConsume},
		{"wrong method", http.MethodGet, "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume", false, "", ""},
		{"other host", http.MethodGet, "https://example.com/backend-api/wham/usage", false, "", ""},
		{"host suffix", http.MethodGet, "https://chatgpt.com.example/backend-api/wham/usage", false, "", ""},
		{"userinfo", http.MethodGet, userInfoURL, false, "", ""},
		{"port", http.MethodGet, "https://chatgpt.com:443/backend-api/wham/usage", false, "", ""},
		{"query", http.MethodGet, "https://chatgpt.com/backend-api/wham/usage?a=b", false, "", ""},
		{"fragment", http.MethodGet, "https://chatgpt.com/backend-api/wham/usage#x", false, "", ""},
		{"unknown path", http.MethodGet, "https://chatgpt.com/backend-api/accounts", false, "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, ok := Resolve(upstream, test.method, test.rawURL)
			if ok != test.wantOK {
				t.Fatalf("Resolve() ok=%v, want %v", ok, test.wantOK)
			}
			if !ok {
				return
			}
			if target.Operation != test.operation || target.URL.Host != upstream.Host || target.URL.Path != test.wantPath {
				t.Fatalf("unexpected target: %#v", target)
			}
		})
	}
}

func TestResetCreditDetailsFromUsage(t *testing.T) {
	result, ok := ResetCreditDetailsFromUsage([]byte(`{"rate_limit_reset_credits":{"available_count":1,"applicable_available_count":0}}`))
	if !ok || string(result) != `{"credits":null,"available_count":1,"applicable_available_count":0}` {
		t.Fatalf("result=%s ok=%v", result, ok)
	}
	result, ok = ResetCreditDetailsFromUsage([]byte(`{"rate_limit_reset_credits":{"available_count":2}}`))
	if !ok || string(result) != `{"credits":null,"available_count":2}` {
		t.Fatalf("result=%s ok=%v", result, ok)
	}
	for _, body := range []string{
		`{}`,
		`{"rate_limit_reset_credits":null}`,
		`{"rate_limit_reset_credits":{"available_count":-1}}`,
		`{"rate_limit_reset_credits":{"available_count":1,"applicable_available_count":-1}}`,
		`not-json`,
	} {
		if _, ok := ResetCreditDetailsFromUsage([]byte(body)); ok {
			t.Fatalf("unexpected success for %q", body)
		}
	}
}
