package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImportPersistsClientKeyForReconciliation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	public, key, err := store.Import("header.payload.signature", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if key == "" || public.ID == "" {
		t.Fatal("import omitted identity or client key")
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	items := reopened.ListForSync()
	if len(items) != 1 || items[0].ClientKey != key || items[0].Version != 2 {
		t.Fatalf("unexpected reconciled identity: %#v", items)
	}
	if _, ok := reopened.Lookup(key); !ok {
		t.Fatal("persisted client key cannot select identity")
	}
}

func TestOpenAcceptsLegacyVersionOneWithoutClientKey(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	token := "legacy.token.value"
	digest := sha256.Sum256([]byte("cais_legacy_key"))
	legacy := persistedIdentity{
		Version:       1,
		ID:            IdentityID(token),
		Token:         token,
		ClientKeyHash: hex.EncodeToString(digest[:]),
		CreatedAt:     time.Unix(1_700_000_000, 0),
	}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(directory, "legacy.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	items := store.ListForSync()
	if len(items) != 1 || items[0].Version != 1 || items[0].ClientKey != "" {
		t.Fatalf("unexpected legacy identity: %#v", items)
	}
}

func TestEncryptedStoreDoesNotPersistPlaintextAndReopens(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	key := bytes.Repeat([]byte{0x42}, encryptionKeySize)
	store, err := Open(directory, WithEncryptionKey(key))
	if err != nil {
		t.Fatal(err)
	}
	const token = "at-secret-personal-access-token"
	public, clientKey, err := store.Import(token, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "identity-"+public.ID[len("agent-"):]+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(token)) || bytes.Contains(raw, []byte(`"codex_access_token":`)) {
		t.Fatalf("encrypted identity leaked plaintext: %s", raw)
	}
	var persisted persistedIdentity
	if json.Unmarshal(raw, &persisted) != nil || persisted.Version != identityVersionEncrypted || persisted.TokenNonce == "" || persisted.TokenCiphertext == "" {
		t.Fatalf("unexpected encrypted identity: %s", raw)
	}
	reopened, err := Open(directory, WithEncryptionKey(key))
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := reopened.Lookup(clientKey)
	if !ok || identity.Token != token || identity.ID != public.ID {
		t.Fatalf("encrypted identity did not reopen: %#v", identity)
	}
}

func TestUpdateMetadataKeepsLookupAndPersistenceInSync(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	public, clientKey, err := store.ImportWithMetadata("at-metadata", CredentialMetadata{Kind: "personal_access_token", Email: "old@example.invalid"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Unix(2_000_000_000, 0)
	if err = store.UpdateMetadata(public.ID, CredentialMetadata{Kind: "personal_access_token", Email: "new@example.invalid", PlanType: "team", ExpiresAt: expiresAt}); err != nil {
		t.Fatal(err)
	}
	lookedUp, ok := store.Lookup(clientKey)
	if !ok || lookedUp.Email != "new@example.invalid" || lookedUp.PlanType != "team" || !lookedUp.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("lookup metadata is stale: %#v", lookedUp)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	items := reopened.List()
	if len(items) != 1 || items[0].Email != "new@example.invalid" || items[0].ExpiresAt == nil || !items[0].ExpiresAt.Equal(expiresAt) {
		t.Fatalf("persisted metadata is stale: %#v", items)
	}
}

func TestEncryptedStoreMigratesPlaintextVersionTwo(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	plain, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	const token = "header.payload.signature"
	public, clientKey, err := plain.Import(token, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x24}, encryptionKeySize)
	migrated, err := Open(directory, WithEncryptionKey(key))
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := migrated.Lookup(clientKey)
	if !ok || identity.Version != identityVersionEncrypted || identity.Token != token {
		t.Fatalf("unexpected migrated identity: %#v", identity)
	}
	path := filepath.Join(directory, "identity-"+public.ID[len("agent-"):]+".json")
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte(token)) || !bytes.Contains(raw, []byte(`"version": 3`)) {
		t.Fatalf("migration did not encrypt file: %s", raw)
	}
}

func TestEncryptedStoreRejectsWrongKey(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store, err := Open(directory, WithEncryptionKey(bytes.Repeat([]byte{0x11}, encryptionKeySize)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Import("at-secret", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(directory, WithEncryptionKey(bytes.Repeat([]byte{0x22}, encryptionKeySize))); err == nil {
		t.Fatal("encrypted store opened with the wrong key")
	}
}

func TestParseEncryptionKey(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x33}, encryptionKeySize)
	parsed, err := ParseEncryptionKey(hex.EncodeToString(key))
	if err != nil || !bytes.Equal(parsed, key) {
		t.Fatalf("parse hex key: %x %v", parsed, err)
	}
	if _, err = ParseEncryptionKey("short"); err == nil {
		t.Fatal("invalid encryption key was accepted")
	}
}
