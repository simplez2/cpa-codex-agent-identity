package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const clientKeyPrefix = "cais_"

const (
	identityVersionLegacyHash = 1
	identityVersionPlaintext  = 2
	identityVersionEncrypted  = 3
)

// Identity is one persisted Agent Identity credential and its opaque CPA key.
// Token and ClientKeyHash are secret and must never be logged or returned by list APIs.
type Identity struct {
	Version       int       `json:"-"`
	ID            string    `json:"-"`
	Token         string    `json:"-"`
	ClientKey     string    `json:"-"`
	ClientKeyHash string    `json:"-"`
	CreatedAt     time.Time `json:"-"`
	Kind          string    `json:"-"`
	Email         string    `json:"-"`
	PlanType      string    `json:"-"`
	ExpiresAt     time.Time `json:"-"`
	FedRAMP       bool      `json:"-"`
}

type persistedIdentity struct {
	Version         int        `json:"version"`
	ID              string     `json:"id"`
	Token           string     `json:"codex_access_token,omitempty"`
	TokenNonce      string     `json:"codex_access_token_nonce,omitempty"`
	TokenCiphertext string     `json:"codex_access_token_ciphertext,omitempty"`
	ClientKey       string     `json:"client_api_key,omitempty"`
	ClientKeyHash   string     `json:"client_key_sha256"`
	CreatedAt       time.Time  `json:"created_at"`
	Kind            string     `json:"credential_kind,omitempty"`
	Email           string     `json:"email,omitempty"`
	PlanType        string     `json:"plan_type,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	FedRAMP         bool       `json:"fedramp,omitempty"`
}

// PublicIdentity is the non-secret representation of an identity.
type PublicIdentity struct {
	ID        string     `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	Kind      string     `json:"credential_kind,omitempty"`
	Email     string     `json:"email,omitempty"`
	PlanType  string     `json:"plan_type,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	FedRAMP   bool       `json:"fedramp,omitempty"`
}

// CredentialMetadata is the non-secret metadata persisted beside an encrypted token.
type CredentialMetadata struct {
	Kind      string
	Email     string
	PlanType  string
	ExpiresAt time.Time
	FedRAMP   bool
}

// Store persists identities as owner-only JSON files.
type Store struct {
	dir       string
	mu        sync.RWMutex
	byID      map[string]*Identity
	byKeyHash map[string]*Identity
	fileByID  map[string]string
	cipher    *tokenCipher
}

// Option configures the identity store.
type Option func(*Store) error

// WithEncryptionKey enables AES-256-GCM encryption for tokens at rest.
func WithEncryptionKey(key []byte) Option {
	keyCopy := append([]byte(nil), key...)
	return func(store *Store) error {
		cipher, err := newTokenCipher(keyCopy)
		for index := range keyCopy {
			keyCopy[index] = 0
		}
		if err != nil {
			return err
		}
		store.cipher = cipher
		return nil
	}
}

// Open loads or creates an identity store.
func Open(dir string, options ...Option) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("identity data directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create identity data directory: %w", err)
	}
	directoryInfo, err := os.Lstat(dir)
	if err != nil || directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
		return nil, errors.New("identity data directory must be a real directory")
	}
	if runtime.GOOS != "windows" {
		if err = os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("secure identity data directory: %w", err)
		}
	}
	result := &Store{
		dir:       dir,
		byID:      make(map[string]*Identity),
		byKeyHash: make(map[string]*Identity),
		fileByID:  make(map[string]string),
	}
	for _, option := range options {
		if option != nil {
			if err = option(result); err != nil {
				return nil, err
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read identity data directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		fileInfo, infoErr := entry.Info()
		if infoErr != nil || !fileInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("identity file %q must be a regular file", entry.Name())
		}
		if runtime.GOOS != "windows" && fileInfo.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("identity file %q permissions are too broad", entry.Name())
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read identity file %q: %w", entry.Name(), readErr)
		}
		identity, decodeErr := result.decodeIdentity(data)
		if decodeErr != nil || !validIdentity(identity) {
			return nil, fmt.Errorf("identity file %q is invalid", entry.Name())
		}
		if result.byID[identity.ID] != nil || result.byKeyHash[identity.ClientKeyHash] != nil {
			return nil, fmt.Errorf("identity file %q duplicates an existing identity", entry.Name())
		}
		if result.cipher != nil && identity.Version != identityVersionEncrypted {
			identity.Version = identityVersionEncrypted
			migrated, encodeErr := result.encodeIdentity(identity)
			if encodeErr != nil {
				return nil, fmt.Errorf("encrypt identity file %q", entry.Name())
			}
			if writeErr := writeOwnerOnlyAtomic(path, migrated); writeErr != nil {
				return nil, fmt.Errorf("encrypt identity file %q: %w", entry.Name(), writeErr)
			}
		}
		result.byID[identity.ID] = identity
		result.byKeyHash[identity.ClientKeyHash] = identity
		result.fileByID[identity.ID] = path
	}
	return result, nil
}

func validIdentity(identity *Identity) bool {
	if identity == nil || (identity.Version != identityVersionLegacyHash && identity.Version != identityVersionPlaintext && identity.Version != identityVersionEncrypted) {
		return false
	}
	if strings.TrimSpace(identity.ID) == "" || strings.TrimSpace(identity.Token) == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(identity.ID), []byte(IdentityID(identity.Token))) != 1 {
		return false
	}
	decoded, err := hex.DecodeString(identity.ClientKeyHash)
	if err != nil || len(decoded) != sha256.Size {
		return false
	}
	if identity.ClientKey == "" {
		return identity.Version == identityVersionLegacyHash || identity.Version == identityVersionEncrypted
	}
	if !strings.HasPrefix(identity.ClientKey, clientKeyPrefix) {
		return false
	}
	digest := sha256.Sum256([]byte(identity.ClientKey))
	return subtle.ConstantTimeCompare([]byte(identity.ClientKeyHash), []byte(hex.EncodeToString(digest[:]))) == 1
}

// Import stores a token and rotates the opaque client key returned to CPA.
func (s *Store) Import(token string, now time.Time) (*PublicIdentity, string, error) {
	return s.ImportWithMetadata(token, CredentialMetadata{}, now)
}

// ImportWithMetadata stores a token and non-secret display metadata while rotating the CPA client key.
func (s *Store) ImportWithMetadata(token string, metadata CredentialMetadata, now time.Time) (*PublicIdentity, string, error) {
	if s == nil {
		return nil, "", errors.New("identity store is nil")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, "", errors.New("codex access token is required")
	}
	id := IdentityID(token)
	clientKey, err := randomClientKey()
	if err != nil {
		return nil, "", errors.New("failed to generate sidecar client key")
	}
	clientDigest := sha256.Sum256([]byte(clientKey))
	identity := &Identity{
		Version:       identityVersionPlaintext,
		ID:            id,
		Token:         token,
		ClientKey:     clientKey,
		ClientKeyHash: hex.EncodeToString(clientDigest[:]),
		CreatedAt:     now.UTC(),
		Kind:          strings.TrimSpace(metadata.Kind),
		Email:         strings.TrimSpace(metadata.Email),
		PlanType:      strings.TrimSpace(metadata.PlanType),
		ExpiresAt:     metadata.ExpiresAt.UTC(),
		FedRAMP:       metadata.FedRAMP,
	}
	if s.cipher != nil {
		identity.Version = identityVersionEncrypted
	}
	data, err := s.encodeIdentity(identity)
	if err != nil {
		return nil, "", errors.New("failed to encode identity")
	}
	path := filepath.Join(s.dir, "identity-"+strings.TrimPrefix(id, "agent-")+".json")
	if err = writeOwnerOnlyAtomic(path, data); err != nil {
		return nil, "", err
	}

	s.mu.Lock()
	if previous := s.byID[id]; previous != nil {
		delete(s.byKeyHash, previous.ClientKeyHash)
	}
	s.byID[id] = identity
	s.byKeyHash[identity.ClientKeyHash] = identity
	s.fileByID[id] = path
	s.mu.Unlock()
	public := publicIdentity(identity)
	return &public, clientKey, nil
}

// IdentityID returns the stable non-secret identifier used for one token.
func IdentityID(token string) string {
	tokenDigest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return "agent-" + hex.EncodeToString(tokenDigest[:6])
}

func randomClientKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return clientKeyPrefix + hex.EncodeToString(raw), nil
}

func writeOwnerOnlyAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".identity-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary identity file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write identity file: %w", err)
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(path)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace identity file: %w", err)
	}
	if err = syncDirectory(dir); err != nil {
		return fmt.Errorf("sync identity directory: %w", err)
	}
	return nil
}

func syncDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// Lookup returns a copy of the identity selected by an opaque CPA client key.
func (s *Store) Lookup(clientKey string) (*Identity, bool) {
	if s == nil || !strings.HasPrefix(clientKey, clientKeyPrefix) {
		return nil, false
	}
	digest := sha256.Sum256([]byte(clientKey))
	hash := hex.EncodeToString(digest[:])
	s.mu.RLock()
	identity := s.byKeyHash[hash]
	if identity == nil || subtle.ConstantTimeCompare([]byte(identity.ClientKeyHash), []byte(hash)) != 1 {
		s.mu.RUnlock()
		return nil, false
	}
	copyIdentity := *identity
	s.mu.RUnlock()
	return &copyIdentity, true
}

// List returns non-secret identity metadata.
func (s *Store) List() []PublicIdentity {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	identities := make([]PublicIdentity, 0, len(s.byID))
	for _, identity := range s.byID {
		identities = append(identities, publicIdentity(identity))
	}
	s.mu.RUnlock()
	return identities
}

// UpdateMetadata replaces only non-secret display metadata for a stored identity.
func (s *Store) UpdateMetadata(id string, metadata CredentialMetadata) error {
	if s == nil {
		return errors.New("identity store is nil")
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.byID[id]
	path := s.fileByID[id]
	if current == nil || path == "" {
		return os.ErrNotExist
	}
	next := *current
	next.Kind = strings.TrimSpace(metadata.Kind)
	next.Email = strings.TrimSpace(metadata.Email)
	next.PlanType = strings.TrimSpace(metadata.PlanType)
	next.ExpiresAt = metadata.ExpiresAt.UTC()
	next.FedRAMP = metadata.FedRAMP
	data, err := s.encodeIdentity(&next)
	if err != nil {
		return errors.New("failed to encode identity metadata")
	}
	if err = writeOwnerOnlyAtomic(path, data); err != nil {
		return err
	}
	s.byID[id] = &next
	s.byKeyHash[next.ClientKeyHash] = &next
	return nil
}

// ListForSync returns secret-bearing copies for internal CPA reconciliation only.
func (s *Store) ListForSync() []Identity {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	identities := make([]Identity, 0, len(s.byID))
	for _, identity := range s.byID {
		identities = append(identities, *identity)
	}
	s.mu.RUnlock()
	return identities
}

// GetByID returns a secret-bearing copy for internal transactional rollback only.
func (s *Store) GetByID(id string) (*Identity, bool) {
	if s == nil {
		return nil, false
	}
	id = strings.TrimSpace(id)
	s.mu.RLock()
	identity := s.byID[id]
	if identity == nil {
		s.mu.RUnlock()
		return nil, false
	}
	copyIdentity := *identity
	s.mu.RUnlock()
	return &copyIdentity, true
}

// Restore rewrites a previous identity snapshot after a failed external transaction.
func (s *Store) Restore(identity *Identity) error {
	if s == nil || !validIdentity(identity) {
		return errors.New("valid identity snapshot is required")
	}
	copyIdentity := *identity
	if s.cipher != nil {
		copyIdentity.Version = identityVersionEncrypted
	}
	data, err := s.encodeIdentity(&copyIdentity)
	if err != nil {
		return errors.New("failed to encode identity")
	}
	path := filepath.Join(s.dir, "identity-"+strings.TrimPrefix(copyIdentity.ID, "agent-")+".json")
	if err = writeOwnerOnlyAtomic(path, data); err != nil {
		return err
	}
	s.mu.Lock()
	if current := s.byID[copyIdentity.ID]; current != nil {
		delete(s.byKeyHash, current.ClientKeyHash)
	}
	s.byID[copyIdentity.ID] = &copyIdentity
	s.byKeyHash[copyIdentity.ClientKeyHash] = &copyIdentity
	s.fileByID[copyIdentity.ID] = path
	s.mu.Unlock()
	return nil
}

// Delete removes an identity and revokes its CPA client key.
func (s *Store) Delete(id string) error {
	if s == nil {
		return errors.New("identity store is nil")
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	identity := s.byID[id]
	path := s.fileByID[id]
	if identity == nil {
		s.mu.Unlock()
		return os.ErrNotExist
	}
	delete(s.byID, id)
	delete(s.byKeyHash, identity.ClientKeyHash)
	delete(s.fileByID, id)
	s.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete identity file: %w", err)
	}
	if err := syncDirectory(s.dir); err != nil {
		return fmt.Errorf("sync identity directory: %w", err)
	}
	return nil
}

func (s *Store) encodeIdentity(identity *Identity) ([]byte, error) {
	if !validIdentity(identity) {
		return nil, errors.New("valid identity is required")
	}
	persisted := persistedIdentity{
		Version:       identity.Version,
		ID:            identity.ID,
		ClientKey:     identity.ClientKey,
		ClientKeyHash: identity.ClientKeyHash,
		CreatedAt:     identity.CreatedAt,
		Kind:          identity.Kind,
		Email:         identity.Email,
		PlanType:      identity.PlanType,
		ExpiresAt:     timePointer(identity.ExpiresAt),
		FedRAMP:       identity.FedRAMP,
	}
	if s.cipher != nil {
		persisted.Version = identityVersionEncrypted
		nonce, ciphertext, err := s.cipher.encrypt(identity.ID, identity.Token)
		if err != nil {
			return nil, err
		}
		persisted.TokenNonce = nonce
		persisted.TokenCiphertext = ciphertext
	} else {
		if identity.Version == identityVersionEncrypted {
			return nil, errors.New("encrypted identity store requires a data encryption key")
		}
		persisted.Token = identity.Token
	}
	return json.MarshalIndent(&persisted, "", "  ")
}

func (s *Store) decodeIdentity(data []byte) (*Identity, error) {
	var persisted persistedIdentity
	if json.Unmarshal(data, &persisted) != nil {
		return nil, errors.New("identity JSON is invalid")
	}
	identity := &Identity{
		Version:       persisted.Version,
		ID:            strings.TrimSpace(persisted.ID),
		ClientKey:     strings.TrimSpace(persisted.ClientKey),
		ClientKeyHash: strings.TrimSpace(persisted.ClientKeyHash),
		CreatedAt:     persisted.CreatedAt,
		Kind:          strings.TrimSpace(persisted.Kind),
		Email:         strings.TrimSpace(persisted.Email),
		PlanType:      strings.TrimSpace(persisted.PlanType),
		ExpiresAt:     timeValue(persisted.ExpiresAt),
		FedRAMP:       persisted.FedRAMP,
	}
	switch persisted.Version {
	case identityVersionLegacyHash, identityVersionPlaintext:
		identity.Token = strings.TrimSpace(persisted.Token)
	case identityVersionEncrypted:
		if strings.TrimSpace(persisted.Token) != "" {
			return nil, errors.New("encrypted identity unexpectedly contains plaintext")
		}
		decrypted, err := s.cipher.decrypt(identity.ID, persisted.TokenNonce, persisted.TokenCiphertext)
		if err != nil {
			return nil, err
		}
		identity.Token = strings.TrimSpace(decrypted)
	default:
		return nil, errors.New("identity version is unsupported")
	}
	return identity, nil
}

func publicIdentity(identity *Identity) PublicIdentity {
	if identity == nil {
		return PublicIdentity{}
	}
	return PublicIdentity{
		ID:        identity.ID,
		CreatedAt: identity.CreatedAt,
		Kind:      identity.Kind,
		Email:     identity.Email,
		PlanType:  identity.PlanType,
		ExpiresAt: timePointer(identity.ExpiresAt),
		FedRAMP:   identity.FedRAMP,
	}
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}
