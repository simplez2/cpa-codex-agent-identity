package credential

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func validFileFor(provider string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"type":                provider,
		"auth_mode":           AuthMode,
		"auth_kind":           "oauth",
		"email":               "agent-aabbccddeeff@invalid.example",
		"access_token":        "upstream.oauth.token",
		SidecarClientKeyField: "cais_test_0000000000000000000000000000",
		"base_url":            "http://codex-agent-identity-sidecar:8787/backend-api/codex",
		"agent_identity_id":   "agent-aabbccddeeff",
		"account_id":          "account-a",
		"chatgpt_user_id":     "user-a",
		"plan_type":           "free",
		"credential_kind":     "agent_identity",
		"websockets":          true,
		"fedramp":             false,
		"disabled":            false,
		"prefix":              "agenttest",
	})
	return raw
}

func validFile() []byte {
	return validFileFor(PluginProvider)
}

func legacyValidFile() []byte {
	return validFileFor(LegacyPluginProvider)
}

func TestParseManagedCredential(t *testing.T) {
	t.Parallel()
	parsed, handled, err := Parse(PluginProvider, "codex-agent-identity-aabbccddeeff.json", validFile())
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
	if parsed.Attributes[SidecarAuthorizationHeaderAttribute] != "Bearer cais_test_0000000000000000000000000000" {
		t.Fatalf("sidecar runtime key was not mapped to the Codex executor: %#v", parsed.Attributes)
	}
	if _, exists := parsed.Attributes["api_key"]; exists {
		t.Fatalf("managed OAuth auth must not be classified as a Codex API key: %#v", parsed.Attributes)
	}
	if parsed.Metadata["access_token"] != "upstream.oauth.token" ||
		parsed.Metadata["auth_kind"] != "oauth" ||
		parsed.Attributes["base_url"] != "http://codex-agent-identity-sidecar:8787/backend-api/codex" ||
		parsed.Attributes["auth_mode"] != AuthMode ||
		parsed.Attributes["auth_kind"] != "oauth" ||
		parsed.Attributes["runtime_only"] != "true" ||
		parsed.Attributes["websockets"] != "true" ||
		parsed.Attributes["plan_type"] != "free" ||
		parsed.Attributes["account_id"] != "account-a" ||
		parsed.Attributes["chatgpt_user_id"] != "user-a" ||
		parsed.Attributes["credential_kind"] != "agent_identity" ||
		parsed.ID != "codex-agent-identity-aabbccddeeff.json" || parsed.Prefix != "agenttest" ||
		parsed.Disabled || parsed.NextRefreshAfter.Before(time.Now().UTC()) {
		t.Fatalf("unexpected parsed credential: %#v", parsed)
	}
}

func TestParseManagedCredentialPreservesCPAOAuthClassification(t *testing.T) {
	t.Parallel()
	parsed, handled, err := Parse(PluginProvider, "codex-agent-identity-aabbccddeeff.json", validFile())
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
	auth := &cliproxyauth.Auth{
		Attributes: parsed.Attributes,
		Metadata:   parsed.Metadata,
	}
	if got := auth.AuthKind(); got != cliproxyauth.AuthKindOAuth {
		t.Fatalf("CPA auth kind = %q, want %q", got, cliproxyauth.AuthKindOAuth)
	}
	if _, exists := auth.Attributes[cliproxyauth.AttributeAPIKey]; exists {
		t.Fatalf("CPA api_key attribute must be absent: %#v", auth.Attributes)
	}
}

func TestParseManagedCredentialSplitsNativeAndRuntimeTokens(t *testing.T) {
	t.Parallel()
	parsed, handled, err := Parse(PluginProvider, "codex-agent-identity-aabbccddeeff.json", validFile())
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
	if token, ok := parsed.Metadata["access_token"].(string); !ok || token != "upstream.oauth.token" {
		t.Fatalf("CPA-native access token was not preserved for management api-call: %#v", parsed.Metadata)
	}
	if strings.TrimSpace(parsed.Attributes[SidecarAuthorizationHeaderAttribute]) != "Bearer cais_test_0000000000000000000000000000" || parsed.Attributes["auth_kind"] != "oauth" || parsed.Attributes["runtime_only"] != "true" {
		t.Fatalf("sidecar runtime routing was not preserved: %#v", parsed.Attributes)
	}
	if _, exists := parsed.Attributes["api_key"]; exists {
		t.Fatalf("sidecar routing must preserve CPA OAuth/file-backed behavior: %#v", parsed.Attributes)
	}
}

func TestParseManagedCredentialAcceptsLegacyAccessTokenClientKey(t *testing.T) {
	t.Parallel()
	var payload map[string]any
	if err := json.Unmarshal(validFile(), &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, SidecarClientKeyField)
	payload["access_token"] = "cais_test_0000000000000000000000000000"
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	parsed, handled, err := Parse(PluginProvider, "legacy-client-key.json", raw)
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
	if parsed.Metadata["access_token"] != "cais_test_0000000000000000000000000000" || parsed.Attributes[SidecarAuthorizationHeaderAttribute] != "Bearer cais_test_0000000000000000000000000000" {
		t.Fatalf("legacy client key mapping changed: metadata=%#v attributes=%#v", parsed.Metadata, parsed.Attributes)
	}
}

func TestParseManagedCredentialLegacyPluginFormat(t *testing.T) {
	t.Parallel()
	parsed, handled, err := Parse(PluginProvider, "legacy.json", legacyValidFile())
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
	if parsed.Metadata["type"] != LegacyPluginProvider {
		t.Fatalf("legacy type was not preserved: %#v", parsed.Metadata)
	}
}

func TestParseLeavesOrdinaryCodexOAuthToCPA(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		provider string
		raw      []byte
	}{
		{name: "ordinary oauth", provider: RuntimeProvider, raw: []byte(`{"type":"codex","access_token":"oauth-token"}`)},
		{name: "wrong provider", provider: "gemini-cli", raw: validFile()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, handled, err := Parse(tc.provider, "oauth.json", tc.raw)
			if err != nil || handled || parsed != nil {
				t.Fatalf("provider=%s handled=%v parsed=%#v err=%v", tc.provider, handled, parsed, err)
			}
		})
	}
}

func TestParseRecognizesCPAPersistedRuntimeProviderSidecarFile(t *testing.T) {
	t.Parallel()
	var payload map[string]any
	if err := json.Unmarshal(validFile(), &payload); err != nil {
		t.Fatal(err)
	}
	payload["type"] = RuntimeProvider // FileTokenStore writes the runtime provider name.
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	parsed, handled, err := Parse(RuntimeProvider, "codex-agent-identity-aabbccddeeff.json", raw)
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
	if parsed.Attributes["base_url"] != "http://codex-agent-identity-sidecar:8787/backend-api/codex" || parsed.Attributes["runtime_only"] != "true" {
		t.Fatalf("persisted runtime attributes were not restored: %#v", parsed.Attributes)
	}
	if parsed.Metadata["auth_mode"] != AuthMode || parsed.Metadata["type"] != RuntimeProvider {
		t.Fatalf("persisted runtime metadata was not retained: %#v", parsed.Metadata)
	}
}

func TestParseDefaultsMissingWebsocketsToEnabled(t *testing.T) {
	t.Parallel()
	var payload map[string]any
	if err := json.Unmarshal(validFile(), &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "websockets")
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	parsed, handled, err := Parse(PluginProvider, "managed.json", raw)
	if err != nil || !handled || parsed == nil || parsed.Attributes["websockets"] != "true" {
		t.Fatalf("missing websockets field was not kept compatible: handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
}

func TestParseAcceptsStringBooleanFields(t *testing.T) {
	t.Parallel()
	var payload map[string]any
	_ = json.Unmarshal(validFile(), &payload)
	payload["websockets"] = "true"
	payload["disabled"] = "true"
	payload["fedramp"] = "true"
	payload["proxy_url"] = "socks5://127.0.0.1:1080"
	raw, _ := json.Marshal(payload)
	parsed, handled, err := Parse(PluginProvider, "managed.json", raw)
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
	if !parsed.Disabled || parsed.ProxyURL != "socks5://127.0.0.1:1080" || parsed.Attributes["fedramp"] != "true" || parsed.Attributes["websockets"] != "true" {
		t.Fatalf("string boolean fields were not normalized: %#v", parsed)
	}
}

func TestParseRefreshTimeNeverUsesExpiredTimestamp(t *testing.T) {
	t.Parallel()
	var payload map[string]any
	_ = json.Unmarshal(validFile(), &payload)
	payload["expires_at"] = "2020-01-01T00:00:00Z"
	raw, _ := json.Marshal(payload)
	before := time.Now().UTC()
	parsed, handled, err := Parse(PluginProvider, "managed.json", raw)
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
	if !parsed.NextRefreshAfter.After(before) {
		t.Fatalf("next refresh is not in the future: %s", parsed.NextRefreshAfter)
	}
}

func TestParseRejectsUnsafeManagedCredential(t *testing.T) {
	t.Parallel()
	mutations := []func(map[string]any){
		func(value map[string]any) { value["base_url"] = "http://127.0.0.1:18788/backend-api/codex" },
		func(value map[string]any) { value["base_url"] = "https://unlisted.example/backend-api/codex" },
		func(value map[string]any) { value[SidecarClientKeyField] = "not-a-sidecar-key" },
		func(value map[string]any) { value["access_token"] = "" },
		func(value map[string]any) { value["access_token"] = "token\r\ninjected" },
		func(value map[string]any) { value["agent_identity_id"] = "agent-../../secret" },
		func(value map[string]any) { value["websockets"] = "sometimes" },
	}
	for index, mutate := range mutations {
		var payload map[string]any
		_ = json.Unmarshal(validFile(), &payload)
		mutate(payload)
		raw, _ := json.Marshal(payload)
		parsed, handled, err := Parse(PluginProvider, "managed.json", raw)
		if !handled || err == nil || parsed != nil {
			t.Fatalf("case %d handled=%v parsed=%#v err=%v", index, handled, parsed, err)
		}
	}
}

func TestParseAllowsExplicitSidecarHost(t *testing.T) {
	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_HOSTS", "codex-sidecar.internal")
	for _, endpoint := range []string{
		"http://codex-sidecar.internal:8787/backend-api/codex",
		"https://codex-sidecar.internal/backend-api/codex",
		"https://codex-sidecar.internal:9443/backend-api/codex",
	} {
		var payload map[string]any
		_ = json.Unmarshal(validFile(), &payload)
		payload["base_url"] = endpoint
		raw, _ := json.Marshal(payload)
		parsed, handled, err := Parse(PluginProvider, "managed.json", raw)
		if err != nil || !handled || parsed == nil || parsed.Attributes["base_url"] != endpoint {
			t.Fatalf("endpoint=%s handled=%v parsed=%#v err=%v", endpoint, handled, parsed, err)
		}
	}
}

func TestParseAllowsDefaultLoopbackSidecarEndpoints(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"http://127.0.0.1:8787/backend-api/codex",
		"http://127.0.0.1:18787/backend-api/codex",
		"http://localhost:8787/backend-api/codex",
		"http://localhost:18787/backend-api/codex",
		"http://[::1]:8787/backend-api/codex",
		"http://[::1]:18787/backend-api/codex",
	} {
		var payload map[string]any
		_ = json.Unmarshal(validFile(), &payload)
		payload["base_url"] = endpoint
		raw, _ := json.Marshal(payload)
		parsed, handled, err := Parse(PluginProvider, "managed.json", raw)
		if err != nil || !handled || parsed == nil || parsed.Attributes["base_url"] != endpoint {
			t.Fatalf("endpoint=%s handled=%v parsed=%#v err=%v", endpoint, handled, parsed, err)
		}
	}
}

func TestParseAllowsDockerSidecarAlias(t *testing.T) {
	t.Parallel()
	var payload map[string]any
	_ = json.Unmarshal(validFile(), &payload)
	payload["base_url"] = "http://sidecar:8787/backend-api/codex"
	raw, _ := json.Marshal(payload)
	parsed, handled, err := Parse(PluginProvider, "managed.json", raw)
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
	if parsed.Attributes["base_url"] != "http://sidecar:8787/backend-api/codex" {
		t.Fatalf("base_url = %q", parsed.Attributes["base_url"])
	}
}

func TestParseKeepsHTTPPortAllowlistNarrow(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"http://127.0.0.1:18788/backend-api/codex",
		"http://codex-agent-identity-sidecar:18787/backend-api/codex",
		"http://codex-agent-identity-sidecar/backend-api/codex",
		"http://sidecar:18788/backend-api/codex",
		"http://sidecar/backend-api/codex",
	} {
		var payload map[string]any
		_ = json.Unmarshal(validFile(), &payload)
		payload["base_url"] = endpoint
		raw, _ := json.Marshal(payload)
		parsed, handled, err := Parse(PluginProvider, "managed.json", raw)
		if !handled || err == nil || parsed != nil {
			t.Fatalf("endpoint=%s handled=%v parsed=%#v err=%v", endpoint, handled, parsed, err)
		}
	}
}

func TestParseAllowsExplicitSidecarHTTPPort(t *testing.T) {
	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_HOSTS", "codex-sidecar.internal")
	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_HTTP_PORTS", "18787")
	var payload map[string]any
	_ = json.Unmarshal(validFile(), &payload)
	payload["base_url"] = "http://codex-sidecar.internal:18787/backend-api/codex"
	raw, _ := json.Marshal(payload)
	parsed, handled, err := Parse(PluginProvider, "managed.json", raw)
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
}
