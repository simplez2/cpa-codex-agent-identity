package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/simplez2/cpa-codex-agent-identity/internal/cpa"
	"github.com/simplez2/cpa-codex-agent-identity/internal/identity"
	"github.com/simplez2/cpa-codex-agent-identity/internal/server"
	identitystore "github.com/simplez2/cpa-codex-agent-identity/internal/store"
)

func TestSidecarManagementUISynchronizesCPACodexAuthFile(t *testing.T) {
	fixture := newAgentFixture(t)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(fixture.jwks)
	}))
	defer jwksServer.Close()

	const managementKey = "management-key-at-least-24-characters"
	var channelMu sync.Mutex
	authFiles := make(map[string][]byte)
	cpaServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+managementKey {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		channelMu.Lock()
		defer channelMu.Unlock()
		name := request.URL.Query().Get("name")
		switch request.URL.Path {
		case "/auth-files":
			switch request.Method {
			case http.MethodGet:
				files := make([]map[string]any, 0, len(authFiles))
				for fileName := range authFiles {
					files = append(files, map[string]any{"name": fileName})
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"files": files})
			case http.MethodPost:
				authFiles[name], _ = io.ReadAll(request.Body)
				writer.WriteHeader(http.StatusOK)
			case http.MethodDelete:
				if _, ok := authFiles[name]; !ok {
					http.NotFound(writer, request)
					return
				}
				delete(authFiles, name)
				writer.WriteHeader(http.StatusOK)
			default:
				http.Error(writer, "method", http.StatusMethodNotAllowed)
			}
		case "/auth-files/download":
			data, ok := authFiles[name]
			if !ok {
				http.NotFound(writer, request)
				return
			}
			_, _ = writer.Write(data)
		case "/auth-files/fields":
			var patch map[string]any
			if request.Method != http.MethodPatch || json.NewDecoder(request.Body).Decode(&patch) != nil {
				http.Error(writer, "bad patch", http.StatusBadRequest)
				return
			}
			fileName, _ := patch["name"].(string)
			var payload map[string]any
			if json.Unmarshal(authFiles[fileName], &payload) != nil {
				http.NotFound(writer, request)
				return
			}
			for key, value := range patch {
				if key != "name" {
					payload[key] = value
				}
			}
			authFiles[fileName], _ = json.Marshal(payload)
			writer.WriteHeader(http.StatusOK)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer cpaServer.Close()

	channelManager, err := cpa.NewManager(cpaServer.URL, managementKey, "http://sidecar:8787/backend-api/codex", cpaServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	credentialStore, err := identitystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := identity.NewManager(jwksServer.URL, jwksServer.URL, jwksServer.Client())
	upstreamURL, _ := url.Parse(jwksServer.URL)
	handler, err := server.New(server.Config{
		UpstreamOrigin:   upstreamURL,
		PublicCPABaseURL: "http://sidecar:8787/backend-api/codex",
		ManagementKey:    managementKey,
		CPAChannels:      channelManager,
	}, credentialStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(handler)
	defer sidecar.Close()

	uiResponse, err := http.Get(sidecar.URL + "/agent-identity/")
	if err != nil {
		t.Fatal(err)
	}
	uiBody, _ := io.ReadAll(uiResponse.Body)
	uiResponse.Body.Close()
	if uiResponse.StatusCode != http.StatusOK || !bytes.Contains(uiBody, []byte("Codex Agent Identity")) {
		t.Fatalf("management UI unavailable: status=%d body=%s", uiResponse.StatusCode, uiBody)
	}
	if !bytes.Contains(uiBody, []byte(`id="connection-form"`)) || !bytes.Contains(uiBody, []byte(`autocomplete="username"`)) || !bytes.Contains(uiBody, []byte(`rel="icon" href="data:,"`)) {
		t.Fatalf("management UI is missing form or favicon metadata: %s", uiBody)
	}
	if uiResponse.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("management UI is missing CSP")
	}
	themeScriptIndex := bytes.Index(uiBody, []byte("<script src=\"./theme.js\"></script>"))
	styleIndex := bytes.Index(uiBody, []byte("<link rel=\"stylesheet\" href=\"./style.css\">"))
	if themeScriptIndex < 0 || styleIndex < 0 || themeScriptIndex > styleIndex {
		t.Fatalf("management UI must load theme.js before style.css: %s", uiBody)
	}
	themeResponse, err := http.Get(sidecar.URL + "/agent-identity/theme.js")
	if err != nil {
		t.Fatal(err)
	}
	themeBody, _ := io.ReadAll(themeResponse.Body)
	themeResponse.Body.Close()
	if themeResponse.StatusCode != http.StatusOK || !bytes.Contains(themeBody, []byte("cli-proxy-theme")) || !bytes.Contains(themeBody, []byte("cpa-codex-agent-identity:theme")) {
		t.Fatalf("theme bridge unavailable: status=%d body=%s", themeResponse.StatusCode, themeBody)
	}
	if !strings.Contains(themeResponse.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("theme bridge CSP is missing self script permission: %q", themeResponse.Header.Get("Content-Security-Policy"))
	}
	styleResponse, err := http.Get(sidecar.URL + "/agent-identity/style.css")
	if err != nil {
		t.Fatal(err)
	}
	styleBody, _ := io.ReadAll(styleResponse.Body)
	styleResponse.Body.Close()
	if styleResponse.StatusCode != http.StatusOK || !bytes.Contains(styleBody, []byte(":root[data-theme=\"dark\"]")) || !bytes.Contains(styleBody, []byte("--primary-color: #8b8680")) {
		t.Fatalf("CPA-aligned stylesheet unavailable: status=%d body=%s", styleResponse.StatusCode, styleBody)
	}
	for _, legacyColor := range [][]byte{[]byte("#2563eb"), []byte("#3971f2"), []byte("#5b8cff"), []byte("#070b12"), []byte("#060910"), []byte("--blue")} {
		if bytes.Contains(bytes.ToLower(styleBody), legacyColor) {
			t.Fatalf("stylesheet still contains legacy color %q", legacyColor)
		}
	}
	embedResponse, err := http.Get(sidecar.URL + "/agent-identity/?embed=cpamc")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, embedResponse.Body)
	embedResponse.Body.Close()
	if !strings.Contains(embedResponse.Header.Get("Content-Security-Policy"), "frame-ancestors 'self'") || embedResponse.Header.Get("X-Frame-Options") != "" {
		t.Fatalf("embed headers are not CPAMC-compatible: csp=%q xfo=%q", embedResponse.Header.Get("Content-Security-Policy"), embedResponse.Header.Get("X-Frame-Options"))
	}

	_, identityID, _ := importIdentityDetailsAtPath(t, sidecar.URL+"/agent-identity/api/identities/import", fixture.token)
	channelMu.Lock()
	fileName := "codex-user@example.invalid-k12.json"
	stored := authFiles[fileName]
	var storedPayload map[string]any
	if json.Unmarshal(stored, &storedPayload) != nil || storedPayload["auth_mode"] != "agent_identity_sidecar" || storedPayload["base_url"] != "http://sidecar:8787/backend-api/codex" || storedPayload["disabled"] != false || storedPayload["email"] != "user@example.invalid" || storedPayload["plan_type"] != "k12" {
		t.Fatalf("unexpected CPA auth file: %s", stored)
	}
	channelMu.Unlock()

	deleteRequest, _ := http.NewRequest(http.MethodDelete, sidecar.URL+"/agent-identity/api/identities/"+identityID, nil)
	deleteRequest.Header.Set("Authorization", "Bearer "+managementKey)
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d", deleteResponse.StatusCode)
	}
	channelMu.Lock()
	defer channelMu.Unlock()
	if len(authFiles) != 0 {
		t.Fatalf("CPA auth file was not removed: %#v", authFiles)
	}
}

func TestSidecarImportProxyAndUnauthorizedRetry(t *testing.T) {
	fixture := newAgentFixture(t)
	var registrations atomic.Int32
	var upstreamCalls atomic.Int32

	services := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/jwks":
			writer.Header().Set("Cache-Control", "max-age=600")
			_ = json.NewEncoder(writer).Encode(fixture.jwks)
		case strings.HasPrefix(request.URL.Path, "/accounts/v1/agent/"):
			var body struct {
				Timestamp string `json:"timestamp"`
				Signature string `json:"signature"`
			}
			if json.NewDecoder(request.Body).Decode(&body) != nil {
				http.Error(writer, "bad registration", http.StatusBadRequest)
				return
			}
			signature, err := base64.StdEncoding.DecodeString(body.Signature)
			if err != nil || !ed25519.Verify(fixture.edPublicKey, []byte(fixture.runtimeID+":"+body.Timestamp), signature) {
				http.Error(writer, "bad signature", http.StatusUnauthorized)
				return
			}
			registration := registrations.Add(1)
			_ = json.NewEncoder(writer).Encode(map[string]string{"task_id": fmt.Sprintf("task-%d", registration)})
		case request.URL.Path == "/backend-api/codex/responses":
			call := upstreamCalls.Add(1)
			if err := verifyAssertion(request, fixture, fmt.Sprintf("task-%d", call)); err != nil {
				http.Error(writer, "bad assertion", http.StatusUnauthorized)
				return
			}
			if got := request.Header.Get("ChatGPT-Account-ID"); got != fixture.accountID {
				http.Error(writer, "missing account", http.StatusUnauthorized)
				return
			}
			body, _ := io.ReadAll(request.Body)
			if string(body) != `{"model":"gpt-test","input":"hello"}` {
				http.Error(writer, "body mismatch", http.StatusBadRequest)
				return
			}
			if call == 1 {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"error":{"code":"auth_unavailable"}}`))
				return
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("event: response.completed\ndata: {\"ok\":true}\n\n"))
		case request.URL.Path == "/backend-api/codex/ws":
			if err := verifyAssertion(request, fixture, "task-3"); err != nil {
				http.Error(writer, "bad assertion", http.StatusUnauthorized)
				return
			}
			connection, buffered, err := writer.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			key := request.Header.Get("Sec-WebSocket-Key")
			digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			accept := base64.StdEncoding.EncodeToString(digest[:])
			_, _ = fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
			_ = buffered.Flush()
			_ = connection.Close()
		default:
			http.NotFound(writer, request)
		}
	}))
	defer services.Close()

	dataDirectory := t.TempDir()
	credentialStore, err := identitystore.Open(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	manager := identity.NewManager(services.URL+"/jwks", services.URL+"/accounts", services.Client())
	upstreamURL, _ := url.Parse(services.URL)
	handler, err := server.New(server.Config{
		UpstreamOrigin:     upstreamURL,
		PublicCPABaseURL:   "http://sidecar:8787/backend-api/codex",
		ManagementKey:      "management-key-at-least-24-characters",
		MaxReplayBodyBytes: 1 << 20,
		OutboundTransport:  services.Client().Transport,
	}, credentialStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(handler)
	defer sidecar.Close()

	clientKey := importIdentity(t, sidecar.URL, fixture.token)
	request, err := http.NewRequest(
		http.MethodPost,
		sidecar.URL+"/backend-api/codex/responses",
		strings.NewReader(`{"model":"gpt-test","input":"hello"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+clientKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Session_id", "session-1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, responseBody)
	}
	if !bytes.Contains(responseBody, []byte("response.completed")) {
		t.Fatalf("SSE response was not forwarded: %s", responseBody)
	}
	if registrations.Load() != 2 || upstreamCalls.Load() != 2 {
		t.Fatalf("registrations=%d upstream_calls=%d, want 2/2", registrations.Load(), upstreamCalls.Load())
	}
	assertWebsocketUpgrade(t, sidecar.URL, clientKey)
	if registrations.Load() != 3 {
		t.Fatalf("registrations after websocket=%d, want 3", registrations.Load())
	}

	entries, err := os.ReadDir(dataDirectory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("identity files = %d err=%v", len(entries), err)
	}
	info, err := os.Stat(filepath.Join(dataDirectory, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("identity file mode=%o, want 600", info.Mode().Perm())
	}
}

func TestSidecarPersonalAccessTokenImportAndProxyUsesBearerWithout401Retry(t *testing.T) {
	const token = "at-team-personal-access-token"
	var whoAmICalls atomic.Int32
	var upstreamCalls atomic.Int32
	services := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/user-auth-credential/whoami":
			if request.Header.Get("Authorization") != "Bearer "+token {
				http.Error(writer, "bad token", http.StatusUnauthorized)
				return
			}
			whoAmICalls.Add(1)
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"email":                      "team-user@example.invalid",
				"chatgpt_user_id":            "user-team",
				"chatgpt_account_id":         "account-team",
				"chatgpt_plan_type":          "team",
				"chatgpt_account_is_fedramp": false,
			})
		case "/backend-api/codex/responses":
			if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("ChatGPT-Account-ID") != "account-team" {
				http.Error(writer, "bad upstream auth", http.StatusForbidden)
				return
			}
			call := upstreamCalls.Add(1)
			if call == 1 {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"ok":true}`))
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"code":"invalid_token"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer services.Close()

	credentialStore, err := identitystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := identity.NewManagerWithPersonalAccessTokenAPI("", "", services.URL, services.Client())
	upstreamURL, _ := url.Parse(services.URL)
	handler, err := server.New(server.Config{
		UpstreamOrigin:     upstreamURL,
		PublicCPABaseURL:   "http://sidecar:8787/backend-api/codex",
		ManagementKey:      "management-key-at-least-24-characters",
		MaxReplayBodyBytes: 1 << 20,
		OutboundTransport:  services.Client().Transport,
	}, credentialStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(handler)
	defer sidecar.Close()

	clientKey, _, importBody := importIdentityDetails(t, sidecar.URL, token)
	if bytes.Contains(importBody, []byte(token)) || !bytes.Contains(importBody, []byte(`"credential_kind":"personal_access_token"`)) {
		t.Fatalf("unexpected import response: %s", importBody)
	}

	call := func() *http.Response {
		request, requestErr := http.NewRequest(http.MethodPost, sidecar.URL+"/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-test","input":"hello"}`))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+clientKey)
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		return response
	}

	response := call()
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("first proxy status=%d", response.StatusCode)
	}
	response = call()
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second proxy status=%d", response.StatusCode)
	}
	if whoAmICalls.Load() != 1 {
		t.Fatalf("whoami calls=%d, want 1", whoAmICalls.Load())
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream calls=%d, PAT 401 must not be retried", upstreamCalls.Load())
	}
}

func TestManagementAPICallUsesAgentAssertionForQuotaAndForwardsOAuth(t *testing.T) {
	fixture := newAgentFixture(t)
	const managementKey = "management-key-at-least-24-characters"
	var registrations atomic.Int32
	var quotaCalls atomic.Int32

	services := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/jwks":
			_ = json.NewEncoder(writer).Encode(fixture.jwks)
		case strings.HasPrefix(request.URL.Path, "/accounts/v1/agent/"):
			registration := registrations.Add(1)
			_ = json.NewEncoder(writer).Encode(map[string]string{"task_id": fmt.Sprintf("quota-task-%d", registration)})
		case request.URL.Path == "/backend-api/wham/usage":
			call := quotaCalls.Add(1)
			if err := verifyAssertion(request, fixture, fmt.Sprintf("quota-task-%d", call)); err != nil {
				http.Error(writer, err.Error(), http.StatusUnauthorized)
				return
			}
			if request.Header.Get("ChatGPT-Account-ID") != fixture.accountID {
				http.Error(writer, "missing account", http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"plan_type":"team","rate_limit":{"primary_window":{"used_percent":12}}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer services.Close()

	credentialStore, _ := identitystore.Open(t.TempDir())
	publicIdentity, _, err := credentialStore.Import(fixture.token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	agentFileName := "codex-user@example.invalid-k12.json"
	agentAuthRaw, _ := json.Marshal(map[string]any{
		"type":              "codex",
		"auth_mode":         "agent_identity_sidecar",
		"agent_identity_id": publicIdentity.ID,
		"access_token":      "cais_test",
	})
	cpaServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+managementKey {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/auth-files":
			_ = json.NewEncoder(writer).Encode(map[string]any{"files": []map[string]any{
				{"name": agentFileName, "auth_index": "agent-index"},
				{"name": "codex-oauth.json", "auth_index": "oauth-index"},
			}})
		case "/auth-files/download":
			switch request.URL.Query().Get("name") {
			case agentFileName:
				_, _ = writer.Write(agentAuthRaw)
			case "codex-oauth.json":
				_, _ = writer.Write([]byte(`{"type":"codex","access_token":"oauth"}`))
			default:
				http.NotFound(writer, request)
			}
		case "/api-call":
			var forwarded map[string]any
			if json.NewDecoder(request.Body).Decode(&forwarded) != nil || forwarded["auth_index"] != "oauth-index" {
				http.Error(writer, "bad forward", http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"status_code": 200,
				"header":      map[string][]string{},
				"body":        `{"oauth":true}`,
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer cpaServer.Close()
	channelManager, err := cpa.NewManager(cpaServer.URL, managementKey, "http://sidecar:8787/backend-api/codex", cpaServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	manager := identity.NewManager(services.URL+"/jwks", services.URL+"/accounts", services.Client())
	upstreamURL, _ := url.Parse(services.URL)
	handler, err := server.New(server.Config{
		UpstreamOrigin:    upstreamURL,
		ManagementKey:     managementKey,
		OutboundTransport: services.Client().Transport,
		CPAChannels:       channelManager,
	}, credentialStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(handler)
	defer sidecar.Close()

	call := func(authIndex, managementHeader string) (int, []byte) {
		t.Helper()
		body := []byte(fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":"https://chatgpt.com/backend-api/wham/usage","header":{"Authorization":"Bearer $TOKEN$","User-Agent":"codex-cli"}}`, authIndex))
		request, _ := http.NewRequest(http.MethodPost, sidecar.URL+"/v0/management/api-call", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if managementHeader == "x" {
			request.Header.Set("X-Management-Key", managementKey)
		} else {
			request.Header.Set("Authorization", "Bearer "+managementKey)
		}
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		return response.StatusCode, raw
	}

	status, raw := call("agent-index", "bearer")
	var agentResponse struct {
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
	}
	if json.Unmarshal(raw, &agentResponse) != nil || status != http.StatusOK || agentResponse.StatusCode != http.StatusOK || !strings.Contains(agentResponse.Body, `"plan_type":"team"`) {
		t.Fatalf("agent status=%d response=%s", status, raw)
	}
	if registrations.Load() != 1 || quotaCalls.Load() != 1 {
		t.Fatalf("registrations=%d quota_calls=%d", registrations.Load(), quotaCalls.Load())
	}

	status, raw = call("oauth-index", "x")
	var oauthResponse struct {
		Body string `json:"body"`
	}
	if json.Unmarshal(raw, &oauthResponse) != nil || status != http.StatusOK || oauthResponse.Body != `{"oauth":true}` {
		t.Fatalf("oauth status=%d response=%s", status, raw)
	}
}

func TestManagementAPICallPersonalAccessTokenFallsBackToUsageForResetCredits(t *testing.T) {
	const (
		token         = "at-team-personal-access-token"
		managementKey = "management-key-at-least-24-characters"
	)
	var whoAmICalls atomic.Int32
	var detailCalls atomic.Int32
	var usageCalls atomic.Int32
	var consumeCalls atomic.Int32

	services := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/user-auth-credential/whoami":
			if request.Header.Get("Authorization") != "Bearer "+token {
				http.Error(writer, "bad token", http.StatusUnauthorized)
				return
			}
			whoAmICalls.Add(1)
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"email":              "team-user@example.invalid",
				"chatgpt_user_id":    "user-team",
				"chatgpt_account_id": "account-team",
				"chatgpt_plan_type":  "team",
			})
		case "/backend-api/wham/rate-limit-reset-credits":
			if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("ChatGPT-Account-ID") != "account-team" {
				http.Error(writer, "bad upstream auth", http.StatusForbidden)
				return
			}
			if request.Header.Get("User-Agent") != "codex-cli-test" {
				http.Error(writer, "missing forwarded user-agent", http.StatusBadRequest)
				return
			}
			detailCalls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"type":"rejected_by_access_enforcement","code":"no_matching_rule"},"status":401}`))
		case "/backend-api/wham/usage":
			if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("ChatGPT-Account-ID") != "account-team" {
				http.Error(writer, "bad fallback auth", http.StatusForbidden)
				return
			}
			usageCalls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"plan_type":"team","rate_limit_reset_credits":{"available_count":1,"applicable_available_count":0}}`))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("ChatGPT-Account-ID") != "account-team" {
				http.Error(writer, "bad consume request", http.StatusForbidden)
				return
			}
			var consumeBody struct {
				RedeemRequestID string  `json:"redeem_request_id"`
				CreditID        *string `json:"credit_id"`
			}
			if json.NewDecoder(request.Body).Decode(&consumeBody) != nil || consumeBody.RedeemRequestID != "redeem-test" || consumeBody.CreditID != nil {
				http.Error(writer, "bad consume body", http.StatusBadRequest)
				return
			}
			consumeCalls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":"reset","windows_reset":2}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer services.Close()

	credentialStore, err := identitystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	publicIdentity, _, err := credentialStore.Import(token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	credentialFileName := "codex-team-user@example.invalid-team.json"
	credentialRaw, _ := json.Marshal(map[string]any{
		"type":              "codex",
		"auth_mode":         "agent_identity_sidecar",
		"credential_kind":   "personal_access_token",
		"agent_identity_id": publicIdentity.ID,
		"access_token":      "cais_test",
	})
	cpaServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+managementKey {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/auth-files":
			_ = json.NewEncoder(writer).Encode(map[string]any{"files": []map[string]any{
				{"name": credentialFileName, "auth_index": "pat-index"},
			}})
		case "/auth-files/download":
			if request.URL.Query().Get("name") != credentialFileName {
				http.NotFound(writer, request)
				return
			}
			_, _ = writer.Write(credentialRaw)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer cpaServer.Close()

	channelManager, err := cpa.NewManager(cpaServer.URL, managementKey, "http://sidecar:8787/backend-api/codex", cpaServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	manager := identity.NewManagerWithPersonalAccessTokenAPI("", "", services.URL, services.Client())
	upstreamURL, _ := url.Parse(services.URL)
	handler, err := server.New(server.Config{
		UpstreamOrigin:    upstreamURL,
		ManagementKey:     managementKey,
		OutboundTransport: services.Client().Transport,
		CPAChannels:       channelManager,
	}, credentialStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(handler)
	defer sidecar.Close()

	body := []byte(`{"auth_index":"pat-index","method":"GET","url":"https://chatgpt.com/backend-api/wham/rate-limit-reset-credits","header":{"User-Agent":"codex-cli-test"}}`)
	request, _ := http.NewRequest(http.MethodPost, sidecar.URL+"/v0/management/api-call", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+managementKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var wrapped struct {
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
	}
	if json.Unmarshal(raw, &wrapped) != nil || response.StatusCode != http.StatusOK || wrapped.StatusCode != http.StatusOK {
		t.Fatalf("status=%d response=%s", response.StatusCode, raw)
	}
	var details struct {
		Credits                  json.RawMessage `json:"credits"`
		AvailableCount           int64           `json:"available_count"`
		ApplicableAvailableCount int64           `json:"applicable_available_count"`
	}
	if json.Unmarshal([]byte(wrapped.Body), &details) != nil || details.AvailableCount != 1 ||
		details.ApplicableAvailableCount != 0 || string(details.Credits) != "null" {
		t.Fatalf("unexpected fallback details: %s", wrapped.Body)
	}

	consumeData, _ := json.Marshal(map[string]string{"redeem_request_id": "redeem-test"})
	consumeCall, _ := json.Marshal(map[string]any{
		"auth_index": "pat-index",
		"method":     http.MethodPost,
		"url":        "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume",
		"data":       string(consumeData),
	})
	consumeRequest, _ := http.NewRequest(http.MethodPost, sidecar.URL+"/v0/management/api-call", bytes.NewReader(consumeCall))
	consumeRequest.Header.Set("Content-Type", "application/json")
	consumeRequest.Header.Set("Authorization", "Bearer "+managementKey)
	consumeResponse, err := http.DefaultClient.Do(consumeRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer consumeResponse.Body.Close()
	consumeRaw, _ := io.ReadAll(consumeResponse.Body)
	if json.Unmarshal(consumeRaw, &wrapped) != nil || consumeResponse.StatusCode != http.StatusOK || wrapped.StatusCode != http.StatusOK {
		t.Fatalf("consume status=%d response=%s", consumeResponse.StatusCode, consumeRaw)
	}

	if whoAmICalls.Load() != 1 || detailCalls.Load() != 1 || usageCalls.Load() != 1 || consumeCalls.Load() != 1 {
		t.Fatalf("whoami=%d detail=%d usage=%d consume=%d, want 1/1/1/1", whoAmICalls.Load(), detailCalls.Load(), usageCalls.Load(), consumeCalls.Load())
	}
}

func assertWebsocketUpgrade(t *testing.T, sidecarURL, clientKey string) {
	t.Helper()
	parsed, err := url.Parse(sidecarURL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", parsed.Host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	requestText := "GET /backend-api/codex/ws HTTP/1.1\r\n" +
		"Host: " + parsed.Host + "\r\n" +
		"Authorization: Bearer " + clientKey + "\r\n" +
		"Session_id: websocket-session\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n" // gitleaks:allow -- RFC 6455 sample nonce, not a credential.
	if _, err = io.WriteString(connection, requestText); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("websocket status=%d, want 101", response.StatusCode)
	}
}

func TestSidecarManagementDoesNotEchoTokenAndDeleteRevokesKey(t *testing.T) {
	fixture := newAgentFixture(t)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(fixture.jwks)
	}))
	defer jwksServer.Close()

	credentialStore, err := identitystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := identity.NewManager(jwksServer.URL, jwksServer.URL, jwksServer.Client())
	upstreamURL, _ := url.Parse(jwksServer.URL)
	handler, err := server.New(server.Config{
		UpstreamOrigin:   upstreamURL,
		PublicCPABaseURL: "http://sidecar:8787/backend-api/codex",
		ManagementKey:    "management-key-at-least-24-characters",
	}, credentialStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(handler)
	defer sidecar.Close()

	clientKey, identityID, responseBody := importIdentityDetails(t, sidecar.URL, fixture.token)
	if bytes.Contains(responseBody, []byte(fixture.token)) || bytes.Contains(responseBody, []byte(fixture.runtimeID)) {
		t.Fatal("management response exposed Agent Identity credential material")
	}

	deleteRequest, _ := http.NewRequest(http.MethodDelete, sidecar.URL+"/admin/v1/identities/"+identityID, nil)
	deleteRequest.Header.Set("Authorization", "Bearer management-key-at-least-24-characters")
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d", deleteResponse.StatusCode)
	}

	proxyRequest, _ := http.NewRequest(http.MethodPost, sidecar.URL+"/backend-api/codex/responses", strings.NewReader(`{}`))
	proxyRequest.Header.Set("Authorization", "Bearer "+clientKey)
	proxyResponse, err := http.DefaultClient.Do(proxyRequest)
	if err != nil {
		t.Fatal(err)
	}
	proxyResponse.Body.Close()
	if proxyResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key status=%d, want 401", proxyResponse.StatusCode)
	}
}

func TestSidecarConcurrentRequestsRegisterOneTask(t *testing.T) {
	fixture := newAgentFixture(t)
	var registrations atomic.Int32
	services := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/jwks":
			_ = json.NewEncoder(writer).Encode(fixture.jwks)
		case strings.HasPrefix(request.URL.Path, "/accounts/v1/agent/"):
			registrations.Add(1)
			time.Sleep(25 * time.Millisecond)
			_ = json.NewEncoder(writer).Encode(map[string]string{"task_id": "shared-task"})
		case request.URL.Path == "/backend-api/codex/responses":
			if err := verifyAssertion(request, fixture, "shared-task"); err != nil {
				http.Error(writer, "bad assertion", http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer services.Close()

	credentialStore, _ := identitystore.Open(t.TempDir())
	manager := identity.NewManager(services.URL+"/jwks", services.URL+"/accounts", services.Client())
	upstreamURL, _ := url.Parse(services.URL)
	handler, err := server.New(server.Config{
		UpstreamOrigin:    upstreamURL,
		ManagementKey:     "management-key-at-least-24-characters",
		OutboundTransport: services.Client().Transport,
	}, credentialStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(handler)
	defer sidecar.Close()
	clientKey := importIdentity(t, sidecar.URL, fixture.token)

	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, 50)
	for range 50 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			request, _ := http.NewRequest(http.MethodPost, sidecar.URL+"/backend-api/codex/responses", strings.NewReader(`{}`))
			request.Header.Set("Authorization", "Bearer "+clientKey)
			request.Header.Set("Session_id", "same-session")
			response, requestErr := http.DefaultClient.Do(request)
			if requestErr != nil {
				errorsChannel <- requestErr
				return
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				errorsChannel <- fmt.Errorf("unexpected status %d", response.StatusCode)
			}
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for requestErr := range errorsChannel {
		t.Error(requestErr)
	}
	if registrations.Load() != 1 {
		t.Fatalf("task registrations=%d, want 1", registrations.Load())
	}
}

type agentFixture struct {
	token       string
	jwks        identity.JWKS
	edPublicKey ed25519.PublicKey
	runtimeID   string
	accountID   string
}

func newAgentFixture(t *testing.T) agentFixture {
	t.Helper()
	rsaPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	edPublicKey, edPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(edPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := "runtime-test"
	accountID := "account-test"
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	payload, _ := json.Marshal(identity.Claims{
		AgentRuntimeID:  runtimeID,
		AgentPrivateKey: base64.StdEncoding.EncodeToString(privateKeyDER),
		AccountID:       accountID,
		ChatGPTUserID:   "user-test",
		Email:           "user@example.invalid",
		PlanType:        "k12",
		Issuer:          identity.JWTIssuer,
		Audience:        identity.JWTAudience,
		IssuedAt:        time.Now().Add(-time.Minute).Unix(),
		ExpiresAt:       time.Now().Add(time.Hour).Unix(),
	})
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(encodedHeader + "." + encodedPayload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, rsaPrivateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	token := encodedHeader + "." + encodedPayload + "." + base64.RawURLEncoding.EncodeToString(signature)
	exponent := big.NewInt(int64(rsaPrivateKey.PublicKey.E)).Bytes()
	return agentFixture{
		token:       token,
		edPublicKey: edPublicKey,
		runtimeID:   runtimeID,
		accountID:   accountID,
		jwks: identity.JWKS{Keys: []identity.JWK{{
			Kid: "test-key",
			Kty: "RSA",
			Alg: "RS256",
			Use: "sig",
			N:   base64.RawURLEncoding.EncodeToString(rsaPrivateKey.PublicKey.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(exponent),
		}}},
	}
}

func verifyAssertion(request *http.Request, fixture agentFixture, expectedTaskID string) error {
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "AgentAssertion ") {
		return fmt.Errorf("unexpected authorization scheme")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(authorization, "AgentAssertion "))
	if err != nil {
		return err
	}
	var assertion struct {
		RuntimeID string `json:"agent_runtime_id"`
		Signature string `json:"signature"`
		TaskID    string `json:"task_id"`
		Timestamp string `json:"timestamp"`
	}
	if err = json.Unmarshal(raw, &assertion); err != nil {
		return err
	}
	if assertion.RuntimeID != fixture.runtimeID || assertion.TaskID != expectedTaskID {
		return fmt.Errorf("unexpected assertion identity")
	}
	signature, err := base64.StdEncoding.DecodeString(assertion.Signature)
	if err != nil {
		return err
	}
	payload := assertion.RuntimeID + ":" + assertion.TaskID + ":" + assertion.Timestamp
	if !ed25519.Verify(fixture.edPublicKey, []byte(payload), signature) {
		return fmt.Errorf("assertion signature verification failed")
	}
	return nil
}

func importIdentity(t *testing.T, sidecarURL, token string) string {
	t.Helper()
	clientKey, _, _ := importIdentityDetails(t, sidecarURL, token)
	return clientKey
}

func importIdentityDetails(t *testing.T, sidecarURL, token string) (clientKey, identityID string, raw []byte) {
	t.Helper()
	return importIdentityDetailsAtPath(t, sidecarURL+"/admin/v1/identities/import", token)
}

func importIdentityDetailsAtPath(t *testing.T, importURL, token string) (clientKey, identityID string, raw []byte) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"codex_access_token": token})
	request, _ := http.NewRequest(http.MethodPost, importURL, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer management-key-at-least-24-characters")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ = io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("import status=%d body=%s", response.StatusCode, raw)
	}
	var decoded struct {
		ClientKey string `json:"client_api_key"`
		Identity  struct {
			ID string `json:"id"`
		} `json:"identity"`
	}
	if json.Unmarshal(raw, &decoded) != nil || decoded.ClientKey == "" || decoded.Identity.ID == "" {
		t.Fatalf("invalid import response: %s", raw)
	}
	return decoded.ClientKey, decoded.Identity.ID, raw
}

func TestHealthEndpoint(t *testing.T) {
	fixture := newAgentFixture(t)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(fixture.jwks)
	}))
	defer jwksServer.Close()
	credentialStore, _ := identitystore.Open(t.TempDir())
	manager := identity.NewManager(jwksServer.URL, jwksServer.URL, jwksServer.Client())
	upstreamURL, _ := url.Parse(jwksServer.URL)
	handler, err := server.New(server.Config{
		UpstreamOrigin: upstreamURL,
		ManagementKey:  "management-key-at-least-24-characters",
	}, credentialStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(context.Background())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status=%d", recorder.Code)
	}
}
