package cpa

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagerUpsertStatusAndRemoveUsesNativeAuthFiles(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	files := map[string][]byte{
		"existing-codex.json":                    []byte(`{"type":"codex","access_token":"existing"}`),
		"codex-agent-identity-aabbccddeeff.json": []byte(`{"type":"codex","auth_mode":"agent_identity_sidecar","agent_identity_id":"agent-aabbccddeeff","access_token":"cais_old","disabled":true}`),
	}
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		name := request.URL.Query().Get("name")
		switch request.URL.Path {
		case "/v0/management/auth-files":
			switch request.Method {
			case http.MethodGet:
				items := make([]map[string]any, 0, len(files))
				for fileName := range files {
					items = append(items, map[string]any{"name": fileName, "auth_index": "index-" + fileName})
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"files": items})
			case http.MethodPost:
				files[name], _ = io.ReadAll(request.Body)
				_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
			case http.MethodDelete:
				if _, ok := files[name]; !ok {
					http.NotFound(writer, request)
					return
				}
				delete(files, name)
				_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
			default:
				http.Error(writer, "method", http.StatusMethodNotAllowed)
			}
		case "/v0/management/auth-files/download":
			data, ok := files[name]
			if !ok {
				http.NotFound(writer, request)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write(data)
		case "/v0/management/auth-files/fields":
			var patch map[string]any
			if request.Method != http.MethodPatch || json.NewDecoder(request.Body).Decode(&patch) != nil {
				http.Error(writer, "bad patch", http.StatusBadRequest)
				return
			}
			fileName, _ := patch["name"].(string)
			var payload map[string]any
			if json.Unmarshal(files[fileName], &payload) != nil {
				http.NotFound(writer, request)
				return
			}
			for key, value := range patch {
				if key != "name" {
					payload[key] = value
				}
			}
			files[fileName], _ = json.Marshal(payload)
			writer.WriteHeader(http.StatusOK)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer service.Close()

	manager, err := NewManager(service.URL+"/v0/management", "management-key", "http://sidecar:8787/backend-api/codex", service.Client())
	if err != nil {
		t.Fatal(err)
	}
	credential := Credential{
		IdentityID: "agent-aabbccddeeff",
		ClientKey:  "cais_secret",
		AccountID:  "account-test",
		UserID:     "user-test",
		Email:      "user@example.invalid",
		PlanType:   "k12",
		ExpiresAt:  time.Unix(2_000_000_000, 0),
	}
	if err = manager.UpsertIdentity(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	status, err := manager.IdentityStatus(context.Background(), []string{credential.IdentityID, "agent-001122334455"})
	if err != nil {
		t.Fatal(err)
	}
	if !status[credential.IdentityID] || status["agent-001122334455"] {
		t.Fatalf("unexpected status: %#v", status)
	}
	states, err := manager.IdentityStates(context.Background(), []string{credential.IdentityID})
	if err != nil || !states[credential.IdentityID].Synced || !states[credential.IdentityID].Disabled {
		t.Fatalf("unexpected initial state: %#v err=%v", states, err)
	}
	if err = manager.SetIdentityDisabled(context.Background(), credential.IdentityID, false); err != nil {
		t.Fatal(err)
	}
	states, err = manager.IdentityStates(context.Background(), []string{credential.IdentityID})
	if err != nil || states[credential.IdentityID].Disabled {
		t.Fatalf("disable state did not update: %#v err=%v", states, err)
	}
	resolvedID, managed, err := manager.IdentityIDForAuthIndex(context.Background(), "index-codex-user@example.invalid-k12.json")
	if err != nil || !managed || resolvedID != credential.IdentityID {
		t.Fatalf("resolved_id=%q managed=%v err=%v", resolvedID, managed, err)
	}
	if resolvedID, managed, err = manager.IdentityIDForAuthIndex(context.Background(), "index-existing-codex.json"); err != nil || managed || resolvedID != "" {
		t.Fatalf("unmanaged resolved_id=%q managed=%v err=%v", resolvedID, managed, err)
	}

	mu.Lock()
	raw := append([]byte(nil), files["codex-user@example.invalid-k12.json"]...)
	if _, legacyExists := files["codex-agent-identity-aabbccddeeff.json"]; legacyExists {
		t.Fatal("legacy hash-named auth file was not migrated")
	}
	if string(files["existing-codex.json"]) != `{"type":"codex","access_token":"existing"}` {
		t.Fatal("unrelated auth file changed")
	}
	mu.Unlock()
	if strings.Contains(string(raw), "codex_access_token") {
		t.Fatal("CPA auth file contains the original Agent Identity field")
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		t.Fatalf("invalid stored payload: %s", raw)
	}
	if payload["auth_mode"] != authMode || payload["access_token"] != credential.ClientKey || payload["base_url"] != "http://sidecar:8787/backend-api/codex" || payload["email"] != credential.Email || payload["plan_type"] != credential.PlanType || payload["disabled"] != false {
		t.Fatalf("unexpected stored payload: %#v", payload)
	}

	if err = manager.RemoveIdentity(context.Background(), credential.IdentityID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := files["codex-user@example.invalid-k12.json"]; exists {
		t.Fatal("managed auth file still exists")
	}
	if _, exists := files["existing-codex.json"]; !exists {
		t.Fatal("unrelated auth file was removed")
	}
}

func TestManagerForwardAPICallPreservesCPAResponse(t *testing.T) {
	t.Parallel()
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v0/management/api-call" || request.Header.Get("Authorization") != "Bearer management-key" {
			http.NotFound(writer, request)
			return
		}
		raw, _ := io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Test", "forwarded")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write(raw)
	}))
	defer service.Close()
	manager, _ := NewManager(service.URL+"/v0/management", "management-key", "http://sidecar", service.Client())
	raw := []byte(`{"auth_index":"oauth-index","method":"GET","url":"https://example.test"}`)
	status, headers, body, err := manager.ForwardAPICall(context.Background(), raw)
	if err != nil || status != http.StatusCreated || headers.Get("X-Test") != "forwarded" || string(body) != string(raw) {
		t.Fatalf("status=%d headers=%v body=%s err=%v", status, headers, body, err)
	}
}

func TestCredentialJSONLabelsPersonalAccessTokenWithoutExposingIt(t *testing.T) {
	t.Parallel()
	manager, err := NewManager("https://example.com/v0/management", "management-key", "http://sidecar/backend-api/codex", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manager.credentialJSON(Credential{
		IdentityID: "agent-aabbccddeeff",
		ClientKey:  "cais_opaque",
		Kind:       "personal_access_token",
		AccountID:  "account-team",
		UserID:     "user-team",
		Email:      "team-user@example.invalid",
		PlanType:   "team",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "at-") {
		t.Fatalf("CPA credential leaked personal access token: %s", raw)
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		t.Fatalf("invalid credential JSON: %s", raw)
	}
	if payload["credential_kind"] != "personal_access_token" || payload["note"] != "Codex Access Token via sidecar" || payload["access_token"] != "cais_opaque" || payload["account_id"] != "account-team" {
		t.Fatalf("unexpected credential payload: %#v", payload)
	}
}

func TestManagerRefusesUnmanagedCollision(t *testing.T) {
	t.Parallel()
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v0/management/auth-files":
			_ = json.NewEncoder(writer).Encode(map[string]any{"files": []map[string]any{{"name": "codex-user@example.invalid-k12.json"}}})
		case "/v0/management/auth-files/download":
			_, _ = writer.Write([]byte(`{"type":"codex","access_token":"user-owned"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer service.Close()
	manager, _ := NewManager(service.URL+"/v0/management", "management-key", "http://sidecar", service.Client())
	err := manager.UpsertIdentity(context.Background(), Credential{IdentityID: "agent-aabbccddeeff", ClientKey: "cais_secret", Email: "user@example.invalid", PlanType: "k12"})
	if err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthFileNameRejectsUnsafeIdentity(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "agent-", "agent-../../x", "other-aabb"} {
		if _, err := authFileName(Credential{IdentityID: value, Email: "user@example.invalid"}); err == nil {
			t.Fatalf("authFileName(%q) succeeded", value)
		}
	}
	name, err := authFileName(Credential{IdentityID: "agent-aabb0011", Email: "User+Test@example.invalid", PlanType: "K12"})
	if err != nil || name != "codex-User+Test@example.invalid-k12.json" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	legacy, err := authFileName(Credential{IdentityID: "agent-aabb0011"})
	if err != nil || legacy != "codex-agent-identity-aabb0011.json" {
		t.Fatalf("legacy name=%q err=%v", legacy, err)
	}
}

func TestNewRequestPreservesManagementBasePath(t *testing.T) {
	t.Parallel()
	manager, _ := NewManager("https://example.com/v0/management", "management-key", "http://sidecar", http.DefaultClient)
	request, err := manager.newRequest(context.Background(), http.MethodGet, "/auth-files/download", nil, "a b.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(request.URL.String())
	if parsed.Path != "/v0/management/auth-files/download" || parsed.Query().Get("name") != "a b.json" {
		t.Fatalf("unexpected URL: %s", parsed)
	}
}
