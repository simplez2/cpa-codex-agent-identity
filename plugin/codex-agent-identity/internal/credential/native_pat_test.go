package credential

import "testing"

func TestCanonicalNativePATIsNotProjectedThroughSidecar(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{RuntimeProvider, PluginProvider} {
		raw := []byte(`{"type":"codex","auth_mode":"agent_identity_sidecar","agent_identity_id":"agent-aabbccddeeff","credential_kind":"personal_access_token","access_token":"at-native","proxy_url":"socks5://proxy.example:1080","disabled":true}`)
		if parsed, handled, err := Parse(provider, "native.json", raw); parsed != nil || handled || err != nil {
			t.Fatal("native PAT was claimed by the sidecar parser")
		}
	}
}
