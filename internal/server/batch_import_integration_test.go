package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/simplez2/cpa-codex-agent-identity/internal/identity"
	"github.com/simplez2/cpa-codex-agent-identity/internal/server"
	identitystore "github.com/simplez2/cpa-codex-agent-identity/internal/store"
)

type batchResponseForTest struct {
	Status      string `json:"status"`
	Preview     bool   `json:"preview"`
	Transaction string `json:"transaction"`
	Summary     struct {
		Total               int `json:"total"`
		Ready               int `json:"ready"`
		Imported            int `json:"imported"`
		Duplicate           int `json:"duplicate"`
		Invalid             int `json:"invalid"`
		UpstreamUnavailable int `json:"upstream_unavailable"`
		Aborted             int `json:"aborted"`
	} `json:"summary"`
	Items []struct {
		Status     string `json:"status"`
		IdentityID string `json:"identity_id"`
		Email      string `json:"email"`
	} `json:"items"`
}

func TestBatchPreviewDeduplicatesWithoutPersistingSecrets(t *testing.T) {
	sidecar, store, fixture := newBatchSidecar(t)
	body, _ := json.Marshal([]any{
		map[string]string{"token": fixture.token, "label": "primary"},
		fixture.token,
	})
	response, raw := postBatch(t, sidecar.URL, true, true, body)
	if !response.Preview || response.Summary.Ready != 1 || response.Summary.Duplicate != 1 || len(store.List()) != 0 {
		t.Fatalf("unexpected preview: %#v stored=%d", response, len(store.List()))
	}
	if bytes.Contains(raw, []byte(fixture.token)) {
		t.Fatal("batch preview response leaked the original token")
	}
	if len(response.Items) != 2 || response.Items[0].Email != "u***r@example.invalid" || response.Items[0].IdentityID == "" {
		t.Fatalf("unexpected redacted items: %#v", response.Items)
	}
}

func TestBatchAtomicValidationFailureDoesNotPersistAnything(t *testing.T) {
	sidecar, store, fixture := newBatchSidecar(t)
	body, _ := json.Marshal([]string{fixture.token, "not-a-valid-codex-token"})
	response, _ := postBatch(t, sidecar.URL, false, true, body)
	if response.Status != "validation_failed" || response.Transaction != "aborted" || response.Summary.Invalid != 1 || response.Summary.Aborted != 1 {
		t.Fatalf("unexpected atomic response: %#v", response)
	}
	if len(store.List()) != 0 {
		t.Fatalf("atomic validation failure persisted %d identities", len(store.List()))
	}
}

func TestBatchNonAtomicModeImportsValidItems(t *testing.T) {
	sidecar, store, fixture := newBatchSidecar(t)
	body, _ := json.Marshal([]string{fixture.token, "not-a-valid-codex-token"})
	response, _ := postBatch(t, sidecar.URL, false, false, body)
	if response.Summary.Imported != 1 || response.Summary.Invalid != 1 || response.Transaction != "committed" {
		t.Fatalf("unexpected non-atomic response: %#v", response)
	}
	if len(store.List()) != 1 {
		t.Fatalf("non-atomic import stored %d identities", len(store.List()))
	}
}

func newBatchSidecar(t *testing.T) (*httptest.Server, *identitystore.Store, agentFixture) {
	t.Helper()
	fixture := newAgentFixture(t)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(fixture.jwks)
	}))
	t.Cleanup(jwksServer.Close)
	credentialStore, err := identitystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := identity.NewManager(jwksServer.URL, jwksServer.URL, jwksServer.Client())
	upstreamURL, _ := url.Parse(jwksServer.URL)
	handler, err := server.New(server.Config{
		UpstreamOrigin: upstreamURL,
		ManagementKey:  "management-key-at-least-24-characters",
	}, credentialStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(handler)
	t.Cleanup(sidecar.Close)
	return sidecar, credentialStore, fixture
}

func postBatch(t *testing.T, sidecarURL string, preview, atomic bool, body []byte) (batchResponseForTest, []byte) {
	t.Helper()
	endpoint := sidecarURL + "/agent-identity/api/identities/import/batch?preview=" + boolText(preview) + "&atomic=" + boolText(atomic)
	request, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer management-key-at-least-24-characters")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("batch status=%d body=%s", response.StatusCode, raw)
	}
	var decoded batchResponseForTest
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("invalid batch response: %s", raw)
	}
	return decoded, raw
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
