package cpa

import (
	"bytes"
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

func TestManagerProbeReportsSafeCPAStates(t *testing.T) {
	t.Parallel()
	const managementKey = "management-key"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v0/management/auth-files" {
			http.NotFound(writer, request)
			return
		}
		switch request.URL.Query().Get("mode") {
		case "unauthorized":
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
		case "forbidden":
			http.Error(writer, "forbidden", http.StatusForbidden)
		case "invalid":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("not-json"))
		case "error":
			http.Error(writer, "broken", http.StatusBadGateway)
		default:
			_ = json.NewEncoder(writer).Encode(map[string]any{"files": []any{}})
		}
	}))
	defer server.Close()

	for _, test := range []struct {
		name      string
		mode      string
		wantState ProbeState
		wantReach bool
	}{
		{name: "ready", wantState: ProbeStateReady, wantReach: true},
		{name: "unauthorized", mode: "unauthorized", wantState: ProbeStateUnauthorized, wantReach: true},
		{name: "forbidden", mode: "forbidden", wantState: ProbeStateUnauthorized, wantReach: true},
		{name: "invalid response", mode: "invalid", wantState: ProbeStateError, wantReach: true},
		{name: "server error", mode: "error", wantState: ProbeStateError, wantReach: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := server.URL + "/v0/management"
			if test.mode != "" {
				base += "?mode=" + test.mode
			}
			manager, err := NewManager(base, managementKey, "http://sidecar:8787/backend-api/codex", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			result := manager.Probe(context.Background())
			if !result.Configured || result.Reachable != test.wantReach || result.State != test.wantState {
				t.Fatalf("probe=%#v, want configured=true reachable=%v state=%q", result, test.wantReach, test.wantState)
			}
		})
	}

	var manager *Manager
	if result := manager.Probe(context.Background()); result.Configured || result.Reachable || result.State != ProbeStateNotConfigured {
		t.Fatalf("nil manager probe=%#v", result)
	}
}

func TestManagerProbeReportsUnreachableWithoutSensitiveDetails(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL + "/v0/management"
	server.Close()
	manager, err := NewManager(endpoint, "management-key", "http://sidecar:8787/backend-api/codex", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	result := manager.Probe(context.Background())
	if !result.Configured || result.Reachable || result.State != ProbeStateUnreachable {
		t.Fatalf("unreachable probe=%#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "management-key") || strings.Contains(string(encoded), endpoint) {
		t.Fatalf("probe leaked sensitive details: %s err=%v", encoded, err)
	}
}

func TestValidateCredentialRequiresValidClientKey(t *testing.T) {
	t.Parallel()
	base := Credential{IdentityID: "agent-aabb0011", UpstreamToken: "upstream-token"}
	cases := []struct {
		name string
		key  string
		want bool
	}{
		{name: "empty", key: "", want: false},
		{name: "short", key: "cais_short", want: false},
		{name: "wrong prefix", key: "token_0000000000000000000000000000", want: false},
		{name: "newline", key: "cais_0000000000000000000000000000\n", want: false},
		{name: "carriage return", key: "cais_0000000000000000000000000000\r", want: false},
		{name: "valid", key: "cais_test_0000000000000000000000000000", want: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			credential := base
			credential.ClientKey = test.key
			err := validateCredential(credential)
			if (err == nil) != test.want {
				t.Fatalf("validateCredential(%q) error=%v, want valid=%v", test.key, err, test.want)
			}
		})
	}
}

func TestCredentialJSONMarshalOmitsSecrets(t *testing.T) {
	t.Parallel()
	const (
		clientKey     = "cais_secret_0000000000000000000000000000"
		upstreamToken = "upstream-secret-token"
	)
	raw, err := json.Marshal(Credential{
		IdentityID:    "agent-aabb0011",
		ClientKey:     clientKey,
		UpstreamToken: upstreamToken,
		Email:         "user@example.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(clientKey)) || bytes.Contains(raw, []byte(upstreamToken)) || bytes.Contains(raw, []byte("ClientKey")) || bytes.Contains(raw, []byte("UpstreamToken")) {
		t.Fatalf("credential JSON leaked secret-bearing fields: %s", raw)
	}
}

func TestValidateCredentialRequiresSafeUpstreamToken(t *testing.T) {
	t.Parallel()
	base := Credential{IdentityID: "agent-aabb0011", ClientKey: "cais_test_0000000000000000000000000000"}
	for _, test := range []struct {
		name  string
		token string
		want  bool
	}{
		{name: "empty", token: "", want: false},
		{name: "newline", token: "upstream\ntoken", want: false},
		{name: "carriage return", token: "upstream\rtoken", want: false},
		{name: "personal access token", token: "at-valid-team-token", want: true},
		{name: "agent identity jwt", token: "header.payload.signature", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			credential := base
			credential.UpstreamToken = test.token
			err := validateCredential(credential)
			if (err == nil) != test.want {
				t.Fatalf("upstream token validation error=%v, want valid=%v", err, test.want)
			}
		})
	}
}

func TestIdentityIDForAuthIndexAcceptsStringAndNumericForms(t *testing.T) {
	t.Parallel()
	const managementKey = "management-key"
	const firstID = "agent-aabb0011"
	const secondID = "agent-aabb0022"
	const thirdID = "agent-aabb0033"
	files := map[string][]byte{
		"codex-first.json":  []byte(`{"type":"codex-agent-identity","auth_mode":"agent_identity_sidecar","agent_identity_id":"agent-aabb0011"}`),
		"codex-second.json": []byte(`{"type":"codex-agent-identity","auth_mode":"agent_identity_sidecar","agent_identity_id":"agent-aabb0022"}`),
		"codex-third.json":  []byte(`{"type":"codex-agent-identity","auth_mode":"agent_identity_sidecar","agent_identity_id":"agent-aabb0033"}`),
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+managementKey {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		name := request.URL.Query().Get("name")
		switch request.URL.Path {
		case "/v0/management/auth-files":
			_ = json.NewEncoder(writer).Encode(map[string]any{"files": []map[string]any{
				{"name": "codex-first.json", "auth_index": 7},
				{"name": "codex-second.json", "authIndex": "8"},
				{"name": "codex-third.json", "AuthIndex": 9},
			}})
		case "/v0/management/auth-files/download":
			if raw, ok := files[name]; ok {
				_, _ = writer.Write(raw)
				return
			}
			http.NotFound(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	manager, err := NewManager(server.URL+"/v0/management", managementKey, "http://sidecar:8787/backend-api/codex", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		index string
		id    string
	}{
		{index: "7", id: firstID},
		{index: "8", id: secondID},
		{index: "9", id: thirdID},
	} {
		id, managed, err := manager.IdentityIDForAuthIndex(context.Background(), test.index)
		if err != nil || !managed || id != test.id {
			t.Fatalf("auth index %q resolved id=%q managed=%v err=%v", test.index, id, managed, err)
		}
	}
}

func TestManagerUpsertStatusAndRemoveUsesNativeAuthFiles(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	files := map[string][]byte{
		"existing-codex.json":                    []byte(`{"type":"codex","access_token":"existing"}`),
		"codex-agent-identity-aabbccddeeff.json": []byte(`{"type":"codex","auth_mode":"agent_identity_sidecar","agent_identity_id":"agent-aabbccddeeff","access_token":"cais_old_0000000000000000000000000000","disabled":true}`),
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
		IdentityID:    "agent-aabbccddeeff",
		ClientKey:     "cais_secret_0000000000000000000000000000",
		UpstreamToken: "agent.identity.jwt",
		Kind:          "agent_identity",
		AccountID:     "account-test",
		UserID:        "user-test",
		Email:         "user@example.invalid",
		PlanType:      "k12",
		ExpiresAt:     time.Unix(2_000_000_000, 0),
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
	resolvedID, managed, err := manager.IdentityIDForAuthIndex(context.Background(), "index-codex-d86f70b3-user@example.invalid-k12-agent-identity.json")
	if err != nil || !managed || resolvedID != credential.IdentityID {
		t.Fatalf("resolved_id=%q managed=%v err=%v", resolvedID, managed, err)
	}
	if resolvedID, managed, err = manager.IdentityIDForAuthIndex(context.Background(), "index-existing-codex.json"); err != nil || managed || resolvedID != "" {
		t.Fatalf("unmanaged resolved_id=%q managed=%v err=%v", resolvedID, managed, err)
	}

	mu.Lock()
	raw := append([]byte(nil), files["codex-d86f70b3-user@example.invalid-k12-agent-identity.json"]...)
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
	if payload["type"] != pluginProviderID || payload["auth_mode"] != authMode || payload["auth_kind"] != "oauth" || payload["access_token"] != credential.UpstreamToken || payload[sidecarClientKeyField] != credential.ClientKey || payload["base_url"] != "http://sidecar:8787/backend-api/codex" || payload["email"] != credential.Email || payload["plan_type"] != credential.PlanType || payload["disabled"] != false {
		t.Fatalf("unexpected stored payload: %#v", payload)
	}

	if err = manager.RemoveIdentity(context.Background(), credential.IdentityID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := files["codex-d86f70b3-user@example.invalid-k12-agent-identity.json"]; exists {
		t.Fatal("managed auth file still exists")
	}
	if _, exists := files["existing-codex.json"]; !exists {
		t.Fatal("unrelated auth file was removed")
	}
}

func TestManagedCredentialIdentityRecognizesNewAndLegacyFormats(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "legacy plugin provider", raw: `{"type":"codex-agent-identity","auth_mode":"agent_identity_sidecar","agent_identity_id":"agent-aabbccddeeff"}`, want: true},
		{name: "legacy native provider", raw: `{"type":"codex","auth_mode":"agent_identity_sidecar","agent_identity_id":"agent-aabbccddeeff"}`, want: true},
		{name: "ordinary codex oauth", raw: `{"type":"codex","access_token":"oauth-token"}`, want: false},
		{name: "other provider", raw: `{"type":"gemini","auth_mode":"agent_identity_sidecar","agent_identity_id":"agent-aabbccddeeff"}`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identityID, managed := managedCredentialIdentity([]byte(tc.raw))
			if managed != tc.want || (managed && identityID != "agent-aabbccddeeff") {
				t.Fatalf("identity=%q managed=%v, want managed=%v: %s", identityID, managed, tc.want, tc.raw)
			}
		})
	}
}

func TestManagedCredentialDisabledAcceptsCompatibleBooleanShapes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "json true", raw: `{"disabled":true}`, want: true},
		{name: "string true", raw: `{"disabled":"true"}`, want: true},
		{name: "string one", raw: `{"disabled":"1"}`, want: true},
		{name: "string yes", raw: `{"disabled":"yes"}`, want: true},
		{name: "number one", raw: `{"disabled":1}`, want: true},
		{name: "json false", raw: `{"disabled":false}`, want: false},
		{name: "string false", raw: `{"disabled":"false"}`, want: false},
		{name: "invalid value", raw: `{"disabled":"sometimes"}`, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := managedCredentialDisabled([]byte(test.raw), true); got != test.want {
				t.Fatalf("managedCredentialDisabled(%s) = %v, want %v", test.raw, got, test.want)
			}
		})
	}
	if managedCredentialDisabled([]byte(`{"disabled":true}`), false) {
		t.Fatal("missing auth file must not report disabled")
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
	manager, _ := NewManager(service.URL+"/v0/management", "management-key", "http://sidecar:8787/backend-api/codex", service.Client())
	raw := []byte(`{"auth_index":"oauth-index","method":"GET","url":"https://example.test"}`)
	status, headers, body, err := manager.ForwardAPICall(context.Background(), raw)
	if err != nil || status != http.StatusCreated || headers.Get("X-Test") != "forwarded" || string(body) != string(raw) {
		t.Fatalf("status=%d headers=%v body=%s err=%v", status, headers, body, err)
	}
}

func TestCredentialJSONExposesPersonalAccessTokenOnlyToCPANativeAuthFile(t *testing.T) {
	t.Parallel()
	manager, err := NewManager("https://example.com/v0/management", "management-key", "http://sidecar:8787/backend-api/codex", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manager.credentialJSON(Credential{
		IdentityID:    "agent-aabbccddeeff",
		ClientKey:     "cais_opaque_0000000000000000000000000000",
		UpstreamToken: "at-team-personal-access-token",
		Kind:          "personal_access_token",
		AccountID:     "account-team",
		UserID:        "user-team",
		Email:         "team-user@example.invalid",
		PlanType:      "team",
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		t.Fatalf("invalid credential JSON: %s", raw)
	}
	if payload["credential_kind"] != "personal_access_token" || payload["note"] != "Codex Access Token via sidecar" || payload["access_token"] != "at-team-personal-access-token" || payload[sidecarClientKeyField] != "cais_opaque_0000000000000000000000000000" || payload["account_id"] != "account-team" {
		t.Fatalf("unexpected credential payload: %#v", payload)
	}
}

func TestManagerRefusesUnmanagedCollision(t *testing.T) {
	t.Parallel()
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v0/management/auth-files":
			_ = json.NewEncoder(writer).Encode(map[string]any{"files": []map[string]any{{"name": "codex-user@example.invalid-k12-agent-identity.json"}}})
		case "/v0/management/auth-files/download":
			_, _ = writer.Write([]byte(`{"type":"codex","access_token":"user-owned"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer service.Close()
	manager, _ := NewManager(service.URL+"/v0/management", "management-key", "http://sidecar:8787/backend-api/codex", service.Client())
	err := manager.UpsertIdentity(context.Background(), Credential{IdentityID: "agent-aabbccddeeff", ClientKey: "cais_secret_0000000000000000000000000000", UpstreamToken: "upstream-token", Email: "user@example.invalid", PlanType: "k12"})
	if err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManagerAllowsNativeOAuthAndMultipleTeamsWithSameEmail(t *testing.T) {
	t.Parallel()
	const nativeName = "codex-58732bd9-user@example.invalid-team.json"
	files := map[string][]byte{
		nativeName: []byte(`{"type":"codex","access_token":"native-oauth"}`),
	}
	var mu sync.Mutex
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
			_, _ = writer.Write(data)
		case "/v0/management/auth-files/fields":
			var patch map[string]any
			if request.Method != http.MethodPatch || json.NewDecoder(request.Body).Decode(&patch) != nil {
				http.Error(writer, "bad patch", http.StatusBadRequest)
				return
			}
			if patchedName, ok := patch["name"].(string); ok {
				name = patchedName
			}
			data, ok := files[name]
			if !ok {
				http.NotFound(writer, request)
				return
			}
			var payload map[string]any
			if json.Unmarshal(data, &payload) != nil {
				http.Error(writer, "bad json", http.StatusBadRequest)
				return
			}
			for key, value := range patch {
				if key != "name" {
					payload[key] = value
				}
			}
			files[name], _ = json.Marshal(payload)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer service.Close()

	manager, err := NewManager(service.URL+"/v0/management", "management-key", "http://sidecar:8787/backend-api/codex", service.Client())
	if err != nil {
		t.Fatal(err)
	}
	first := Credential{
		IdentityID:    "agent-aabb0011",
		ClientKey:     "cais_secret_0000000000000000000000000000",
		UpstreamToken: "upstream-team-token",
		Kind:          "personal_access_token",
		AccountID:     "workspace-one",
		Email:         "user@example.invalid",
		PlanType:      "team",
	}
	second := Credential{
		IdentityID:    "agent-aabb0022",
		ClientKey:     "cais_secret_1111111111111111111111111111",
		UpstreamToken: first.UpstreamToken,
		Kind:          first.Kind,
		AccountID:     "workspace-two",
		Email:         first.Email,
		PlanType:      first.PlanType,
	}
	firstName, err := authFileName(first)
	if err != nil {
		t.Fatal(err)
	}
	secondName, err := authFileName(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstName == nativeName || secondName == nativeName || firstName == secondName {
		t.Fatalf("managed filenames collided: native=%q first=%q second=%q", nativeName, firstName, secondName)
	}
	if err = manager.UpsertIdentity(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err = manager.UpsertIdentity(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if string(files[nativeName]) != `{"type":"codex","access_token":"native-oauth"}` {
		t.Fatal("native OAuth credential was changed")
	}
	for _, item := range []struct {
		name       string
		credential Credential
	}{
		{name: firstName, credential: first},
		{name: secondName, credential: second},
	} {
		managedRaw, ok := files[item.name]
		if !ok {
			t.Fatalf("sidecar-managed file %q was not created", item.name)
		}
		if !isManagedCredential(managedRaw, item.credential.IdentityID) {
			t.Fatalf("created file %q is not sidecar-managed", item.name)
		}
		var payload map[string]any
		if json.Unmarshal(managedRaw, &payload) != nil || payload["access_token"] != item.credential.UpstreamToken || payload[sidecarClientKeyField] != item.credential.ClientKey || payload["account_id"] != item.credential.AccountID {
			t.Fatalf("created file %q did not preserve its Team-scoped credential mapping", item.name)
		}
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
	if err != nil || name != "codex-User+Test@example.invalid-k12-agent-identity.json" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	legacy, err := authFileName(Credential{IdentityID: "agent-aabb0011"})
	if err != nil || legacy != "codex-agent-identity-aabb0011.json" {
		t.Fatalf("legacy name=%q err=%v", legacy, err)
	}
}

func TestAuthFileNameSeparatesSameEmailTeamWorkspaces(t *testing.T) {
	t.Parallel()
	first, err := authFileName(Credential{
		IdentityID: "agent-aabb0011",
		AccountID:  "workspace-one",
		Email:      "user@example.invalid",
		PlanType:   "team",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := authFileName(Credential{
		IdentityID: "agent-aabb0022",
		AccountID:  "workspace-two",
		Email:      "user@example.invalid",
		PlanType:   "team",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("same-email Team workspaces collided: %q", first)
	}
	if first != "codex-df52114f-user@example.invalid-team-agent-identity.json" {
		t.Fatalf("unexpected first workspace name: %q", first)
	}
	if second != "codex-831ad5d8-user@example.invalid-team-agent-identity.json" {
		t.Fatalf("unexpected second workspace name: %q", second)
	}
}

func TestNewRequestPreservesManagementBasePath(t *testing.T) {
	t.Parallel()
	manager, _ := NewManager("https://example.com/v0/management", "management-key", "http://sidecar:8787/backend-api/codex", http.DefaultClient)
	request, err := manager.newRequest(context.Background(), http.MethodGet, "/auth-files/download", nil, "a b.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(request.URL.String())
	if parsed.Path != "/v0/management/auth-files/download" || parsed.Query().Get("name") != "a b.json" {
		t.Fatalf("unexpected URL: %s", parsed)
	}
}

func TestNewManagerValidatesSidecarBaseURL(t *testing.T) {
	t.Parallel()
	valid := []string{
		"http://127.0.0.1:8787/backend-api/codex",
		"http://sidecar:8787/backend-api/codex/",
		"https://sidecar.internal/backend-api/codex",
	}
	for _, endpoint := range valid {
		manager, err := NewManager("https://example.com/v0/management", "management-key", endpoint, http.DefaultClient)
		if err != nil {
			t.Fatalf("NewManager(%q): %v", endpoint, err)
		}
		if manager.sidecarBaseURL != strings.TrimRight(endpoint, "/") {
			t.Fatalf("normalized sidecar URL = %q, want %q", manager.sidecarBaseURL, strings.TrimRight(endpoint, "/"))
		}
	}

	invalid := []string{
		"http://sidecar:8787",
		"http://sidecar:8787/backend-api/codex?token=x",
		"http://sidecar:8787/backend-api/codex#fragment",
		"http://user:pass@sidecar:8787/backend-api/codex",
		"//sidecar:8787/backend-api/codex",
		"ftp://sidecar/backend-api/codex",
		"http://sidecar:8787/backend-api/%63odex",
	}
	for _, endpoint := range invalid {
		if _, err := NewManager("https://example.com/v0/management", "management-key", endpoint, http.DefaultClient); err == nil {
			t.Fatalf("NewManager accepted invalid sidecar URL %q", endpoint)
		}
	}
}

type deleteCommitThenDisconnectTransport struct {
	base    http.RoundTripper
	oldName string
}

func (transport *deleteCommitThenDisconnectTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodDelete && request.URL.Query().Get("name") == transport.oldName {
		response, err := transport.base.RoundTrip(request)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		if err != nil {
			return nil, err
		}
		return nil, io.ErrUnexpectedEOF
	}
	return transport.base.RoundTrip(request)
}

func TestUpsertMigrationRestoresOldFileWhenDeleteDisconnectsAfterCommit(t *testing.T) {
	const managementKey = "management-key"
	credential := Credential{
		IdentityID:    "agent-aabb0044",
		ClientKey:     "cais_test_0000000000000000000000000000",
		UpstreamToken: "upstream-rollback-token",
		AccountID:     "workspace-rollback",
		Email:         "rollback@example.invalid",
		PlanType:      "team",
	}
	oldName := "codex-rollback@example.invalid-team-agent-identity.json"
	oldRaw := []byte(`{"type":"codex-agent-identity","auth_mode":"agent_identity_sidecar","agent_identity_id":"agent-aabb0044","access_token":"cais_old_0000000000000000000000000000","account_id":"workspace-rollback","email":"rollback@example.invalid","plan_type":"team"}`)
	files := map[string][]byte{oldName: append([]byte(nil), oldRaw...)}
	var mu sync.Mutex
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+managementKey {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		name := request.URL.Query().Get("name")
		mu.Lock()
		defer mu.Unlock()
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
				writer.WriteHeader(http.StatusOK)
			case http.MethodDelete:
				if _, ok := files[name]; !ok {
					http.NotFound(writer, request)
					return
				}
				delete(files, name)
				writer.WriteHeader(http.StatusOK)
			}
		case "/v0/management/auth-files/download":
			if raw, ok := files[name]; ok {
				_, _ = writer.Write(raw)
				return
			}
			http.NotFound(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer service.Close()

	transport := &deleteCommitThenDisconnectTransport{base: service.Client().Transport, oldName: oldName}
	client := &http.Client{Transport: transport}
	manager, err := NewManager(service.URL+"/v0/management", managementKey, "http://sidecar:8787/backend-api/codex", client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.UpsertIdentity(context.Background(), credential); err == nil {
		t.Fatal("expected migration failure after simulated delete disconnect")
	}
	canonicalName, err := authFileName(credential)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if string(files[oldName]) != string(oldRaw) {
		t.Fatalf("old auth file was not restored: %s", files[oldName])
	}
	if _, ok := files[canonicalName]; ok {
		t.Fatalf("canonical auth file remained after rollback: %q", canonicalName)
	}
}
