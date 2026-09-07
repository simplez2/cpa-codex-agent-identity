package cpa

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

func TestManagedCredentialVerifiesPersistedWebsockets(t *testing.T) {
	t.Parallel()
	m := &Manager{sidecarBaseURL: "http://sidecar:8787/backend-api/codex"}
	c := Credential{IdentityID: "agent-aabbccddeeff", Kind: "personal_access_token", UpstreamToken: "at-test-native", ClientKey: "cais_00000000000000000000000000000000"}
	for _, want := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			value   any
			missing bool
			valid   bool
			enabled bool
		}{
			{name: "false", value: false, valid: true},
			{name: "true", value: true, valid: true, enabled: true},
			{name: "string false", value: "false", valid: true},
			{name: "string true", value: " true ", valid: true, enabled: true},
			{name: "string zero", value: "0", valid: true},
			{name: "string one", value: "1", valid: true, enabled: true},
			{name: "missing", missing: true},
			{name: "null"},
			{name: "invalid string", value: "sometimes"},
			{name: "non-native yes", value: "yes"},
			{name: "numeric zero", value: 0},
			{name: "numeric one", value: 1},
		} {
			t.Run(tc.name+map[bool]string{false: "/want_off", true: "/want_on"}[want], func(t *testing.T) {
				raw, err := m.credentialJSONWithDisabled(c, false)
				if err != nil {
					t.Fatal(err)
				}
				expected, _ := decodeJSONMap(raw)
				expected["websockets"] = want
				expectedRaw, _ := json.Marshal(expected)
				actual, _ := decodeJSONMap(expectedRaw)
				if tc.missing {
					delete(actual, "websockets")
				} else {
					actual["websockets"] = tc.value
				}
				actualRaw, _ := json.Marshal(actual)
				if got := managedCredentialMatches(actualRaw, c, expectedRaw); got != (tc.valid && tc.enabled == want) {
					t.Fatalf("verification=%v: must require a persisted native-compatible websocket setting", got)
				}
			})
		}
	}
}

func TestUpsertPreservesWebsocketsAcrossRefreshAndManagerRestart(t *testing.T) {
	t.Parallel()
	for _, ws := range []any{false, true, "false", "true"} {
		t.Run(reflect.TypeOf(ws).String()+"/"+jsonStringValue(ws), func(t *testing.T) {
			var mu sync.Mutex
			files := map[string][]byte{}
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				name := r.URL.Query().Get("name")
				switch {
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
					items := []map[string]any{}
					for n := range files {
						items = append(items, map[string]any{"name": n})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"files": items})
				case r.URL.Path == "/v0/management/auth-files/download":
					if raw, ok := files[name]; ok {
						_, _ = w.Write(raw)
					} else {
						http.NotFound(w, r)
					}
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodPost:
					files[name], _ = io.ReadAll(r.Body)
					writes++
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				default:
					t.Errorf("unexpected management operation: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			newManager := func() *Manager {
				m, err := NewManager(server.URL+"/v0/management", "management-key", "http://sidecar:8787/backend-api/codex", server.Client())
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			c := Credential{IdentityID: "agent-aabbccddeeff", Kind: "personal_access_token", UpstreamToken: "at-test-original", ClientKey: "cais_00000000000000000000000000000000", AccountID: "team-a"}
			m := newManager()
			if err := m.UpsertIdentity(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			name, _ := authFileName(c)
			mu.Lock()
			prior, _ := decodeJSONMap(files[name])
			defaultWS := prior["websockets"]
			// Model the file persisted by native CPA field/status PATCH, not a
			// sidecar preference. New managers must reread that source of truth.
			prior["websockets"] = ws
			prior["disabled"] = true
			prior["proxy_url"] = "socks5://proxy.example:1080"
			prior["note"] = "keep user note"
			files[name], _ = json.Marshal(prior)
			mu.Unlock()
			if defaultWS != true {
				t.Fatal("new imports should retain the websocket-capable default")
			}
			for _, token := range []string{"at-test-refreshed", "at-test-refreshed-again"} {
				m = newManager() // no in-memory preference from a previous process
				c.UpstreamToken = token
				if err := m.UpsertIdentity(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				actual, _ := decodeJSONMap(files[name])
				before := writes
				mu.Unlock()
				if !reflect.DeepEqual(actual["websockets"], ws) || actual["disabled"] != true || actual["proxy_url"] != prior["proxy_url"] || actual["note"] != prior["note"] || actual["access_token"] != token {
					t.Fatal("refresh changed native settings or failed to replace the token")
				}
				if err := m.UpsertIdentity(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				after := writes
				mu.Unlock()
				if after != before {
					t.Fatal("unchanged sync rewrote the file; native session/cooldown state may be disrupted")
				}
			}
		})
	}
}
