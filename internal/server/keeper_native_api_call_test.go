package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/simplez2/cpa-codex-agent-identity/internal/cpa"
	"github.com/simplez2/cpa-codex-agent-identity/internal/identity"
	"github.com/simplez2/cpa-codex-agent-identity/internal/server"
	identitystore "github.com/simplez2/cpa-codex-agent-identity/internal/store"
)

func TestKeeperNativeAPICallUsesUpstreamPATWhileRuntimeKeepsSidecarKey(t *testing.T) {
	const (
		managementKey = "management-key-at-least-24-characters"
		upstreamToken = "at-keeper-team-personal-access-token"
		accountID     = "account-keeper-team"
		authIndex     = "keeper-pat-auth-index"
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/user-auth-credential/whoami":
			if request.Header.Get("Authorization") != "Bearer "+upstreamToken || request.Header.Get("ChatGPT-Account-ID") != accountID {
				http.Error(writer, "bad whoami auth", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"email":              "keeper@example.invalid",
				"chatgpt_user_id":    "user-keeper-team",
				"chatgpt_account_id": accountID,
				"chatgpt_plan_type":  "team",
			})
		case "/backend-api/wham/usage":
			if request.Header.Get("Authorization") != "Bearer "+upstreamToken || request.Header.Get("ChatGPT-Account-ID") != accountID {
				http.Error(writer, "bad quota auth", http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"plan_type":"team","rate_limit":{"primary_window":{"used_percent":7}}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer upstream.Close()

	var filesMu sync.Mutex
	files := make(map[string][]byte)
	indexes := make(map[string]string)
	cpaServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+managementKey && request.Header.Get("X-Management-Key") != managementKey {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/v0/management/auth-files":
			name := request.URL.Query().Get("name")
			filesMu.Lock()
			defer filesMu.Unlock()
			switch request.Method {
			case http.MethodGet:
				items := make([]map[string]any, 0, len(files))
				for fileName := range files {
					items = append(items, map[string]any{"name": fileName, "auth_index": indexes[fileName]})
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"files": items})
			case http.MethodPost:
				raw, err := io.ReadAll(request.Body)
				if err != nil {
					http.Error(writer, "bad auth file", http.StatusBadRequest)
					return
				}
				files[name] = raw
				indexes[name] = authIndex
				writer.WriteHeader(http.StatusOK)
			default:
				writer.WriteHeader(http.StatusMethodNotAllowed)
			}
		case "/v0/management/auth-files/download":
			filesMu.Lock()
			raw, ok := files[request.URL.Query().Get("name")]
			filesMu.Unlock()
			if !ok {
				http.NotFound(writer, request)
				return
			}
			_, _ = writer.Write(raw)
		case "/v0/management/api-call":
			var call struct {
				AuthIndex string            `json:"auth_index"`
				Method    string            `json:"method"`
				URL       string            `json:"url"`
				Header    map[string]string `json:"header"`
			}
			if json.NewDecoder(request.Body).Decode(&call) != nil || call.AuthIndex != authIndex {
				http.Error(writer, "bad keeper request", http.StatusBadRequest)
				return
			}
			filesMu.Lock()
			var authRaw []byte
			for fileName, index := range indexes {
				if index == call.AuthIndex {
					authRaw = append([]byte(nil), files[fileName]...)
					break
				}
			}
			filesMu.Unlock()
			var authPayload map[string]any
			if len(authRaw) == 0 || json.Unmarshal(authRaw, &authPayload) != nil {
				http.Error(writer, "auth not found", http.StatusBadRequest)
				return
			}
			token, _ := authPayload["access_token"].(string)
			if strings.TrimSpace(token) == "" {
				http.Error(writer, "token not found", http.StatusBadRequest)
				return
			}
			target, err := url.Parse(call.URL)
			if err != nil {
				http.Error(writer, "bad target", http.StatusBadRequest)
				return
			}
			upstreamBase, _ := url.Parse(upstream.URL)
			target.Scheme = upstreamBase.Scheme
			target.Host = upstreamBase.Host
			upstreamRequest, err := http.NewRequest(call.Method, target.String(), nil)
			if err != nil {
				http.Error(writer, "bad upstream request", http.StatusBadRequest)
				return
			}
			for name, value := range call.Header {
				upstreamRequest.Header.Set(name, strings.ReplaceAll(value, "$TOKEN$", token))
			}
			response, err := upstream.Client().Do(upstreamRequest)
			if err != nil {
				http.Error(writer, "upstream unavailable", http.StatusBadGateway)
				return
			}
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"status_code": response.StatusCode,
				"header":      response.Header,
				"body":        string(body),
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer cpaServer.Close()

	channelManager, err := cpa.NewManager(cpaServer.URL+"/v0/management", managementKey, "http://sidecar:8787/backend-api/codex", cpaServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	credentialStore, err := identitystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	credentialManager := identity.NewManagerWithPersonalAccessTokenAPI("", "", upstream.URL, upstream.Client())
	upstreamURL, _ := url.Parse(upstream.URL)
	handler, err := server.New(server.Config{
		UpstreamOrigin: upstreamURL,
		ManagementKey:  managementKey,
		CPAChannels:    channelManager,
	}, credentialStore, credentialManager)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(handler)
	defer sidecar.Close()

	importBody, _ := json.Marshal(map[string]string{"codex_access_token": upstreamToken, "account_id": accountID})
	importRequest, _ := http.NewRequest(http.MethodPost, sidecar.URL+"/admin/v1/identities/import", bytes.NewReader(importBody))
	importRequest.Header.Set("Authorization", "Bearer "+managementKey)
	importRequest.Header.Set("Content-Type", "application/json")
	importResponse, err := sidecar.Client().Do(importRequest)
	if err != nil {
		t.Fatal(err)
	}
	importRaw, _ := io.ReadAll(importResponse.Body)
	_ = importResponse.Body.Close()
	if importResponse.StatusCode != http.StatusOK {
		t.Fatalf("import status=%d body=%s", importResponse.StatusCode, importRaw)
	}

	filesMu.Lock()
	if len(files) != 1 {
		filesMu.Unlock()
		t.Fatalf("managed auth files=%d, want 1", len(files))
	}
	var managedRaw []byte
	for _, raw := range files {
		managedRaw = append([]byte(nil), raw...)
	}
	filesMu.Unlock()
	var managed map[string]any
	if json.Unmarshal(managedRaw, &managed) != nil {
		t.Fatal("managed auth file is invalid")
	}
	clientKey, _ := managed["sidecar_client_key"].(string)
	if managed["access_token"] != upstreamToken || !strings.HasPrefix(clientKey, "cais_") {
		t.Fatal("managed auth file did not split native token and sidecar runtime key")
	}

	keeperBody := []byte(`{"auth_index":"keeper-pat-auth-index","method":"GET","url":"https://chatgpt.com/backend-api/wham/usage","header":{"Authorization":"Bearer $TOKEN$","Content-Type":"application/json","User-Agent":"codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal","Chatgpt-Account-Id":"account-keeper-team"}}`)
	keeperRequest, _ := http.NewRequest(http.MethodPost, cpaServer.URL+"/v0/management/api-call", bytes.NewReader(keeperBody))
	keeperRequest.Header.Set("Authorization", "Bearer "+managementKey)
	keeperRequest.Header.Set("Content-Type", "application/json")
	keeperResponse, err := cpaServer.Client().Do(keeperRequest)
	if err != nil {
		t.Fatal(err)
	}
	keeperRaw, _ := io.ReadAll(keeperResponse.Body)
	_ = keeperResponse.Body.Close()
	var result struct {
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
	}
	if keeperResponse.StatusCode != http.StatusOK || json.Unmarshal(keeperRaw, &result) != nil || result.StatusCode != http.StatusOK || !strings.Contains(result.Body, `"plan_type":"team"`) {
		t.Fatalf("keeper status=%d response=%s", keeperResponse.StatusCode, keeperRaw)
	}
}
