package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPersonalAccessTokenInspectAndAuthorize(t *testing.T) {
	t.Parallel()
	const token = "at-test-personal-access-token"
	var metadataCalls atomic.Int32
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != personalAccessTokenWhoAmIPath {
			http.NotFound(writer, request)
			return
		}
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		metadataCalls.Add(1)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"email":                      "team-user@example.invalid",
			"chatgpt_user_id":            "user-team",
			"chatgpt_account_id":         "account-team",
			"chatgpt_plan_type":          "team",
			"chatgpt_account_is_fedramp": true,
		})
	}))
	defer service.Close()

	manager := NewManagerWithPersonalAccessTokenAPI("", "", service.URL, service.Client())
	credential, err := manager.Inspect(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Kind != CredentialKindPersonalAccessToken || credential.AccountID != "account-team" || credential.UserID != "user-team" || credential.Email != "team-user@example.invalid" || credential.PlanType != "team" || !credential.FedRAMP || credential.TokenHash == "" {
		t.Fatalf("unexpected credential: %#v", credential)
	}
	authorization, err := manager.Authorize(context.Background(), "agent-test", token, "session-test")
	if err != nil {
		t.Fatal(err)
	}
	if authorization.Header != "Bearer "+token || authorization.AccountID != "account-team" || authorization.Kind != CredentialKindPersonalAccessToken || !authorization.FedRAMP {
		t.Fatalf("unexpected authorization: %#v", authorization)
	}
	if metadataCalls.Load() != 1 {
		t.Fatalf("whoami calls=%d, want 1", metadataCalls.Load())
	}
}

func TestPersonalAccessTokenRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer service.Close()
	manager := NewManagerWithPersonalAccessTokenAPI("", "", service.URL, service.Client())
	if _, err := manager.Inspect(context.Background(), "at-invalid"); err == nil {
		t.Fatal("invalid personal access token was accepted")
	}
}

func TestPersonalAccessTokenClassifierUsesOfficialPrefix(t *testing.T) {
	t.Parallel()
	if !IsPersonalAccessToken("at-example") {
		t.Fatal("official at- prefix was not classified")
	}
	if IsPersonalAccessToken("at_example") || IsPersonalAccessToken("header.payload.signature") {
		t.Fatal("non-official prefix was classified as a personal access token")
	}
}
