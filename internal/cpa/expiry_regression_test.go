package cpa

import (
	"encoding/json"
	"testing"
)

func TestManagedCredentialRejectsResidualNativeExpiry(t *testing.T) {
	m := &Manager{sidecarBaseURL: "http://sidecar:8787/backend-api/codex"}
	c := Credential{IdentityID: "agent-aabb0011", ClientKey: "cais_test_0000000000000000000000000000", UpstreamToken: "test-token", Email: "test@example.com"}
	expected, err := m.credentialJSONWithDisabled(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if !managedCredentialMatches(expected, c, expected) {
		t.Fatal("canonical credential must match")
	}
	var stale map[string]any
	if err := json.Unmarshal(expected, &stale); err != nil {
		t.Fatal(err)
	}
	if stale["type"] != pluginProviderID {
		t.Fatal("stored type must select the plugin, not bypass it")
	}
	stale["type"] = runtimeProviderID
	legacy, _ := json.Marshal(stale)
	if managedCredentialMatches(legacy, c, expected) {
		t.Fatal("legacy native dispatch must require migration")
	}
	stale["type"] = pluginProviderID
	stale["expired"] = "2020-01-01T00:00:00Z"
	stale["proxy_url"] = "socks5://proxy.example:1080"
	stale["priority"] = 7
	raw, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if managedCredentialMatches(raw, c, expected) {
		t.Fatal("residual native expiry must not pass synchronization verification")
	}
	merged, err := mergeManagedAuthFields(expected, raw)
	if err != nil {
		t.Fatal(err)
	}
	var repaired map[string]any
	if err := json.Unmarshal(merged, &repaired); err != nil {
		t.Fatal(err)
	}
	if _, exists := repaired["expired"]; exists {
		t.Fatal("canonical synchronization must drop residual native expiry")
	}
	if repaired["proxy_url"] != stale["proxy_url"] || repaired["priority"] != float64(7) {
		t.Fatal("repair must preserve user routing settings")
	}
	if !managedCredentialMatches(merged, c, expected) {
		t.Fatal("repaired credential must match")
	}
}
