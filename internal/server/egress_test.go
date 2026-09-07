package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/simplez2/cpa-codex-agent-identity/internal/egress"
)

func TestQuotaJSONProxySurvivesCustomDecoder(t *testing.T) {
	for _, key := range []string{"proxy_url", "proxyUrl", "proxy-url"} {
		var call cpaAPICallRequest
		if err := json.Unmarshal([]byte(`{"auth_index":"test","`+key+`":"socks5://127.0.0.1:1"}`), &call); err != nil {
			t.Fatal(err)
		}
		s := &Server{config: Config{EnforceIdentityProxy: true}}
		r, err := s.withIdentityProxy(httptest.NewRequest("POST", "/", nil), "agent-abcdef", call.ProxyURL)
		if err != nil || egress.Proxy(r.Context()) != "socks5://127.0.0.1:1" {
			t.Fatal("JSON proxy override was lost")
		}
	}
	var call cpaAPICallRequest
	if json.Unmarshal([]byte(`{"proxy_url":42}`), &call) == nil {
		t.Fatal("invalid proxy type accepted")
	}
}

func TestProxyResolutionFailsClosedWithoutCPA(t *testing.T) {
	s := &Server{config: Config{EnforceIdentityProxy: true}}
	r := httptest.NewRequest("GET", "/", nil)
	if _, err := s.withIdentityProxy(r, "agent-abcdef", ""); err == nil {
		t.Fatal("missing CPA allowed implicit direct")
	}
	for _, explicit := range []string{"socks5://127.0.0.1:9", "direct"} {
		next, err := s.withIdentityProxy(r, "agent-abcdef", explicit)
		if err != nil || egress.Proxy(next.Context()) != explicit {
			t.Fatal("management proxy override lost")
		}
	}
}
