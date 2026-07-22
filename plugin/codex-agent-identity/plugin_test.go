package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestRegisterDeclaresAuthAndManagementCapabilities(t *testing.T) {
	configurePluginForTest(t, "sidecar_url: /agent-identity/")
	raw, err := handleMethod(pluginabi.MethodPluginRegister, lifecyclePayload(t, "sidecar_url: /agent-identity/"))
	if err != nil {
		t.Fatal(err)
	}
	var result registration
	decodePluginResult(t, raw, &result)
	if result.Metadata.Name != pluginName || result.Metadata.GitHubRepository != pluginRepository || result.Metadata.Logo != pluginLogo || !result.Capabilities.AuthProvider || !result.Capabilities.ManagementAPI {
		t.Fatalf("unexpected registration: %#v", result)
	}
	if len(result.Metadata.ConfigFields) != 1 || result.Metadata.ConfigFields[0].Name != configSidecarURL {
		t.Fatalf("unexpected config fields: %#v", result.Metadata.ConfigFields)
	}
}

func TestManagementResourceEmbedsConfiguredSidecar(t *testing.T) {
	configurePluginForTest(t, "sidecar_url: /agent-identity/")
	raw, err := handleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var registered managementRegistration
	decodePluginResult(t, raw, &registered)
	if len(registered.Resources) != 1 || registered.Resources[0].Path != resourcePath || registered.Resources[0].Menu != pluginMenu {
		t.Fatalf("unexpected resources: %#v", registered.Resources)
	}
	raw, err = handleMethod(pluginabi.MethodManagementHandle, nil)
	if err != nil {
		t.Fatal(err)
	}
	var response managementResponse
	decodePluginResult(t, raw, &response)
	body := string(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "/agent-identity/?embed=cpamc") || !strings.Contains(body, readyMessageType) {
		t.Fatalf("unexpected management response: status=%d body=%s", response.StatusCode, body)
	}
	for _, expected := range []string{themeMessageType, "cli-proxy-theme", "--bg-primary", "--primary-color", "searchParams.set('theme'"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("management response is missing CPA theme integration %q", expected)
		}
	}
	for _, legacyColor := range []string{"#2563eb", "#3971f2", "#5b8cff", "#070b12", "#060910", "--blue"} {
		if strings.Contains(strings.ToLower(body), legacyColor) {
			t.Fatalf("management response still contains legacy color %q", legacyColor)
		}
	}
	if !strings.Contains(response.Headers.Get("Content-Security-Policy"), "frame-src 'self'") {
		t.Fatalf("unexpected CSP: %s", response.Headers.Get("Content-Security-Policy"))
	}
}

func TestInvalidSidecarURLRendersConfigurationFallback(t *testing.T) {
	configurePluginForTest(t, "sidecar_url: file:///tmp/identity")
	raw, err := handleMethod(pluginabi.MethodManagementHandle, nil)
	if err != nil {
		t.Fatal(err)
	}
	var response managementResponse
	decodePluginResult(t, raw, &response)
	body := string(response.Body)
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "sidecar_url must use") {
		t.Fatalf("unexpected fallback: status=%d body=%s", response.StatusCode, response.Body)
	}
	if !strings.Contains(body, "cli-proxy-theme") || !strings.Contains(body, "data-theme=\"dark\"") || strings.Contains(strings.ToLower(body), "#070b12") {
		t.Fatalf("configuration fallback is not CPA theme aware: %s", body)
	}
}

func TestNormalizeSidecarURLRejectsCredentialsAndQuery(t *testing.T) {
	for _, value := range []string{
		"https://user:pass@example.com/agent-identity/",
		"https://example.com/agent-identity/?secret=1",
		"relative/path",
	} {
		if _, _, err := normalizeSidecarURL(value); err == nil {
			t.Fatalf("normalizeSidecarURL(%q) succeeded", value)
		}
	}
	got, source, err := normalizeSidecarURL("https://cpa.example.com/agent-identity")
	if err != nil || got != "https://cpa.example.com/agent-identity/" || source != "https://cpa.example.com" {
		t.Fatalf("got=%q source=%q err=%v", got, source, err)
	}
}

func configurePluginForTest(t *testing.T, yaml string) {
	t.Helper()
	if _, err := handleMethod(pluginabi.MethodPluginReconfigure, lifecyclePayload(t, yaml)); err != nil {
		t.Fatal(err)
	}
}

func lifecyclePayload(t *testing.T, yaml string) []byte {
	t.Helper()
	raw, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte(yaml), SchemaVersion: pluginabi.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodePluginResult(t *testing.T, raw []byte, target any) {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, raw)
	}
	if !env.OK || env.Error != nil {
		t.Fatalf("plugin returned error: %#v", env.Error)
	}
	if err := json.Unmarshal(env.Result, target); err != nil {
		t.Fatalf("decode result: %v body=%s", err, env.Result)
	}
}
