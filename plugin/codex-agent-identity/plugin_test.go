package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPluginAndRuntimeProvidersAreDistinct(t *testing.T) {
	if pluginProviderID == runtimeProviderID {
		t.Fatal("plugin provider must not claim CPA's native codex provider")
	}
}

func TestAuthParseLeavesNativeCodexOAuthToCPA(t *testing.T) {
	request, err := json.Marshal(pluginapi.AuthParseRequest{
		Provider: "codex",
		FileName: "oauth.json",
		RawJSON:  []byte(`{"type":"codex","access_token":"oauth-token"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(pluginabi.MethodAuthParse, request)
	if err != nil {
		t.Fatal(err)
	}
	var response pluginapi.AuthParseResponse
	decodePluginResult(t, raw, &response)
	if response.Handled {
		t.Fatalf("native Codex OAuth auth was intercepted: %#v", response)
	}
}

func TestAuthParseMapsManagedCredentialToNativeCodexOAuthShape(t *testing.T) {
	storage := []byte(`{
		"type":"codex-agent-identity",
		"auth_mode":"agent_identity_sidecar",
		"auth_kind":"oauth",
		"email":"agent@example.invalid",
		"access_token":"cais_test_0000000000000000000000000000",
		"base_url":"http://codex-agent-identity-sidecar:8787/backend-api/codex",
		"agent_identity_id":"agent-aabbccddeeff",
		"account_id":"account-a",
		"chatgpt_user_id":"user-a",
		"plan_type":"free",
		"credential_kind":"agent_identity",
		"websockets":"true",
		"disabled":"true",
		"proxy_url":"socks5://127.0.0.1:1080"
	}`)
	request, err := json.Marshal(pluginapi.AuthParseRequest{Provider: pluginProviderID, FileName: "managed.json", RawJSON: storage})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(pluginabi.MethodAuthParse, request)
	if err != nil {
		t.Fatal(err)
	}
	var response pluginapi.AuthParseResponse
	decodePluginResult(t, raw, &response)
	auth := response.Auth
	if !response.Handled || auth.Provider != "codex" || !auth.Disabled || auth.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("managed auth state was not mapped: %#v", response)
	}
	if auth.Attributes["auth_kind"] != "oauth" || auth.Attributes["plan_type"] != "free" || auth.Attributes["account_id"] != "account-a" || auth.Attributes["chatgpt_user_id"] != "user-a" {
		t.Fatalf("native Codex routing attributes are incomplete: %#v", auth.Attributes)
	}
	if strings.TrimSpace(auth.Attributes["api_key"]) != "" || auth.Metadata["access_token"] != "cais_test_0000000000000000000000000000" {
		t.Fatalf("managed auth was not kept on the OAuth-compatible executor path: metadata=%#v attributes=%#v", auth.Metadata, auth.Attributes)
	}
	if string(auth.StorageJSON) != string(storage) || auth.NextRefreshAfter.Before(time.Now().UTC()) {
		t.Fatalf("provider storage or refresh schedule was lost: %#v", auth)
	}
}

func TestAuthRefreshPreservesManagedCredentialState(t *testing.T) {
	storage := []byte(`{
		"type":"codex-agent-identity",
		"auth_mode":"agent_identity_sidecar",
		"email":"agent@example.invalid",
		"access_token":"cais_test_0000000000000000000000000000",
		"base_url":"http://codex-agent-identity-sidecar:8787/backend-api/codex",
		"agent_identity_id":"agent-aabbccddeeff",
		"plan_type":"team",
		"websockets":true,
		"disabled":true,
		"proxy_url":"http://proxy.internal:8080"
	}`)
	request, err := json.Marshal(pluginapi.AuthRefreshRequest{
		AuthID:       "managed.json",
		AuthProvider: pluginProviderID,
		StorageJSON:  storage,
		Metadata:     map[string]any{"file_name": "managed.json", "disabled": true},
		Attributes:   map[string]string{"auth_kind": "oauth", "plan_type": "team"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(pluginabi.MethodAuthRefresh, request)
	if err != nil {
		t.Fatal(err)
	}
	var response pluginapi.AuthRefreshResponse
	decodePluginResult(t, raw, &response)
	if !response.Auth.Disabled || response.Auth.ProxyURL != "http://proxy.internal:8080" || response.Auth.Attributes["auth_kind"] != "oauth" || response.Auth.Attributes["plan_type"] != "team" {
		t.Fatalf("refresh dropped managed auth state: %#v", response.Auth)
	}
	if string(response.Auth.StorageJSON) != string(storage) || response.NextRefreshAfter.IsZero() || response.NextRefreshAfter.Before(time.Now().UTC()) {
		t.Fatalf("refresh dropped storage or returned a stale schedule: %#v", response)
	}
}

func TestAuthRefreshPassesThroughNativeCodexOAuth(t *testing.T) {
	storage := []byte(`{"type":"codex","access_token":"oauth-token","email":"oauth@example.invalid"}`)
	request, err := json.Marshal(pluginapi.AuthRefreshRequest{
		AuthID:       "oauth.json",
		AuthProvider: "codex",
		StorageJSON:  storage,
		Metadata:     map[string]any{"file_name": "oauth.json", "email": "oauth@example.invalid"},
		Attributes:   map[string]string{"auth_kind": "oauth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(pluginabi.MethodAuthRefresh, request)
	if err != nil {
		t.Fatal(err)
	}
	var response pluginapi.AuthRefreshResponse
	decodePluginResult(t, raw, &response)
	if response.Auth.Provider != "codex" || response.Auth.ID != "oauth.json" || response.Auth.Label != "oauth@example.invalid" {
		t.Fatalf("native OAuth auth was not passed through: %#v", response.Auth)
	}
	if string(response.Auth.StorageJSON) != string(storage) || !response.NextRefreshAfter.IsZero() {
		t.Fatalf("native OAuth storage or refresh schedule changed: %#v", response)
	}
}

func TestAuthLoginStartDoesNotImpersonateNativeCodexOAuth(t *testing.T) {
	request, err := json.Marshal(pluginapi.AuthLoginStartRequest{Provider: pluginProviderID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handleMethod(pluginabi.MethodAuthLoginStart, request); err == nil || !strings.Contains(err.Error(), "native Codex OAuth login is unchanged") {
		t.Fatalf("unexpected login start result: %v", err)
	}
}

func TestAuthIdentifierUsesPluginProviderIDWithoutClaimingNativeCodex(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodAuthIdentifier, nil)
	if err != nil {
		t.Fatal(err)
	}
	var response identifierResponse
	decodePluginResult(t, raw, &response)
	if response.Identifier != pluginProviderID || response.Identifier == runtimeProviderID {
		t.Fatalf("identifier=%q, want plugin provider %q distinct from runtime provider %q", response.Identifier, pluginProviderID, runtimeProviderID)
	}
}

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
	if len(result.Metadata.ConfigFields) != 0 {
		t.Fatalf("internal sidecar endpoints must not appear in public plugin metadata: %#v", result.Metadata.ConfigFields)
	}
}

func TestLegacySidecarConfigRemainsAcceptedWhenFieldsAreHidden(t *testing.T) {
	configurePluginForTest(t, "sidecar_url: http://127.0.0.1:18787/agent-identity/\nsidecar_api_url: http://127.0.0.1:18787/v0/management/api-call")
	current := currentRuntimeState()
	if current.sidecarURL != "http://127.0.0.1:18787/agent-identity/" || current.sidecarAPIURL != "http://127.0.0.1:18787/v0/management/api-call" {
		t.Fatalf("legacy sidecar config was not retained: %#v", current)
	}
	raw, err := handleMethod(pluginabi.MethodPluginRegister, lifecyclePayload(t, "sidecar_url: http://127.0.0.1:18787/agent-identity/\nsidecar_api_url: http://127.0.0.1:18787/v0/management/api-call"))
	if err != nil {
		t.Fatal(err)
	}
	var result registration
	decodePluginResult(t, raw, &result)
	for _, field := range result.Metadata.ConfigFields {
		if field.Name == configSidecarURL || field.Name == configSidecarAPIURL {
			t.Fatalf("legacy internal config leaked into public metadata: %#v", result.Metadata.ConfigFields)
		}
	}
}

func TestRegisterNegotiatesSchemaVersion(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  uint32
	}{
		{name: "missing schema", input: []byte(`{}`), want: 1},
		{name: "schema one", input: []byte(`{"schema_version":1}`), want: 1},
		{name: "current schema", input: []byte(fmt.Sprintf(`{"schema_version":%d}`, pluginabi.SchemaVersion)), want: pluginabi.SchemaVersion},
		{name: "future schema", input: []byte(fmt.Sprintf(`{"schema_version":%d}`, pluginabi.SchemaVersion+1)), want: pluginabi.SchemaVersion},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := handleMethod(pluginabi.MethodPluginRegister, test.input)
			if err != nil {
				t.Fatal(err)
			}
			var result registration
			decodePluginResult(t, raw, &result)
			if result.SchemaVersion != test.want {
				t.Fatalf("schema_version = %d, want %d", result.SchemaVersion, test.want)
			}
		})
	}
}

func TestManagementUIRegistersAuthenticatedRouteAndResource(t *testing.T) {
	configurePluginForTest(t, "sidecar_url: /agent-identity/")
	raw, err := handleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var registered managementRegistration
	decodePluginResult(t, raw, &registered)
	if len(registered.Routes) != 2 {
		t.Fatalf("unexpected routes: %#v", registered.Routes)
	}
	var route managementRoute
	for _, candidate := range registered.Routes {
		if candidate.Method == http.MethodGet && candidate.Path == managementOpenPath {
			route = candidate
		}
	}
	if route.Method != http.MethodGet || route.Path != managementOpenPath {
		t.Fatalf("unexpected management route: %#v", route)
	}
	var apiRoute managementRoute
	for _, candidate := range registered.Routes {
		if candidate.Method == http.MethodPost && candidate.Path == managementAPICallPath {
			apiRoute = candidate
		}
	}
	if apiRoute.Method != http.MethodPost || apiRoute.Path != managementAPICallPath {
		t.Fatalf("missing quota API bridge route: %#v", registered.Routes)
	}
	if len(registered.Resources) != 1 {
		t.Fatalf("unexpected resources: %#v", registered.Resources)
	}
	resource := registered.Resources[0]
	if resource.Path != "/open" || resource.Menu != pluginName || resource.Description == "" {
		t.Fatalf("unexpected resource route: %#v", resource)
	}
	if !strings.Contains(string(raw), `"resources"`) || !strings.Contains(string(raw), pluginName) {
		t.Fatalf("registration did not advertise the CPAMC resource menu: %s", raw)
	}

	for _, path := range []string{managementOpenFullPath, resourceOpenFullPath} {
		raw, err = handleMethod(pluginabi.MethodManagementHandle, managementPayload(t, http.MethodGet, path))
		if err != nil {
			t.Fatal(err)
		}
		var response managementResponse
		decodePluginResult(t, raw, &response)
		body := string(response.Body)
		if response.StatusCode != http.StatusOK || !strings.Contains(body, "/agent-identity/?embed=cpamc") || !strings.Contains(body, readyMessageType) {
			t.Fatalf("unexpected management response for %s: status=%d body=%s", path, response.StatusCode, body)
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
		if !strings.Contains(response.Headers.Get("Content-Security-Policy"), "frame-ancestors 'self'") || response.Headers.Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Fatalf("management response can be framed cross-origin: headers=%v", response.Headers)
		}
	}
}

func TestManagementAPICallBridgeForwardsCPARequestToSidecar(t *testing.T) {
	requestBody := []byte(`{"auth_index":"auth-1","method":"GET","url":"https://chatgpt.com/backend-api/wham/usage","header":{"Authorization":"Bearer $TOKEN$"}}`)
	var received struct {
		method        string
		path          string
		authorization string
		cookie        string
		body          []byte
	}
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.method = r.Method
		received.path = r.URL.Path
		received.authorization = r.Header.Get("Authorization")
		received.cookie = r.Header.Get("Cookie")
		received.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","status_code":200,"header":{},"body":"{\"plan_type\":\"pro\"}"}`))
	}))
	defer sidecar.Close()

	configurePluginForTest(t, "sidecar_url: http://127.0.0.1:18787/agent-identity/\nsidecar_api_url: "+sidecar.URL)
	payload, err := json.Marshal(managementRequest{
		Method: http.MethodPost,
		Path:   managementAPICallFullPath,
		Headers: http.Header{
			"Authorization": []string{"Bearer management-key"},
			"Cookie":        []string{"must-not-forward"},
		},
		Body: requestBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(pluginabi.MethodManagementHandle, payload)
	if err != nil {
		t.Fatal(err)
	}
	var response managementResponse
	decodePluginResult(t, raw, &response)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(response.Body), `"status_code":200`) {
		t.Fatalf("unexpected bridge response: status=%d body=%s", response.StatusCode, response.Body)
	}
	if received.method != http.MethodPost || received.path != sidecarAPICallPath || received.authorization != "Bearer management-key" || received.cookie != "" || string(received.body) != string(requestBody) {
		t.Fatalf("sidecar request = method=%s path=%s authorization=%q cookie=%q body=%s", received.method, received.path, received.authorization, received.cookie, received.body)
	}
}

func TestManagementHandlerRejectsUnknownResourceAndWrongMethod(t *testing.T) {
	configurePluginForTest(t, "sidecar_url: /agent-identity/")

	raw, err := handleMethod(pluginabi.MethodManagementHandle, managementPayload(t, http.MethodGet, "/v0/resource/plugins/codex-agent-identity/unknown"))
	if err != nil {
		t.Fatal(err)
	}
	var response managementResponse
	decodePluginResult(t, raw, &response)
	if response.StatusCode != http.StatusNotFound || strings.Contains(string(response.Body), "/agent-identity/") {
		t.Fatalf("unknown resource path was not rejected: status=%d body=%s", response.StatusCode, response.Body)
	}

	raw, err = handleMethod(pluginabi.MethodManagementHandle, managementPayload(t, http.MethodPost, managementOpenFullPath))
	if err != nil {
		t.Fatal(err)
	}
	decodePluginResult(t, raw, &response)
	if response.StatusCode != http.StatusMethodNotAllowed || response.Headers.Get("Allow") != http.MethodGet {
		t.Fatalf("wrong method response: status=%d headers=%v", response.StatusCode, response.Headers)
	}

	for _, method := range []string{"get", " GET "} {
		raw, err = handleMethod(pluginabi.MethodManagementHandle, managementPayload(t, method, managementOpenFullPath))
		if err != nil {
			t.Fatal(err)
		}
		decodePluginResult(t, raw, &response)
		if response.StatusCode != http.StatusMethodNotAllowed || response.Headers.Get("Allow") != http.MethodGet {
			t.Fatalf("noncanonical method %q was accepted: status=%d headers=%v", method, response.StatusCode, response.Headers)
		}
	}

	for _, path := range []string{" " + managementOpenFullPath, managementOpenFullPath + " "} {
		raw, err = handleMethod(pluginabi.MethodManagementHandle, managementPayload(t, http.MethodGet, path))
		if err != nil {
			t.Fatal(err)
		}
		decodePluginResult(t, raw, &response)
		if response.StatusCode != http.StatusNotFound || strings.Contains(string(response.Body), "/agent-identity/") {
			t.Fatalf("noncanonical path %q was accepted: status=%d body=%s", path, response.StatusCode, response.Body)
		}
	}
}

func TestManagementHandlerRejectsMalformedRequest(t *testing.T) {
	configurePluginForTest(t, "sidecar_url: /agent-identity/")

	for name, payload := range map[string][]byte{
		"empty":          nil,
		"invalid json":   []byte(`{`),
		"missing fields": []byte(`{}`),
		"missing method": []byte(`{"Path":"/v0/management/codex-agent-identity/open"}`),
		"missing path":   []byte(`{"Method":"GET"}`),
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := handleMethod(pluginabi.MethodManagementHandle, payload)
			if err != nil {
				t.Fatal(err)
			}
			var response managementResponse
			decodePluginResult(t, raw, &response)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
			}
			if response.Headers.Get("Cache-Control") != "no-store" || response.Headers.Get("X-Frame-Options") != "DENY" {
				t.Fatalf("unsafe error headers: %v", response.Headers)
			}
		})
	}
}

func TestInvalidSidecarURLRendersConfigurationFallback(t *testing.T) {
	configurePluginForTest(t, "sidecar_url: file:///tmp/identity")
	raw, err := handleMethod(pluginabi.MethodManagementHandle, managementPayload(t, http.MethodGet, managementOpenFullPath))
	if err != nil {
		t.Fatal(err)
	}
	var response managementResponse
	decodePluginResult(t, raw, &response)
	body := string(response.Body)
	for _, mojibake := range []string{"\u93bb", "\u7e60", "\u7487", "\u951b"} {
		if strings.Contains(body, mojibake) {
			t.Fatalf("configuration fallback contains mojibake %q: %s", mojibake, body)
		}
	}
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "sidecar_url must use") {
		t.Fatalf("unexpected fallback: status=%d body=%s", response.StatusCode, response.Body)
	}
	if !strings.Contains(body, "cli-proxy-theme") || !strings.Contains(body, "data-theme=\"dark\"") || strings.Contains(strings.ToLower(body), "#070b12") {
		t.Fatalf("configuration fallback is not CPA theme aware: %s", body)
	}
	if !strings.Contains(response.Headers.Get("Content-Security-Policy"), "frame-ancestors 'self'") || response.Headers.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("configuration fallback can be framed cross-origin: headers=%v", response.Headers)
	}
}

func TestDefaultSidecarURLIsUsedWhenConfigIsOmitted(t *testing.T) {
	configurePluginForTest(t, "")
	raw, err := handleMethod(pluginabi.MethodManagementHandle, managementPayload(t, http.MethodGet, managementOpenFullPath))
	if err != nil {
		t.Fatal(err)
	}
	var response managementResponse
	decodePluginResult(t, raw, &response)
	body := string(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, defaultSidecarURL+"?embed=cpamc") {
		t.Fatalf("default sidecar URL was not applied: status=%d body=%s", response.StatusCode, body)
	}
	if !strings.Contains(response.Headers.Get("Content-Security-Policy"), defaultSidecarOrigin) {
		t.Fatalf("default sidecar origin is missing from CSP: %s", response.Headers.Get("Content-Security-Policy"))
	}
}

func TestRelativeSidecarURLDerivesSameOriginAuthenticatedBridge(t *testing.T) {
	target, err := sidecarManagementAPIURL(runtimeState{sidecarURL: "/agent-identity/"}, http.Header{
		"Origin": []string{"https://cpa.example.com"},
	})
	if err != nil || target != "https://cpa.example.com/agent-identity/api/cpa-api-call" {
		t.Fatalf("derived sidecar API URL = %q, %v", target, err)
	}
}

func TestApplyConfigKeepsCustomSidecarOriginForDerivedAPI(t *testing.T) {
	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_API_URL", "")
	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_HOSTS", "")
	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_HTTP_PORTS", "")
	configurePluginForTest(t, "sidecar_url: https://sidecar.example.test/agent-identity/")
	got, err := sidecarManagementAPIURL(currentRuntimeState(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://sidecar.example.test/v0/management/api-call"; got != want {
		t.Fatalf("derived custom sidecar API URL = %q, want %q", got, want)
	}
}

func TestDefaultRuntimeSidecarAPIURLUsesContainerServiceEnvironment(t *testing.T) {
	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_API_URL", "")
	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_HOSTS", "codex-agent-identity-sidecar,unused")
	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_HTTP_PORTS", "")
	if got, want := defaultRuntimeSidecarAPIURL(), "http://codex-agent-identity-sidecar:8787/v0/management/api-call"; got != want {
		t.Fatalf("default container sidecar API URL = %q, want %q", got, want)
	}

	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_HTTP_PORTS", "9876")
	if got, want := defaultRuntimeSidecarAPIURL(), "http://codex-agent-identity-sidecar:9876/v0/management/api-call"; got != want {
		t.Fatalf("configured container sidecar API URL = %q, want %q", got, want)
	}

	t.Setenv("CODEX_AGENT_IDENTITY_SIDECAR_API_URL", "http://sidecar.internal:8787")
	if got, want := defaultRuntimeSidecarAPIURL(), "http://sidecar.internal:8787/v0/management/api-call"; got != want {
		t.Fatalf("explicit container sidecar API URL = %q, want %q", got, want)
	}
}

func TestManagementAPICallBridgeStripsTransportHeaders(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Fatalf("sidecar Accept-Encoding = %q, want identity", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "identity")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("Set-Cookie", "should-not-cross-management-boundary")
		_, _ = w.Write([]byte(`{"status_code":204,"header":{},"body":""}`))
	}))
	defer sidecar.Close()

	configurePluginForTest(t, "sidecar_url: http://127.0.0.1:18787/agent-identity/\nsidecar_api_url: "+sidecar.URL)
	payload, err := json.Marshal(managementRequest{
		Method: http.MethodPost,
		Path:   managementAPICallFullPath,
		Headers: http.Header{
			"Accept-Encoding": []string{"gzip"},
			"TE":              []string{"trailers"},
		},
		Body: []byte(`{"auth_index":"auth-1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(pluginabi.MethodManagementHandle, payload)
	if err != nil {
		t.Fatal(err)
	}
	var response managementResponse
	decodePluginResult(t, raw, &response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bridge status=%d body=%s", response.StatusCode, response.Body)
	}
	for _, name := range []string{"Content-Length", "Content-Encoding", "Connection", "Keep-Alive", "Set-Cookie", "Transfer-Encoding"} {
		if response.Headers.Get(name) != "" {
			t.Fatalf("response header %s crossed management boundary: %v", name, response.Headers)
		}
	}
}

func TestNormalizeSidecarURLRejectsCredentialsAndQuery(t *testing.T) {
	for _, value := range []string{
		"https://user:pass@example.com/agent-identity/",
		"https://example.com/agent-identity/?secret=1",
		"https://example.com/agent-identity/?",
		"//example.com/agent-identity/",
		"relative/path",
	} {
		if _, _, err := normalizeSidecarURL(value); err == nil {
			t.Fatalf("normalizeSidecarURL(%q) succeeded", value)
		}
	}
	for _, test := range []struct {
		input  string
		want   string
		source string
	}{
		{input: "", want: defaultSidecarURL, source: defaultSidecarOrigin},
		{input: "/", want: "/agent-identity/", source: "'self'"},
		{input: "http://127.0.0.1:18787/", want: defaultSidecarURL, source: defaultSidecarOrigin},
		{input: "https://cpa.example.com/agent-identity", want: "https://cpa.example.com/agent-identity/", source: "https://cpa.example.com"},
	} {
		got, source, err := normalizeSidecarURL(test.input)
		if err != nil || got != test.want || source != test.source {
			t.Fatalf("normalizeSidecarURL(%q) = %q, %q, %v; want %q, %q", test.input, got, source, err, test.want, test.source)
		}
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

func managementPayload(t *testing.T, method, path string) []byte {
	t.Helper()
	raw, err := json.Marshal(managementRequest{Method: method, Path: path})
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
