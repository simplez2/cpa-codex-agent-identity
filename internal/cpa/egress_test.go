package cpa

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestProxyForIdentityReadsFreshAuthFile(t *testing.T) {
	var proxy atomic.Value
	proxy.Store("socks5://127.0.0.1:9")
	const id = "agent-abcdef"
	const name = "codex-agent-identity-abcdef.json"
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth-files":
			json.NewEncoder(w).Encode(map[string]any{"files": []any{map[string]string{"name": name, "auth_index": "idx"}}})
		case "/auth-files/download":
			if r.URL.Query().Get("name") != name {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"type": "codex", "auth_mode": "agent_identity_sidecar", "agent_identity_id": id, "proxy_url": proxy.Load()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()
	m, err := NewManager(service.URL, "test", "http://127.0.0.1:8787/backend-api/codex", service.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []string{"socks5://127.0.0.1:9", "direct", ""} {
		proxy.Store(next)
		got, err := m.ProxyForIdentity(t.Context(), id)
		if err != nil || got != next {
			t.Fatalf("routing mismatch: err=%v", err)
		}
	}
}
