package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
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

func TestPersonalAccessTokenRejectsAccountSelectionMismatch(t *testing.T) {
	t.Parallel()
	const token = "at-mismatch-token"
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("ChatGPT-Account-ID") != "requested-team" {
			t.Fatalf("ChatGPT-Account-ID = %q, want requested-team", request.Header.Get("ChatGPT-Account-ID"))
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"email":              "same-user@example.invalid",
			"chatgpt_user_id":    "same-user",
			"chatgpt_account_id": "different-team",
			"chatgpt_plan_type":  "team",
		})
	}))
	defer service.Close()

	manager := NewManagerWithPersonalAccessTokenAPI("", "", service.URL, service.Client())
	if _, err := manager.InspectForAccount(context.Background(), token, "requested-team"); err != ErrCredentialInvalid {
		t.Fatalf("mismatched Team returned err=%v, want %v", err, ErrCredentialInvalid)
	}
}

func TestPersonalAccessTokenAccountSelectionIsCachedSeparately(t *testing.T) {
	t.Parallel()
	const token = "at-multi-team-token"
	var requests []string
	var mu sync.Mutex
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != personalAccessTokenWhoAmIPath || request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		accountID := request.Header.Get("ChatGPT-Account-ID")
		mu.Lock()
		requests = append(requests, accountID)
		mu.Unlock()
		if accountID == "" {
			accountID = "default-team"
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"email":                      "same-user@example.invalid",
			"chatgpt_user_id":            "same-user",
			"chatgpt_account_id":         accountID,
			"chatgpt_plan_type":          "team",
			"chatgpt_account_is_fedramp": false,
		})
	}))
	defer service.Close()

	manager := NewManagerWithPersonalAccessTokenAPI("", "", service.URL, service.Client())
	first, err := manager.InspectForAccount(context.Background(), token, "team-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.InspectForAccount(context.Background(), token, "team-two")
	if err != nil {
		t.Fatal(err)
	}
	if first.AccountID != "team-one" || second.AccountID != "team-two" {
		t.Fatalf("selected account IDs were not preserved: first=%#v second=%#v", first, second)
	}
	if _, err = manager.AuthorizeForAccount(context.Background(), "agent-team-one", token, "session", "team-one"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0] != "team-one" || requests[1] != "team-two" {
		t.Fatalf("unexpected whoami requests: %#v", requests)
	}
}
