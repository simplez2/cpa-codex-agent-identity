package cpa

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestNativePATMigrationPreservesNativeSettings(t *testing.T) {
	t.Parallel()
	m, err := NewManager("https://cpa.example/v0/management", "management-key", "http://sidecar:8787/backend-api/codex", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	c := Credential{IdentityID: "agent-aabbccddeeff", Kind: "personal_access_token", UpstreamToken: "at-test-native", ClientKey: "cais_00000000000000000000000000000000", AccountID: "team-a"}
	next, err := m.credentialJSONWithDisabled(c, true)
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := decodeJSONMap(next)
	prior["type"] = pluginProviderID
	prior["runtime_only"] = true
	prior["base_url"] = "http://sidecar:8787/backend-api/codex"
	prior["expired"] = "2020-01-01T00:00:00Z"
	settings := map[string]any{
		"disabled": true, "proxy_url": "socks5://proxy.example:1080", "priority": json.Number("3"),
		"prefix": "team-a", "note": "user note", "websockets": false,
		"headers": map[string]any{"X-User-Header": "keep"}, "excluded_models": []any{"unwanted-model"},
	}
	for key, value := range settings {
		prior[key] = value
	}
	old, _ := json.Marshal(prior)
	if managedCredentialMatches(old, c, next) {
		t.Fatal("legacy sidecar PAT must require migration")
	}
	merged, err := mergeManagedAuthFields(next, old)
	if err != nil {
		t.Fatal(err)
	}
	actual, _ := decodeJSONMap(merged)
	for key, value := range settings {
		if !jsonValuesEquivalent(key, actual[key], value) {
			t.Fatalf("native setting %s changed", key)
		}
	}
	if !managedCredentialMatches(merged, c, merged) {
		t.Fatal("canonical native PAT not recognized")
	}
	for _, field := range []string{"runtime_only", "base_url", "expired"} {
		if _, exists := actual[field]; exists {
			t.Fatalf("legacy %s retained", field)
		}
	}
	for key, value := range map[string]any{"type": pluginProviderID, "runtime_only": true, "base_url": "http://sidecar:8787/backend-api/codex"} {
		mutant, _ := decodeJSONMap(merged)
		mutant[key] = value
		raw, _ := json.Marshal(mutant)
		if managedCredentialMatches(raw, c, merged) {
			t.Fatalf("accepted residual bridge field %s", key)
		}
	}
}
