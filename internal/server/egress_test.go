package server

import (
	"net/http/httptest"
	"testing"

	"github.com/simplez2/cpa-codex-agent-identity/internal/egress"
)

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
