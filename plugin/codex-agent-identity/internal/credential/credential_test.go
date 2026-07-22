package credential

import (
	"encoding/json"
	"strings"
	"testing"
)

func validFile() []byte {
	raw, _ := json.Marshal(map[string]any{
		"type":              "codex",
		"auth_mode":         AuthMode,
		"email":             "agent-aabbccddeeff@invalid.example",
		"access_token":      "cais_0123456789abcdef0123456789abcdef",
		"base_url":          "http://codex-agent-identity-sidecar:8787/backend-api/codex",
		"agent_identity_id": "agent-aabbccddeeff",
		"websockets":        true,
		"prefix":            "agenttest",
	})
	return raw
}

func TestParseManagedCredential(t *testing.T) {
	t.Parallel()
	parsed, handled, err := Parse("codex", "codex-agent-identity-aabbccddeeff.json", validFile())
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
	if parsed.Attributes["api_key"] != "cais_0123456789abcdef0123456789abcdef" ||
		parsed.Attributes["base_url"] != "http://codex-agent-identity-sidecar:8787/backend-api/codex" ||
		parsed.ID != "codex-agent-identity-aabbccddeeff.json" || parsed.Prefix != "agenttest" {
		t.Fatalf("unexpected parsed credential: %#v", parsed)
	}
}

func TestParseLeavesOrdinaryCodexOAuthToCPA(t *testing.T) {
	t.Parallel()
	for _, raw := range [][]byte{
		[]byte(`{"type":"codex","access_token":"oauth-token"}`),
		validFile(),
	} {
		provider := "codex"
		if strings.Contains(string(raw), AuthMode) {
			provider = "gemini-cli"
		}
		parsed, handled, err := Parse(provider, "oauth.json", raw)
		if err != nil || handled || parsed != nil {
			t.Fatalf("provider=%s handled=%v parsed=%#v err=%v", provider, handled, parsed, err)
		}
	}
}

func TestParseRejectsUnsafeManagedCredential(t *testing.T) {
	t.Parallel()
	mutations := []func(map[string]any){
		func(value map[string]any) { value["base_url"] = "http://127.0.0.1:8787/backend-api/codex" },
		func(value map[string]any) {
			value["base_url"] = "https://codex-agent-identity-sidecar:8787/backend-api/codex"
		},
		func(value map[string]any) { value["access_token"] = "not-a-sidecar-key" },
		func(value map[string]any) { value["agent_identity_id"] = "agent-../../secret" },
	}
	for index, mutate := range mutations {
		var payload map[string]any
		_ = json.Unmarshal(validFile(), &payload)
		mutate(payload)
		raw, _ := json.Marshal(payload)
		parsed, handled, err := Parse("codex", "managed.json", raw)
		if !handled || err == nil || parsed != nil {
			t.Fatalf("case %d handled=%v parsed=%#v err=%v", index, handled, parsed, err)
		}
	}
}

func TestParseAllowsExplicitSidecarHost(t *testing.T) {
	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_HOSTS", "codex-sidecar.internal")
	var payload map[string]any
	_ = json.Unmarshal(validFile(), &payload)
	payload["base_url"] = "http://codex-sidecar.internal:8787/backend-api/codex"
	raw, _ := json.Marshal(payload)
	parsed, handled, err := Parse("codex", "managed.json", raw)
	if err != nil || !handled || parsed == nil {
		t.Fatalf("handled=%v parsed=%#v err=%v", handled, parsed, err)
	}
}
