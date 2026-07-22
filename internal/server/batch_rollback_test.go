package server

import (
	"context"
	"testing"
	"time"

	identitystore "github.com/simplez2/cpa-codex-agent-identity/internal/store"
)

func TestRollbackBatchImportsRemovesStoredCredentials(t *testing.T) {
	t.Parallel()
	store, err := identitystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.Import("at-first", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.Import("at-second", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	items := []batchImportItemResult{
		{IdentityID: first.ID, Status: "imported", ChannelSynced: false},
		{IdentityID: second.ID, Status: "imported", ChannelSynced: false},
	}
	server := &Server{store: store}
	if !server.rollbackBatchImports(context.Background(), []int{0, 1}, items) {
		t.Fatal("rollback unexpectedly failed")
	}
	if len(store.List()) != 0 || items[0].Status != "rolled_back" || items[1].Status != "rolled_back" {
		t.Fatalf("rollback did not remove all credentials: items=%#v stored=%d", items, len(store.List()))
	}
}
