package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/simplez2/cpa-codex-agent-identity/internal/identity"
	identitystore "github.com/simplez2/cpa-codex-agent-identity/internal/store"
)

// Real store mutations with a deterministic post-commit durability failure.
// No real credentials, upstream requests or reset operations are used.
type failingDurabilityStore struct {
	*identitystore.Store
	failImport, failCleanup, appliedDelete bool
}

func (s *failingDurabilityStore) ImportWithMetadata(token string, metadata identitystore.CredentialMetadata, now time.Time) (*identitystore.PublicIdentity, string, error) {
	public, key, err := s.Store.ImportWithMetadata(token, metadata, now)
	if err == nil && s.failImport {
		err = &identitystore.AppliedError{Err: errors.New("synthetic directory sync failure")}
	}
	return public, key, err
}

func (s *failingDurabilityStore) Delete(id string) error {
	if s.failCleanup {
		return errors.New("synthetic unlink failure")
	}
	err := s.Store.Delete(id)
	if err == nil && s.appliedDelete {
		return &identitystore.AppliedError{Err: errors.New("synthetic directory sync failure")}
	}
	return err
}

func (s *failingDurabilityStore) Restore(value *identitystore.Identity) error {
	if s.failCleanup {
		return errors.New("synthetic restore failure")
	}
	return s.Store.Restore(value)
}

func newDurabilityStore(t *testing.T) *failingDurabilityStore {
	t.Helper()
	store, err := identitystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &failingDurabilityStore{Store: store}
}

func TestImportAppliedSyncFailureRollsBackCurrentMutation(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "existing"}[existing], func(t *testing.T) {
			store := newDurabilityStore(t)
			const token = "at-durability-fixture"
			var originalKey string
			if existing {
				_, key, err := store.Import(token, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				originalKey = key
			}
			store.failImport = true
			server := &Server{store: store}
			result, err := server.commitInspectedTokenLocked(context.Background(), token, "", &identity.CredentialInfo{}, false)
			if result != nil || err == nil || err.Code != "store_sync_failed" || err.RollbackFailed {
				t.Fatalf("unexpected failure result: %#v", err)
			}
			reopened, openErr := identitystore.Open(store.Directory())
			if openErr != nil {
				t.Fatal(openErr)
			}
			for _, current := range []*identitystore.Store{store.Store, reopened} {
				if existing {
					if _, ok := current.Lookup(originalKey); !ok || len(current.List()) != 1 {
						t.Fatal("failed rotation did not preserve the old identity/key")
					}
				} else if len(current.List()) != 0 {
					t.Fatal("failed fresh import left an unreported identity")
				}
			}
		})
	}
}

func TestAtomicBatchNeverClaimsRollbackAfterCleanupFailure(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup_ok", true: "cleanup_fails"}[cleanupFails], func(t *testing.T) {
			store := newDurabilityStore(t)
			store.failImport, store.failCleanup = true, cleanupFails
			whoami := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]string{
					"chatgpt_account_id": "fixture-team",
					"chatgpt_user_id":    "fixture-user",
					"chatgpt_plan_type":  "team",
				})
			}))
			defer whoami.Close()
			manager := identity.NewManagerWithPersonalAccessTokenAPI("", "", whoami.URL, whoami.Client())
			server := &Server{store: store, manager: manager}
			report := server.processBatchImport(context.Background(), []importCandidate{
				{Index: 1, Token: "at-batch-durability-fixture", AccountID: "fixture-team"},
			}, false, true)
			expected := "rolled_back"
			expectedCount := 0
			if cleanupFails {
				expected, expectedCount = "rollback_failed", 1
			}
			if report.Transaction != expected || len(store.List()) != expectedCount {
				t.Fatalf("transaction=%s remaining=%d", report.Transaction, len(store.List()))
			}
			if cleanupFails && (report.Summary.RollbackFailed != 1 || report.Items[0].Status != "rollback_failed") {
				t.Fatalf("cleanup failure hidden in report: %#v", report)
			}
		})
	}
}

func TestDeleteAppliedSyncFailureReportsRevocationWithoutRollback(t *testing.T) {
	store := newDurabilityStore(t)
	public, key, err := store.Import("at-delete-durability-fixture", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store.appliedDelete = true
	server := &Server{
		store: store, backupDir: t.TempDir(),
		config: Config{ManagementKey: "synthetic-management-key"},
	}
	request := httptest.NewRequest(http.MethodDelete, "/admin/v1/identities/"+public.ID, nil)
	request.Header.Set("Authorization", "Bearer synthetic-management-key")
	recorder := httptest.NewRecorder()
	server.handleIdentity(recorder, request)
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusInternalServerError || body["mutation_applied"] != true || body["rollback"] != "not_attempted" {
		t.Fatalf("applied deletion was not distinguished: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, ok := store.Lookup(key); ok {
		t.Fatal("deleted key remains selectable")
	}
}
