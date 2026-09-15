package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testChannelTimeout = 10 * time.Second

type storeWriteResult struct {
	public *PublicIdentity
	key    string
	err    error
}

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

func TestMutationAppliedRecognizesWrappedAppliedError(t *testing.T) {
	t.Parallel()
	cause := errors.New("forced durability failure")
	applied := &AppliedError{Err: cause}
	if applied.Error() != cause.Error() {
		t.Fatalf("applied error changed its cause text: %q", applied.Error())
	}
	if !errors.Is(applied, cause) {
		t.Fatal("applied error does not preserve errors.Is")
	}
	if !MutationApplied(applied) {
		t.Fatal("direct applied error was not recognized")
	}
	if !MutationApplied(fmt.Errorf("outer context: %w", applied)) {
		t.Fatal("wrapped applied error was not recognized")
	}
	if MutationApplied(cause) {
		t.Fatal("ordinary error was reported as an applied mutation")
	}
}

func TestWriteOwnerOnlyAtomicReplacesExistingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(path, []byte("old identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	replaced, err := writeOwnerOnlyAtomic(path, []byte("new identity"))
	if err != nil {
		t.Fatalf("replace existing identity file: %v", err)
	}
	if !replaced {
		t.Fatal("successful replacement was not reported as applied")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new identity" {
		t.Fatalf("existing identity file was not replaced: %q", data)
	}
}

func TestWriteOwnerOnlyAtomicRenameFailurePreservesTarget(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	replaced, err := writeOwnerOnlyAtomic(path, []byte("replacement identity"))
	if err == nil {
		t.Fatal("replacing a directory with an identity file unexpectedly succeeded")
	}
	if replaced {
		t.Fatal("failed rename was reported as applied")
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatalf("rename failure removed the previous target: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("rename failure changed the previous target type: %v", info.Mode())
	}
	entries, readErr := os.ReadDir(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("rename failure changed the empty target directory: %#v", entries)
	}
}

func TestIdentityIDForAccountScopesSamePATByWorkspace(t *testing.T) {
	t.Parallel()
	const token = "at-same-personal-access-token"
	first := IdentityIDForAccount(token, "team-one")
	second := IdentityIDForAccount(token, "team-two")
	if first == second {
		t.Fatalf("different workspaces collided: %q", first)
	}
	if first != IdentityIDForAccount(token, " team-one ") {
		t.Fatal("account ID whitespace was not normalized")
	}
	if IdentityIDForAccount(token, "") != IdentityID(token) {
		t.Fatal("empty account ID did not preserve legacy identity ID")
	}
}

func TestAccountScopedIdentityPersistsAndDetectsOnlySameWorkspaceAsDuplicate(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store, err := Open(directory, WithEncryptionKey(bytes.Repeat([]byte{0x51}, encryptionKeySize)))
	if err != nil {
		t.Fatal(err)
	}
	const token = "at-multi-team"
	first, firstKey, err := store.ImportWithMetadata(token, CredentialMetadata{
		Kind:          "personal_access_token",
		AccountID:     "team-one",
		AccountScoped: true,
	}, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, secondKey, err := store.ImportWithMetadata(token, CredentialMetadata{
		Kind:          "personal_access_token",
		AccountID:     "team-two",
		AccountScoped: true,
	}, time.Unix(1_700_000_001, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || firstKey == secondKey || len(store.List()) != 2 {
		t.Fatalf("same PAT was not separated by workspace: first=%#v second=%#v", first, second)
	}
	if _, ok := store.LookupByTokenAndAccount(token, "team-one"); !ok {
		t.Fatal("team-one identity was not found")
	}
	if _, ok := store.LookupByTokenAndAccount(token, "team-two"); !ok {
		t.Fatal("team-two identity was not found")
	}
	if _, ok := store.LookupByTokenAndAccount(token, "team-three"); ok {
		t.Fatal("unimported workspace was treated as duplicate")
	}

	reopened, err := Open(directory, WithEncryptionKey(bytes.Repeat([]byte{0x51}, encryptionKeySize)))
	if err != nil {
		t.Fatal(err)
	}
	firstIdentity, ok := reopened.Lookup(firstKey)
	if !ok || firstIdentity.AccountID != "team-one" || !firstIdentity.AccountScoped || firstIdentity.Token != token {
		t.Fatalf("team-one identity did not persist: %#v", firstIdentity)
	}
	secondIdentity, ok := reopened.Lookup(secondKey)
	if !ok || secondIdentity.AccountID != "team-two" || !secondIdentity.AccountScoped || secondIdentity.Token != token {
		t.Fatalf("team-two identity did not persist: %#v", secondIdentity)
	}
}

func TestConcurrentSameIdentityWriteAndDeleteStayConsistent(t *testing.T) {
	testCases := []struct {
		name      string
		encrypted bool
		restore   bool
	}{
		{name: "plaintext_import", restore: false},
		{name: "encrypted_restore", encrypted: true, restore: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			var encryptionKey []byte
			if testCase.encrypted {
				encryptionKey = bytes.Repeat([]byte{0x61}, encryptionKeySize)
			}
			openStore := func() (*Store, error) {
				if encryptionKey == nil {
					return Open(directory)
				}
				return Open(directory, WithEncryptionKey(encryptionKey))
			}
			store, err := openStore()
			if err != nil {
				t.Fatal(err)
			}
			const token = "at-concurrent-multi-team"
			targetMetadata := CredentialMetadata{
				Kind:          "personal_access_token",
				AccountID:     "team-one",
				AccountScoped: true,
			}
			targetPublic, targetKey, err := store.ImportWithMetadata(token, targetMetadata, time.Unix(1_700_000_000, 0))
			if err != nil {
				t.Fatal(err)
			}
			targetSnapshot, ok := store.GetByID(targetPublic.ID)
			if !ok {
				t.Fatal("target identity snapshot is missing")
			}
			controlPublic, controlKey, err := store.ImportWithMetadata(token, CredentialMetadata{
				Kind:          "personal_access_token",
				AccountID:     "team-two",
				AccountScoped: true,
			}, time.Unix(1_700_000_001, 0))
			if err != nil {
				t.Fatal(err)
			}

			realWrite := store.writeFile
			writeCommitted := make(chan struct{})
			allowWriteReturn := make(chan struct{})
			var signalCommitted sync.Once
			var releaseWriteOnce sync.Once
			releaseWrite := func() {
				releaseWriteOnce.Do(func() {
					close(allowWriteReturn)
				})
			}
			t.Cleanup(releaseWrite)
			store.writeFile = func(path string, data []byte) (bool, error) {
				replaced, writeErr := realWrite(path, data)
				signalCommitted.Do(func() {
					close(writeCommitted)
				})
				if releaseErr := waitForTestRelease(allowWriteReturn, "identity write release"); writeErr == nil && releaseErr != nil {
					writeErr = releaseErr
				}
				return replaced, writeErr
			}

			writeDone := make(chan storeWriteResult, 1)
			if testCase.restore {
				go func() {
					writeDone <- storeWriteResult{err: store.Restore(targetSnapshot)}
				}()
			} else {
				go func() {
					public, key, importErr := store.ImportWithMetadata(token, targetMetadata, time.Unix(1_700_000_002, 0))
					writeDone <- storeWriteResult{public: public, key: key, err: importErr}
				}()
			}
			waitForTestSignal(t, writeCommitted, "identity write commit")
			if store.mu.TryLock() {
				store.mu.Unlock()
				releaseWrite()
				_ = waitForTestValue(t, writeDone, "identity write completion")
				t.Fatal("identity file replacement completed without holding the store mutation lock")
			}

			deleteStarted := make(chan struct{})
			deleteDone := make(chan error, 1)
			go func() {
				close(deleteStarted)
				deleteDone <- store.Delete(targetPublic.ID)
			}()
			waitForTestSignal(t, deleteStarted, "identity delete start")
			releaseWrite()
			result := waitForTestValue(t, writeDone, "identity write completion")
			if result.err != nil {
				t.Fatalf("write identity: %v", result.err)
			}
			if err = waitForTestValue(t, deleteDone, "identity delete completion"); err != nil {
				t.Fatalf("delete identity: %v", err)
			}

			keysThatMustBeRevoked := []string{targetKey}
			if result.key != "" {
				keysThatMustBeRevoked = append(keysThatMustBeRevoked, result.key)
			}
			assertOnlyControlIdentity(t, store, targetPublic.ID, controlPublic.ID, controlKey, keysThatMustBeRevoked)

			reopened, err := openStore()
			if err != nil {
				t.Fatalf("reopen store: %v", err)
			}
			assertOnlyControlIdentity(t, reopened, targetPublic.ID, controlPublic.ID, controlKey, keysThatMustBeRevoked)
		})
	}
}

func TestConcurrentSameAccountImportsStayConsistent(t *testing.T) {
	directory := t.TempDir()
	encryptionKey := bytes.Repeat([]byte{0x62}, encryptionKeySize)
	openStore := func() (*Store, error) {
		return Open(directory, WithEncryptionKey(encryptionKey))
	}
	store, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	const token = "at-concurrent-same-account-imports"
	metadata := CredentialMetadata{
		Kind:          "personal_access_token",
		AccountID:     "team-one",
		AccountScoped: true,
	}

	realWrite := store.writeFile
	firstCommitted := make(chan struct{})
	secondCommitted := make(chan struct{})
	allowFirstReturn := make(chan struct{})
	allowSecondReturn := make(chan struct{})
	var writeCount atomic.Int32
	var releaseFirstOnce sync.Once
	var releaseSecondOnce sync.Once
	releaseFirst := func() {
		releaseFirstOnce.Do(func() {
			close(allowFirstReturn)
		})
	}
	releaseSecond := func() {
		releaseSecondOnce.Do(func() {
			close(allowSecondReturn)
		})
	}
	t.Cleanup(releaseFirst)
	t.Cleanup(releaseSecond)
	store.writeFile = func(path string, data []byte) (bool, error) {
		replaced, writeErr := realWrite(path, data)
		var release <-chan struct{}
		var description string
		switch writeCount.Add(1) {
		case 1:
			close(firstCommitted)
			release = allowFirstReturn
			description = "first import release"
		case 2:
			close(secondCommitted)
			release = allowSecondReturn
			description = "second import release"
		default:
			return replaced, errors.New("unexpected additional identity write")
		}
		if releaseErr := waitForTestRelease(release, description); writeErr == nil && releaseErr != nil {
			writeErr = releaseErr
		}
		return replaced, writeErr
	}

	firstDone := make(chan storeWriteResult, 1)
	go func() {
		public, key, importErr := store.ImportWithMetadata(token, metadata, time.Unix(1_700_000_000, 0))
		firstDone <- storeWriteResult{public: public, key: key, err: importErr}
	}()
	waitForTestSignal(t, firstCommitted, "first import commit")
	if store.mu.TryLock() {
		store.mu.Unlock()
		releaseFirst()
		_ = waitForTestValue(t, firstDone, "first import completion")
		t.Fatal("first identity import replaced its file without holding the store mutation lock")
	}

	secondStarted := make(chan struct{})
	secondDone := make(chan storeWriteResult, 1)
	go func() {
		close(secondStarted)
		public, key, importErr := store.ImportWithMetadata(token, metadata, time.Unix(1_700_000_001, 0))
		secondDone <- storeWriteResult{public: public, key: key, err: importErr}
	}()
	waitForTestSignal(t, secondStarted, "second import start")
	releaseFirst()
	first := waitForTestValue(t, firstDone, "first import completion")
	if first.err != nil {
		t.Fatalf("first import: %v", first.err)
	}
	waitForTestSignal(t, secondCommitted, "second import commit")
	if store.mu.TryLock() {
		store.mu.Unlock()
		releaseSecond()
		_ = waitForTestValue(t, secondDone, "second import completion")
		t.Fatal("second identity import replaced its file without holding the store mutation lock")
	}
	releaseSecond()
	second := waitForTestValue(t, secondDone, "second import completion")
	if second.err != nil {
		t.Fatalf("second import: %v", second.err)
	}
	if first.public == nil || second.public == nil || first.public.ID != second.public.ID {
		t.Fatalf("same-account imports selected different identities: first=%#v second=%#v", first.public, second.public)
	}
	if first.key == "" || second.key == "" || first.key == second.key {
		t.Fatalf("same-account imports did not rotate distinct client keys: first=%q second=%q", first.key, second.key)
	}
	if _, ok := store.Lookup(first.key); ok {
		t.Fatal("first import client key remained selectable after the second import")
	}
	current, ok := store.Lookup(second.key)
	if !ok || current.ID != second.public.ID || current.AccountID != "team-one" {
		t.Fatalf("second import client key does not select the final identity: %#v", current)
	}
	items := store.ListForSync()
	if len(items) != 1 || items[0].ClientKey != second.key {
		t.Fatalf("in-memory state does not match the last import: %#v", items)
	}

	reopened, err := openStore()
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if _, ok = reopened.Lookup(first.key); ok {
		t.Fatal("reopened store retained the first import client key")
	}
	persisted, ok := reopened.Lookup(second.key)
	if !ok || persisted.ID != second.public.ID || persisted.AccountID != "team-one" {
		t.Fatalf("reopened store does not match the last import: %#v", persisted)
	}
	reopenedItems := reopened.ListForSync()
	if len(reopenedItems) != 1 || reopenedItems[0].ClientKey != second.key {
		t.Fatalf("reopened indexes do not match the last import: %#v", reopenedItems)
	}
}

func TestUncommittedWriteFailureDoesNotPublishIdentity(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	const token = "at-uncommitted-write"
	metadata := CredentialMetadata{AccountID: "team-one", AccountScoped: true}
	public, originalKey, err := store.ImportWithMetadata(token, metadata, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	expectedFailure := errors.New("forced pre-replacement failure")
	store.writeFile = func(string, []byte) (bool, error) {
		return false, expectedFailure
	}
	nextPublic, nextKey, err := store.ImportWithMetadata(token, metadata, time.Unix(1_700_000_001, 0))
	if !errors.Is(err, expectedFailure) {
		t.Fatalf("unexpected import error: %v", err)
	}
	if nextPublic != nil || nextKey != "" {
		t.Fatalf("failed write returned a published identity: public=%#v key=%q", nextPublic, nextKey)
	}
	if identity, ok := store.Lookup(originalKey); !ok || identity.ID != public.ID {
		t.Fatalf("original client key was not retained: %#v", identity)
	}
	if items := store.ListForSync(); len(items) != 1 || items[0].ClientKey != originalKey {
		t.Fatalf("failed write changed in-memory state: %#v", items)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if identity, ok := reopened.Lookup(originalKey); !ok || identity.ID != public.ID {
		t.Fatalf("failed write changed persisted state: %#v", identity)
	}
}

func TestCommittedWriteSyncFailurePublishesMatchingState(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	realWrite := store.writeFile
	expectedFailure := errors.New("forced directory sync failure")
	store.writeFile = func(path string, data []byte) (bool, error) {
		replaced, writeErr := realWrite(path, data)
		if writeErr != nil {
			return replaced, writeErr
		}
		return true, &AppliedError{Err: fmt.Errorf("identity file replaced but sync identity directory: %w", expectedFailure)}
	}
	public, clientKey, err := store.ImportWithMetadata("at-committed-sync-failure", CredentialMetadata{
		AccountID:     "team-one",
		AccountScoped: true,
	}, time.Unix(1_700_000_000, 0))
	if !errors.Is(err, expectedFailure) {
		t.Fatalf("unexpected import error: %v", err)
	}
	if !strings.Contains(err.Error(), "identity file replaced but sync identity directory") {
		t.Fatalf("sync failure did not report that replacement was applied: %v", err)
	}
	if !MutationApplied(err) {
		t.Fatalf("committed write sync failure was not classified as applied: %v", err)
	}
	if public == nil || clientKey == "" {
		t.Fatalf("committed write did not return its applied identity: public=%#v key=%q", public, clientKey)
	}
	if identity, ok := store.Lookup(clientKey); !ok || identity.ID != public.ID {
		t.Fatalf("committed write was not published in memory: %#v", identity)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if identity, ok := reopened.Lookup(clientKey); !ok || identity.ID != public.ID {
		t.Fatalf("committed write did not match persisted state: %#v", identity)
	}
}

func TestDeleteUnlinkFailureRetainsIdentity(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	public, clientKey, err := store.Import("at-delete-unlink-failure", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	expectedFailure := errors.New("forced unlink failure")
	store.removeFile = func(string) error {
		return expectedFailure
	}
	if err = store.Delete(public.ID); !errors.Is(err, expectedFailure) {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if MutationApplied(err) {
		t.Fatalf("failed unlink was classified as an applied deletion: %v", err)
	}
	if identity, ok := store.Lookup(clientKey); !ok || identity.ID != public.ID {
		t.Fatalf("unlink failure revoked the in-memory client key: %#v", identity)
	}
	if identity, ok := store.GetByID(public.ID); !ok || identity.ClientKey != clientKey {
		t.Fatalf("unlink failure removed the in-memory identity: %#v", identity)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if identity, ok := reopened.Lookup(clientKey); !ok || identity.ID != public.ID {
		t.Fatalf("unlink failure changed persisted state: %#v", identity)
	}
}

func TestDeleteSyncFailureReportsAppliedDeletion(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	public, clientKey, err := store.Import("at-delete-sync-failure", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	expectedFailure := errors.New("forced directory sync failure")
	store.syncDir = func(string) error {
		return expectedFailure
	}
	if err = store.Delete(public.ID); !errors.Is(err, expectedFailure) {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if !strings.Contains(err.Error(), "identity deleted but sync identity directory") {
		t.Fatalf("sync failure did not report that deletion was applied: %v", err)
	}
	if !MutationApplied(err) {
		t.Fatalf("delete sync failure was not classified as applied: %v", err)
	}
	if _, ok := store.Lookup(clientKey); ok {
		t.Fatal("applied deletion retained the in-memory client key")
	}
	if _, ok := store.GetByID(public.ID); ok {
		t.Fatal("applied deletion retained the in-memory identity")
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if items := reopened.ListForSync(); len(items) != 0 {
		t.Fatalf("applied deletion remained on disk: %#v", items)
	}
}

func assertOnlyControlIdentity(t *testing.T, store *Store, deletedID, controlID, controlKey string, revokedKeys []string) {
	t.Helper()
	if _, ok := store.GetByID(deletedID); ok {
		t.Fatalf("deleted identity %q remains in memory", deletedID)
	}
	for _, key := range revokedKeys {
		if _, ok := store.Lookup(key); ok {
			t.Fatalf("deleted client key %q remains selectable", key)
		}
	}
	control, ok := store.Lookup(controlKey)
	if !ok || control.ID != controlID || control.AccountID != "team-two" {
		t.Fatalf("control client key does not select team-two: %#v", control)
	}
	items := store.ListForSync()
	if len(items) != 1 || items[0].ID != controlID || items[0].ClientKey != controlKey {
		t.Fatalf("unexpected store contents: %#v", items)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.byID) != 1 || len(store.byKeyHash) != 1 || len(store.fileByID) != 1 {
		t.Fatalf("store indexes diverged: byID=%d byKeyHash=%d fileByID=%d", len(store.byID), len(store.byKeyHash), len(store.fileByID))
	}
	if store.byID[controlID] == nil || store.byKeyHash[store.byID[controlID].ClientKeyHash] != store.byID[controlID] {
		t.Fatal("control identity indexes do not reference the same record")
	}
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	timer := time.NewTimer(testChannelTimeout)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForTestValue[T any](t *testing.T, values <-chan T, description string) T {
	t.Helper()
	timer := time.NewTimer(testChannelTimeout)
	defer timer.Stop()
	select {
	case value := <-values:
		return value
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func waitForTestRelease(release <-chan struct{}, description string) error {
	timer := time.NewTimer(testChannelTimeout)
	defer timer.Stop()
	select {
	case <-release:
		return nil
	case <-timer.C:
		return fmt.Errorf("timed out waiting for %s", description)
	}
}
