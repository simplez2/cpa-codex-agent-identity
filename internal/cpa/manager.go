package cpa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	authMode                = "agent_identity_sidecar"
	pluginProviderID        = "codex-agent-identity"
	runtimeProviderID       = "codex"
	legacyProviderID        = "codex" // legacy sidecar auth files emitted before provider separation
	authFilePrefix          = "codex-agent-identity-"
	managedAuthSuffix       = "-agent-identity"
	sidecarClientKeyField   = "sidecar_client_key"
	managementRetryAttempts = 4
	managementRetryInitial  = 75 * time.Millisecond
	managementRetryMaximum  = 500 * time.Millisecond
	managementVerifyWindow  = 5 * time.Second
)

// Credential is the CPA-facing representation of one sidecar-managed Codex credential.
// ClientKey is the opaque key used only for CPA runtime calls through the sidecar.
// UpstreamToken is secret-bearing and is written to CPA's native access_token field
// so stock management clients such as Keeper receive the same token semantics as
// CPA-native Codex OAuth credentials. It must never be logged or returned by APIs.
// ErrUnmanagedAuthFile indicates that CPA already owns the target filename
// with a credential that was not created by this sidecar.
var ErrUnmanagedAuthFile = errors.New("unmanaged CPA auth file")

type Credential struct {
	IdentityID    string
	ClientKey     string `json:"-"`
	UpstreamToken string `json:"-"`
	Kind          string
	AccountID     string
	UserID        string
	Email         string
	PlanType      string
	ExpiresAt     time.Time
	FedRAMP       bool
}

// Manager keeps sidecar identities synchronized with CPA's native Codex auth-file list.
// It deliberately uses CPA's public management API so the CPA image can remain stock.
type Manager struct {
	baseURL        *url.URL
	managementKey  string
	client         *http.Client
	sidecarBaseURL string
	mu             sync.Mutex
}

type authFileEntry struct {
	Name      string `json:"name"`
	AuthIndex string `json:"auth_index"`
}

type managedAuthFile struct {
	IdentityID string
	Raw        []byte
}

// AuthFileSnapshot is a secret-bearing, in-memory snapshot used only during
// an explicitly requested identity delete transaction. Callers must keep it
// in memory or write it to an owner-only backup; it is never returned by list
// APIs or logged.
type AuthFileSnapshot struct {
	Name       string
	IdentityID string
	Raw        []byte
	Disabled   bool
}

// IdentityState is the non-secret CPA synchronization state for one sidecar identity.
type IdentityState struct {
	Synced   bool   `json:"synced"`
	Disabled bool   `json:"disabled"`
	AuthFile string `json:"auth_file,omitempty"`
}

// ProbeState is the bounded, non-secret result of checking CPA's auth-file API.
type ProbeState string

const (
	ProbeStateReady         ProbeState = "ready"
	ProbeStateNotConfigured ProbeState = "not_configured"
	ProbeStateUnauthorized  ProbeState = "unauthorized"
	ProbeStateUnreachable   ProbeState = "unreachable"
	ProbeStateError         ProbeState = "error"
)

// ProbeResult intentionally contains no URL, key, file name, or response body.
type ProbeResult struct {
	Configured bool       `json:"configured"`
	Reachable  bool       `json:"reachable"`
	State      ProbeState `json:"state"`
}

// NewManager creates a CPA auth-file manager.
func NewManager(rawBaseURL, managementKey, sidecarBaseURL string, client *http.Client) (*Manager, error) {
	rawBaseURL = strings.TrimRight(strings.TrimSpace(rawBaseURL), "/")
	managementKey = strings.TrimSpace(managementKey)
	sidecarBaseURL = strings.TrimRight(strings.TrimSpace(sidecarBaseURL), "/")
	if rawBaseURL == "" || managementKey == "" || sidecarBaseURL == "" {
		return nil, errors.New("CPA management URL, key, and sidecar base URL are required")
	}
	parsed, err := url.Parse(rawBaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("CPA management URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("CPA management URL must use HTTP or HTTPS")
	}
	sidecarBaseURL, err = normalizeSidecarBaseURL(sidecarBaseURL)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Manager{
		baseURL:        parsed,
		managementKey:  managementKey,
		client:         client,
		sidecarBaseURL: sidecarBaseURL,
	}, nil
}

// Probe performs a lightweight authenticated request against CPA's auth-file
// endpoint. It never downloads or exposes credential contents, making it safe
// for the sidecar diagnostics page.
func (m *Manager) Probe(ctx context.Context) ProbeResult {
	result := ProbeResult{Configured: m != nil, State: ProbeStateUnreachable}
	if m == nil {
		result.State = ProbeStateNotConfigured
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := m.newRequest(ctx, http.MethodGet, "/auth-files", nil, "")
	if err != nil {
		result.State = ProbeStateError
		return result
	}
	response, err := m.client.Do(request)
	if err != nil {
		result.State = ProbeStateUnreachable
		return result
	}
	defer response.Body.Close()
	result.Reachable = true
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		result.State = ProbeStateUnauthorized
		return result
	case http.StatusOK:
		var payload struct {
			Files []authFileEntry `json:"files"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
			result.State = ProbeStateError
			return result
		}
		result.State = ProbeStateReady
		return result
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		result.State = ProbeStateError
		return result
	}
}

func normalizeSidecarBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery || parsed.Opaque != "" {
		return "", errors.New("sidecar base URL is invalid")
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if (scheme != "http" && scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" {
		return "", errors.New("sidecar base URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.EscapedPath() != "/backend-api/codex" {
		return "", errors.New("sidecar base URL must end at /backend-api/codex")
	}
	parsed.Scheme = scheme
	return parsed.String(), nil
}

// UpsertIdentity creates or replaces the sidecar-owned Codex auth file for an identity.
//
// The management API is deliberately treated as an eventually-consistent file
// boundary. We upload the final payload in one operation (rather than staging
// disabled=true and then PATCHing it back) because CPA runtime-only auths may
// acknowledge a field patch without persisting it to disk.
func (m *Manager) UpsertIdentity(ctx context.Context, credential Credential) error {
	ctx = nonNilContext(ctx)
	credential = normalizeCredential(credential)
	if err := validateCredential(credential); err != nil {
		return err
	}
	name, err := authFileName(credential)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	managedBefore, err := m.managedAuthFilesForCredentials(ctx, []Credential{credential})
	if err != nil {
		return err
	}
	previous, existed, err := m.downloadAuthFile(ctx, name)
	if err != nil {
		return err
	}
	if existed && !isManagedCredential(previous, credential.IdentityID) {
		return fmt.Errorf("%w: refusing to overwrite an unmanaged CPA auth file", ErrUnmanagedAuthFile)
	}
	// A disabled runtime-only auth can be omitted from CPA's list. The
	// credential-aware discovery above still finds it by deterministic name.
	if !existed {
		if old, ok := managedBefore[name]; ok && old.IdentityID == credential.IdentityID {
			previous = append([]byte(nil), old.Raw...)
			existed = true
		}
	}

	priorRaw := append([]byte(nil), previous...)
	priorExists := existed
	if !priorExists {
		if oldName, old, ok := preferredManagedFile(managedBefore, credential); ok {
			_ = oldName
			priorRaw = append([]byte(nil), old.Raw...)
			priorExists = true
		}
	}
	disabled := managedCredentialDisabled(priorRaw, priorExists)
	body, err := m.credentialJSONWithDisabled(credential, disabled)
	if err != nil {
		return err
	}
	if priorExists {
		body, err = mergeManagedAuthFields(body, priorRaw)
		if err != nil {
			return err
		}
	}

	rollback := func() {
		m.rollbackManagedFiles(name, previous, existed, nil)
	}
	if !existed || !equivalentJSON(previous, body) {
		if err = m.uploadAuthFile(ctx, name, body); err != nil {
			rollback()
			return err
		}
	}
	if _, err = m.waitForAuthFile(ctx, name, func(raw []byte) bool {
		return managedCredentialMatches(raw, credential, body)
	}); err != nil {
		rollback()
		return err
	}

	// Migrate and remove stale sidecar names only after the canonical file has
	// been observed on disk. Sorting makes the transaction deterministic and
	// keeps rollback behavior reproducible.
	oldNames := matchingManagedFileNames(managedBefore, credential)
	deleted := make(map[string][]byte)
	for _, oldName := range oldNames {
		if oldName == name {
			continue
		}
		old := managedBefore[oldName]
		// Snapshot before DELETE. The management request can remove the file
		// successfully and still return a transport error (for example when a
		// proxy closes the connection after committing the request). Recording
		// the bytes first makes that partially-applied migration reversible.
		deleted[oldName] = append([]byte(nil), old.Raw...)
		if err = m.deleteAuthFile(ctx, oldName); err != nil {
			m.rollbackManagedFiles(name, previous, existed, deleted)
			return err
		}
		if err = m.waitForAuthFileAbsent(ctx, oldName); err != nil {
			m.rollbackManagedFiles(name, previous, existed, deleted)
			return err
		}
	}
	if _, err = m.waitForAuthFile(ctx, name, func(raw []byte) bool {
		return managedCredentialMatches(raw, credential, body)
	}); err != nil {
		m.rollbackManagedFiles(name, previous, existed, deleted)
		return err
	}
	return nil
}

// RemoveIdentity removes only the sidecar-owned auth file for this identity.
func (m *Manager) RemoveIdentity(ctx context.Context, identityID string) error {
	_, err := m.RemoveIdentityWithSnapshot(ctx, identityID)
	return err
}

// SnapshotIdentity returns all native CPA auth files owned by one identity.
// Disabled files are intentionally included so they can be backed up and
// deleted just like enabled files.
func (m *Manager) SnapshotIdentity(ctx context.Context, identityID string) ([]AuthFileSnapshot, error) {
	return m.SnapshotCredential(ctx, Credential{IdentityID: strings.TrimSpace(identityID)})
}

// SnapshotCredential returns all native CPA auth files owned by a credential,
// including disabled runtime-only files which CPA may omit from /auth-files.
func (m *Manager) SnapshotCredential(ctx context.Context, credential Credential) ([]AuthFileSnapshot, error) {
	ctx = nonNilContext(ctx)
	credential = normalizeCredential(credential)
	if err := validateIdentityID(credential.IdentityID); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	files, err := m.managedAuthFilesForCredentials(ctx, []Credential{credential})
	if err != nil {
		return nil, err
	}
	return snapshotsForCredential(files, credential), nil
}

// RemoveIdentityWithSnapshot deletes the native auth files and returns the
// exact bytes that were removed so a caller can roll the CPA side back if a
// later step fails.
func (m *Manager) RemoveIdentityWithSnapshot(ctx context.Context, identityID string) ([]AuthFileSnapshot, error) {
	return m.RemoveCredentialWithSnapshot(ctx, Credential{IdentityID: strings.TrimSpace(identityID)})
}

// RemoveCredentialWithSnapshot removes all auth-file names known to belong to
// one credential, including deterministic legacy names hidden by CPA when a
// runtime-only auth is disabled.
func (m *Manager) RemoveCredentialWithSnapshot(ctx context.Context, credential Credential) ([]AuthFileSnapshot, error) {
	ctx = nonNilContext(ctx)
	credential = normalizeCredential(credential)
	if err := validateIdentityID(credential.IdentityID); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	files, err := m.managedAuthFilesForCredentials(ctx, []Credential{credential})
	if err != nil {
		return nil, err
	}
	names := matchingManagedFileNames(files, credential)
	snapshots := snapshotsForCredential(files, credential)
	deleted := make(map[string][]byte, len(names))
	for _, name := range names {
		// Record the previous bytes before the remote delete. If the API
		// acknowledges the delete but visibility lags, rollback still knows
		// which file must be restored.
		deleted[name] = append([]byte(nil), files[name].Raw...)
		if err = m.deleteAuthFile(ctx, name); err != nil {
			m.restoreManagedFiles(deleted)
			return nil, err
		}
		if err = m.waitForAuthFileAbsent(ctx, name); err != nil {
			m.restoreManagedFiles(deleted)
			return nil, err
		}
	}
	return snapshots, nil
}

// RestoreAuthFiles restores a previously captured CPA auth-file snapshot. It
// is used only to roll back a delete when the sidecar store cannot be removed.
func (m *Manager) RestoreAuthFiles(ctx context.Context, snapshots []AuthFileSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	ctx = nonNilContext(ctx)
	ordered := append([]AuthFileSnapshot(nil), snapshots...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, snapshot := range ordered {
		name := filepath.Base(strings.TrimSpace(snapshot.Name))
		if name == "" || name != snapshot.Name || strings.Contains(name, "..") {
			return errors.New("CPA auth-file snapshot name is invalid")
		}
		if err := m.uploadAuthFile(ctx, name, snapshot.Raw); err != nil {
			return err
		}
		if _, err := m.waitForAuthFile(ctx, name, func(raw []byte) bool {
			return equivalentJSON(raw, snapshot.Raw)
		}); err != nil {
			return err
		}
	}
	return nil
}

// SetIdentityDisabled changes the native CPA auth-file state without rotating
// the sidecar key. The identity-only form remains for API compatibility; new
// callers should use SetIdentityDisabledForCredential so hidden legacy names
// can be discovered deterministically.
func (m *Manager) SetIdentityDisabled(ctx context.Context, identityID string, disabled bool) error {
	return m.SetIdentityDisabledForCredential(ctx, Credential{IdentityID: strings.TrimSpace(identityID)}, disabled)
}

// SetIdentityDisabledForCredential updates the complete JSON document instead
// of relying on CPA's PATCH endpoint. This is required for runtime-only auths:
// CPA may update the in-memory Auth object while skipping persistence of a
// field-only mutation.
func (m *Manager) SetIdentityDisabledForCredential(ctx context.Context, credential Credential, disabled bool) error {
	ctx = nonNilContext(ctx)
	credential = normalizeCredential(credential)
	if err := validateIdentityID(credential.IdentityID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	files, err := m.managedAuthFilesForCredentials(ctx, []Credential{credential})
	if err != nil {
		return err
	}
	names := matchingManagedFileNames(files, credential)
	if len(names) == 0 {
		return errors.New("CPA Codex credential is not synchronized")
	}
	updated := make(map[string][]byte, len(names))
	for _, name := range names {
		oldRaw := append([]byte(nil), files[name].Raw...)
		// Register the rollback image before uploading. A successful upload
		// followed by a verification timeout must restore this file too.
		updated[name] = oldRaw
		nextRaw, errEncode := setManagedCredentialDisabled(oldRaw, disabled)
		if errEncode != nil {
			m.restoreManagedFiles(updated)
			return errEncode
		}
		if !equivalentJSON(oldRaw, nextRaw) {
			if err = m.uploadAuthFile(ctx, name, nextRaw); err != nil {
				m.restoreManagedFiles(updated)
				return err
			}
		}
		if _, err = m.waitForAuthFile(ctx, name, func(raw []byte) bool {
			return managedCredentialStateMatches(raw, credential, disabled)
		}); err != nil {
			m.restoreManagedFiles(updated)
			return err
		}
	}
	return nil
}

// IdentityStatus reports whether each identity currently has a native CPA Codex auth file.
func (m *Manager) IdentityStatus(ctx context.Context, identityIDs []string) (map[string]bool, error) {
	states, err := m.IdentityStates(ctx, identityIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(identityIDs))
	for _, id := range identityIDs {
		id = strings.TrimSpace(id)
		result[id] = states[id].Synced
	}
	return result, nil
}

// IdentityStates reports synchronization, disabled state, and safe auth-file names.
func (m *Manager) IdentityStates(ctx context.Context, identityIDs []string) (map[string]IdentityState, error) {
	credentials := make([]Credential, 0, len(identityIDs))
	for _, id := range identityIDs {
		credentials = append(credentials, Credential{IdentityID: strings.TrimSpace(id)})
	}
	return m.IdentityStatesForCredentials(ctx, credentials)
}

// IdentityStatesForCredentials is the credential-aware status path used by the
// sidecar server. It probes deterministic canonical and legacy filenames in
// addition to CPA's visible list, so disabled runtime-only records are not
// falsely reported as unsynchronized.
func (m *Manager) IdentityStatesForCredentials(ctx context.Context, credentials []Credential) (map[string]IdentityState, error) {
	ctx = nonNilContext(ctx)
	normalized := make([]Credential, 0, len(credentials))
	result := make(map[string]IdentityState, len(credentials))
	for _, credential := range credentials {
		credential = normalizeCredential(credential)
		id := strings.TrimSpace(credential.IdentityID)
		if id == "" {
			continue
		}
		result[id] = IdentityState{}
		if validateIdentityID(id) == nil {
			normalized = append(normalized, credential)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	files, err := m.managedAuthFilesForCredentials(ctx, normalized)
	if err != nil {
		return nil, err
	}
	for _, credential := range normalized {
		if name, file, ok := preferredManagedFile(files, credential); ok {
			result[credential.IdentityID] = IdentityState{
				Synced:   true,
				Disabled: managedCredentialDisabled(file.Raw, true),
				AuthFile: filepath.Base(name),
			}
		}
	}
	return result, nil
}

// IdentityIDForAuthIndex resolves a CPA runtime auth index only when it belongs
// to a sidecar-managed native Codex auth file.
func (m *Manager) IdentityIDForAuthIndex(ctx context.Context, authIndex string) (string, bool, error) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return "", false, nil
	}
	ctx = nonNilContext(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := m.listAuthFileEntries(ctx)
	if err != nil {
		return "", false, err
	}
	for _, item := range entries {
		if strings.TrimSpace(item.AuthIndex) != authIndex {
			continue
		}
		name := filepath.Base(strings.TrimSpace(item.Name))
		raw, exists, downloadErr := m.downloadAuthFile(ctx, name)
		if downloadErr != nil {
			return "", false, downloadErr
		}
		if !exists {
			return "", false, nil
		}
		identityID, managed := managedCredentialIdentity(raw)
		if !managed {
			return "", false, nil
		}
		return identityID, true, nil
	}
	return "", false, nil
}

// ForwardAPICall passes a management api-call request to stock CPA unchanged.
// It is used by the sidecar compatibility shim for non-Agent-Identity entries.
func (m *Manager) ForwardAPICall(ctx context.Context, raw []byte) (int, http.Header, []byte, error) {
	request, err := m.newRequest(ctx, http.MethodPost, "/api-call", bytes.NewReader(raw), "")
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := m.client.Do(request)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("forward CPA api-call: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return 0, nil, nil, errors.New("forward CPA api-call: invalid response")
	}
	return response.StatusCode, response.Header.Clone(), body, nil
}

func (m *Manager) credentialJSON(credential Credential) ([]byte, error) {
	return m.credentialJSONWithDisabled(credential, false)
}

func (m *Manager) credentialJSONWithDisabled(credential Credential, disabled bool) ([]byte, error) {
	email := strings.TrimSpace(credential.Email)
	if email == "" {
		email = credential.IdentityID + "@agent-identity.local"
	}
	// Persist the auth file under CPA's native Codex provider namespace. The
	// sidecar marker below keeps the plugin-owned parser scoped to these files,
	// while the native provider name lets CPA-compatible consumers (including
	// Keeper) select their normal Codex quota implementation instead of treating
	// the credential as an unknown provider.
	payload := map[string]any{
		"type":                runtimeProviderID,
		"auth_mode":           authMode,
		"auth_kind":           "oauth",
		"email":               email,
		"access_token":        credential.UpstreamToken,
		sidecarClientKeyField: credential.ClientKey,
		"base_url":            m.sidecarBaseURL,
		"websockets":          true,
		"disabled":            disabled,
		"runtime_only":        true,
		"agent_identity_id":   credential.IdentityID,
		"note":                "Agent Identity via sidecar",
	}
	if credential.Kind != "" {
		payload["credential_kind"] = credential.Kind
	}
	if credential.Kind == "personal_access_token" {
		payload["note"] = "Codex Access Token via sidecar"
	}
	if credential.AccountID != "" {
		payload["account_id"] = credential.AccountID
	}
	if credential.UserID != "" {
		payload["chatgpt_user_id"] = credential.UserID
	}
	if credential.PlanType != "" {
		payload["plan_type"] = strings.ToLower(credential.PlanType)
	}
	if !credential.ExpiresAt.IsZero() {
		payload["expires_at"] = credential.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if credential.FedRAMP {
		payload["fedramp"] = true
	}
	return json.MarshalIndent(payload, "", "  ")
}

func authFileName(credential Credential) (string, error) {
	if err := validateIdentityID(credential.IdentityID); err != nil {
		return "", err
	}
	email := sanitizeFilePart(credential.Email)
	planType := sanitizeFilePart(strings.ToLower(credential.PlanType))
	if email == "" {
		return legacyAuthFileName(credential.IdentityID)
	}
	name := "codex-"
	if accountID := strings.TrimSpace(credential.AccountID); accountID != "" {
		digest := sha256.Sum256([]byte(accountID))
		name += hex.EncodeToString(digest[:])[:8] + "-"
	}
	name += email
	if planType != "" {
		name += "-" + planType
	}
	// Keep sidecar-managed files visibly Codex-native while reserving a
	// distinct filename namespace from CPA's OAuth importer. A user can
	// legitimately have both a native OAuth credential and a PAT for the
	// same email/Team workspace; reusing the native filename would make the
	// sidecar refuse to overwrite the user's credential as unmanaged.
	name += managedAuthSuffix
	return name + ".json", nil
}

func legacyAuthFileName(identityID string) (string, error) {
	if err := validateIdentityID(identityID); err != nil {
		return "", err
	}
	return authFilePrefix + strings.TrimPrefix(strings.TrimSpace(identityID), "agent-") + ".json", nil
}

func validateIdentityID(identityID string) error {
	identityID = strings.TrimSpace(identityID)
	if !strings.HasPrefix(identityID, "agent-") || len(identityID) <= len("agent-") {
		return errors.New("identity ID is invalid")
	}
	suffix := strings.TrimPrefix(identityID, "agent-")
	for _, character := range suffix {
		if !((character >= 'a' && character <= 'f') || (character >= '0' && character <= '9')) {
			return errors.New("identity ID is invalid")
		}
	}
	return nil
}

func sanitizeFilePart(value string) string {
	value = strings.TrimSpace(value)
	var result strings.Builder
	for _, character := range value {
		allowed := (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("@._+-", character)
		if allowed {
			result.WriteRune(character)
		} else if result.Len() > 0 && !strings.HasSuffix(result.String(), "_") {
			result.WriteByte('_')
		}
		if result.Len() >= 160 {
			break
		}
	}
	return strings.Trim(result.String(), "._-+")
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func normalizeCredential(credential Credential) Credential {
	credential.IdentityID = strings.TrimSpace(credential.IdentityID)
	credential.ClientKey = strings.TrimSpace(credential.ClientKey)
	credential.UpstreamToken = strings.TrimSpace(credential.UpstreamToken)
	credential.Kind = strings.ToLower(strings.TrimSpace(credential.Kind))
	credential.AccountID = strings.TrimSpace(credential.AccountID)
	credential.UserID = strings.TrimSpace(credential.UserID)
	credential.Email = strings.TrimSpace(credential.Email)
	credential.PlanType = strings.ToLower(strings.TrimSpace(credential.PlanType))
	if !credential.ExpiresAt.IsZero() {
		credential.ExpiresAt = credential.ExpiresAt.UTC()
	}
	return credential
}

func validateCredential(credential Credential) error {
	if err := validateIdentityID(credential.IdentityID); err != nil {
		return err
	}
	// Keep the same client-key shape enforced by the plugin parser so malformed
	// runtime routing material can never be installed.
	if len(credential.ClientKey) < len("cais_")+32 ||
		!strings.HasPrefix(credential.ClientKey, "cais_") ||
		strings.ContainsAny(credential.ClientKey, "\r\n") {
		return errors.New("client key is invalid")
	}
	if credential.UpstreamToken == "" || strings.ContainsAny(credential.UpstreamToken, "\r\n") {
		return errors.New("upstream token is invalid")
	}
	return nil
}

func isManagedCredential(raw []byte, identityID string) bool {
	managedIdentityID, managed := managedCredentialIdentity(raw)
	return managed && managedIdentityID == strings.TrimSpace(identityID)
}

func managedCredentialIdentity(raw []byte) (string, bool) {
	payload, err := decodeJSONMap(raw)
	if err != nil {
		return "", false
	}
	payloadType := strings.ToLower(strings.TrimSpace(jsonStringValue(payload["type"])))
	managed := (payloadType == pluginProviderID || payloadType == legacyProviderID) &&
		strings.EqualFold(strings.TrimSpace(jsonStringValue(payload["auth_mode"])), authMode) &&
		validateIdentityID(jsonStringValue(payload["agent_identity_id"])) == nil
	if !managed {
		return "", false
	}
	return strings.TrimSpace(jsonStringValue(payload["agent_identity_id"])), true
}

func managedCredentialDisabled(raw []byte, exists bool) bool {
	if !exists {
		return false
	}
	payload, err := decodeJSONMap(raw)
	if err != nil {
		return false
	}
	return boolValue(payload["disabled"])
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(typed))
		return trimmed == "true" || trimmed == "1" || trimmed == "yes" || trimmed == "on"
	case float64:
		return typed == 1
	case json.Number:
		return typed.String() == "1"
	default:
		return false
	}
}

func equivalentJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func disabledCredentialJSON(raw []byte) ([]byte, error) {
	return setManagedCredentialDisabled(raw, true)
}

func (m *Manager) verifyIdentity(ctx context.Context, identityID string, want bool) error {
	files, err := m.managedAuthFiles(ctx)
	if err != nil {
		return fmt.Errorf("verify CPA auth-file update: %w", err)
	}
	found := false
	for _, file := range files {
		if file.IdentityID == strings.TrimSpace(identityID) {
			found = true
			break
		}
	}
	if found != want {
		return errors.New("CPA auth-file update did not persist")
	}
	return nil
}

type managedAuthFileCandidate struct {
	Name          string
	RetryNotFound bool
}

// managedAuthFilesForCredentials discovers visible files and deterministic
// names for disabled runtime-only files, then validates the downloaded JSON.
// It deliberately does not trust a filename as proof of ownership.
func (m *Manager) managedAuthFilesForCredentials(ctx context.Context, credentials []Credential) (map[string]managedAuthFile, error) {
	ctx = nonNilContext(ctx)
	entries, err := m.listAuthFileEntries(ctx)
	if err != nil {
		return nil, err
	}

	wantedIDs := make(map[string]struct{}, len(credentials))
	candidateRetries := make(map[string]bool)
	for _, item := range entries {
		name, ok := safeAuthFileName(item.Name)
		if !ok || !isCodexAuthFileName(name) {
			continue
		}
		candidateRetries[name] = true
	}
	for _, rawCredential := range credentials {
		credential := normalizeCredential(rawCredential)
		if validateIdentityID(credential.IdentityID) != nil {
			continue
		}
		wantedIDs[credential.IdentityID] = struct{}{}
		for _, candidate := range managedAuthFileCandidates(credential) {
			if existing, exists := candidateRetries[candidate.Name]; !exists || candidate.RetryNotFound {
				candidateRetries[candidate.Name] = existing || candidate.RetryNotFound
			}
		}
	}

	names := make([]string, 0, len(candidateRetries))
	for name := range candidateRetries {
		names = append(names, name)
	}
	sort.Strings(names)
	files := make(map[string]managedAuthFile, len(names))
	for _, name := range names {
		raw, exists, downloadErr := m.downloadAuthFileWithOptions(ctx, name, candidateRetries[name])
		if downloadErr != nil {
			return nil, downloadErr
		}
		if !exists {
			continue
		}
		identityID, managed := managedCredentialIdentity(raw)
		if !managed {
			continue
		}
		if len(wantedIDs) > 0 {
			if _, wanted := wantedIDs[identityID]; !wanted {
				continue
			}
		}
		files[name] = managedAuthFile{IdentityID: identityID, Raw: append([]byte(nil), raw...)}
	}
	return files, nil
}

func (m *Manager) managedAuthFiles(ctx context.Context) (map[string]managedAuthFile, error) {
	return m.managedAuthFilesForCredentials(ctx, nil)
}

func managedAuthFileCandidates(credential Credential) []managedAuthFileCandidate {
	credential = normalizeCredential(credential)
	if validateIdentityID(credential.IdentityID) != nil {
		return nil
	}
	seen := make(map[string]int)
	result := make([]managedAuthFileCandidate, 0, 9)
	add := func(name string, retryNotFound bool) {
		name, ok := safeAuthFileName(name)
		if !ok {
			return
		}
		if index, exists := seen[name]; exists {
			if retryNotFound {
				result[index].RetryNotFound = true
			}
			return
		}
		seen[name] = len(result)
		result = append(result, managedAuthFileCandidate{Name: name, RetryNotFound: retryNotFound})
	}

	email := sanitizeFilePart(credential.Email)
	if email == "" {
		if legacy, err := legacyAuthFileName(credential.IdentityID); err == nil {
			add(legacy, true)
		}
		return result
	}
	planType := sanitizeFilePart(strings.ToLower(credential.PlanType))
	workspacePrefix := ""
	if accountID := strings.TrimSpace(credential.AccountID); accountID != "" {
		digest := sha256.Sum256([]byte(accountID))
		workspacePrefix = hex.EncodeToString(digest[:])[:8] + "-"
	}

	// The first form is the current canonical name. Suffix-bearing historical
	// names are cheap to retry because they are the names emitted by this
	// sidecar; no-suffix variants are probed once unless they are listed by CPA.
	forms := []struct {
		workspace string
		suffix    bool
		retry404  bool
	}{
		{workspace: workspacePrefix, suffix: true, retry404: true},
		{workspace: workspacePrefix, suffix: false, retry404: false},
		{workspace: "", suffix: true, retry404: true},
		{workspace: "", suffix: false, retry404: false},
	}
	for _, withPlan := range []bool{true, false} {
		for _, form := range forms {
			name := "codex-" + form.workspace + email
			if withPlan && planType != "" {
				name += "-" + planType
			}
			if form.suffix {
				name += managedAuthSuffix
			}
			add(name+".json", form.retry404)
		}
	}
	if legacy, err := legacyAuthFileName(credential.IdentityID); err == nil {
		add(legacy, true)
	}
	return result
}

func safeAuthFileName(value string) (string, bool) {
	name := strings.TrimSpace(value)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) || !strings.HasSuffix(strings.ToLower(name), ".json") {
		return "", false
	}
	return name, true
}

func isCodexAuthFileName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(name, "codex-") && strings.HasSuffix(name, ".json")
}

func managedCredentialMatchScore(raw []byte, credential Credential) (int, bool) {
	credential = normalizeCredential(credential)
	identityID, managed := managedCredentialIdentity(raw)
	if !managed || identityID != credential.IdentityID {
		return 0, false
	}
	payload, err := decodeJSONMap(raw)
	if err != nil {
		return 0, false
	}
	score := 1
	if credential.ClientKey != "" {
		if token := managedSidecarClientKey(payload); token == credential.ClientKey {
			score += 1000
		} else if token != "" {
			// A client key can remain stable for the lifetime of an identity, but
			// accepting an older key here lets an upsert migrate legacy filenames.
			score += 1
		}
	}
	if ok, points := matchingStringField(payload, "account_id", credential.AccountID, false); !ok {
		return 0, false
	} else {
		score += points
	}
	if ok, points := matchingStringField(payload, "chatgpt_user_id", credential.UserID, false); !ok {
		return 0, false
	} else {
		score += points
	}
	if ok, points := matchingStringField(payload, "email", credential.Email, true); !ok {
		return 0, false
	} else {
		score += points
	}
	if ok, points := matchingStringField(payload, "plan_type", credential.PlanType, true); !ok {
		return 0, false
	} else {
		score += points
	}
	if ok, points := matchingStringField(payload, "credential_kind", credential.Kind, true); !ok {
		return 0, false
	} else {
		score += points
	}
	return score, true
}

func managedSidecarClientKey(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	if key := strings.TrimSpace(jsonStringValue(payload[sidecarClientKeyField])); key != "" {
		return key
	}
	// Legacy files stored the sidecar client key directly in access_token.
	return strings.TrimSpace(jsonStringValue(payload["access_token"]))
}

func matchingStringField(payload map[string]any, key, want string, foldCase bool) (bool, int) {
	want = strings.TrimSpace(want)
	if want == "" {
		return true, 0
	}
	got := strings.TrimSpace(jsonStringValue(payload[key]))
	if got == "" {
		// Legacy sidecar files may not contain all metadata. Identity and the
		// remaining fields still provide the ownership boundary.
		return true, 2
	}
	if foldCase {
		if !strings.EqualFold(got, want) {
			return false, 0
		}
	} else if got != want {
		return false, 0
	}
	return true, 25
}

func matchingManagedFileNames(files map[string]managedAuthFile, credential Credential) []string {
	credential = normalizeCredential(credential)
	names := make([]string, 0, len(files))
	for name, file := range files {
		if _, ok := managedCredentialMatchScore(file.Raw, credential); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func preferredManagedFile(files map[string]managedAuthFile, credential Credential) (string, managedAuthFile, bool) {
	credential = normalizeCredential(credential)
	names := matchingManagedFileNames(files, credential)
	if len(names) == 0 {
		return "", managedAuthFile{}, false
	}
	if canonical, err := authFileName(credential); err == nil {
		for _, name := range names {
			if name == canonical {
				return name, files[name], true
			}
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		leftScore, _ := managedCredentialMatchScore(files[names[i]].Raw, credential)
		rightScore, _ := managedCredentialMatchScore(files[names[j]].Raw, credential)
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return names[i] < names[j]
	})
	return names[0], files[names[0]], true
}

func snapshotsForCredential(files map[string]managedAuthFile, credential Credential) []AuthFileSnapshot {
	names := matchingManagedFileNames(files, credential)
	result := make([]AuthFileSnapshot, 0, len(names))
	for _, name := range names {
		file := files[name]
		result = append(result, AuthFileSnapshot{
			Name:       name,
			IdentityID: file.IdentityID,
			Raw:        append([]byte(nil), file.Raw...),
			Disabled:   managedCredentialDisabled(file.Raw, true),
		})
	}
	return result
}

var managedCPAFieldKeys = map[string]struct{}{
	"priority":              {},
	"weight":                {},
	"proxy_url":             {},
	"proxy-url":             {},
	"headers":               {},
	"model_aliases":         {},
	"model-aliases":         {},
	"excluded_models":       {},
	"excluded-models":       {},
	"request_retry":         {},
	"request-retry":         {},
	"prefix":                {},
	"disable_cooling":       {},
	"disable-cooling":       {},
	"request_scoped_errors": {},
	"request-scoped-errors": {},
	"tool_prefix_disabled":  {},
	"tool-prefix-disabled":  {},
	"fingerprint_profile":   {},
	"fingerprint-profile":   {},
	"note":                  {},
}

func mergeManagedAuthFields(nextRaw, priorRaw []byte) ([]byte, error) {
	next, err := decodeJSONMap(nextRaw)
	if err != nil {
		return nil, errors.New("encode CPA auth file")
	}
	prior, err := decodeJSONMap(priorRaw)
	if err != nil {
		return nil, errors.New("read previous CPA auth file")
	}
	for key := range managedCPAFieldKeys {
		value, exists := prior[key]
		if !exists {
			continue
		}
		if key == "note" {
			if note := strings.TrimSpace(jsonStringValue(value)); note != "" {
				next[key] = value
			}
			continue
		}
		if _, exists := next[key]; !exists {
			next[key] = value
		}
	}
	return json.MarshalIndent(next, "", "  ")
}

func managedCredentialMatches(raw []byte, credential Credential, expectedRaw []byte) bool {
	if _, ok := managedCredentialMatchScore(raw, credential); !ok {
		return false
	}
	actual, err := decodeJSONMap(raw)
	if err != nil {
		return false
	}
	expected, err := decodeJSONMap(expectedRaw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(jsonStringValue(actual["auth_mode"]), authMode) ||
		strings.TrimSpace(jsonStringValue(actual["agent_identity_id"])) != credential.IdentityID {
		return false
	}
	if credential.ClientKey != "" && managedSidecarClientKey(actual) != credential.ClientKey {
		return false
	}
	if credential.UpstreamToken != "" && strings.TrimSpace(jsonStringValue(actual["access_token"])) != credential.UpstreamToken {
		return false
	}
	if !sameNormalizedString(actual, expected, "base_url", true) || !sameBoolean(actual, expected, "disabled") {
		return false
	}
	if runtimeOnly, exists := actual["runtime_only"]; exists && !boolValue(runtimeOnly) {
		return false
	}
	for _, key := range []string{"email", "account_id", "chatgpt_user_id", "plan_type", "credential_kind", "expires_at", "fedramp"} {
		if expectedValue, exists := expected[key]; exists {
			actualValue, actualExists := actual[key]
			if !actualExists || !jsonValuesEquivalent(key, actualValue, expectedValue) {
				return false
			}
		}
	}
	if expectedWebsockets, exists := expected["websockets"]; exists {
		if actualWebsockets, actualExists := actual["websockets"]; actualExists && !jsonValuesEquivalent("websockets", actualWebsockets, expectedWebsockets) {
			return false
		}
	}
	return true
}

func managedCredentialStateMatches(raw []byte, credential Credential, disabled bool) bool {
	if _, ok := managedCredentialMatchScore(raw, credential); !ok {
		return false
	}
	return managedCredentialDisabled(raw, true) == disabled
}

func setManagedCredentialDisabled(raw []byte, disabled bool) ([]byte, error) {
	if _, managed := managedCredentialIdentity(raw); !managed {
		return nil, errors.New("CPA auth file is not managed by Codex Agent Identity")
	}
	payload, err := decodeJSONMap(raw)
	if err != nil {
		return nil, errors.New("encode CPA auth file state")
	}
	payload["disabled"] = disabled
	return json.MarshalIndent(payload, "", "  ")
}

func decodeJSONMap(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, errors.New("CPA auth file must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("CPA auth file has trailing data")
	}
	return payload, nil
}

func jsonStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return fmt.Sprintf("%v", typed)
	case bool:
		return fmt.Sprintf("%t", typed)
	default:
		return ""
	}
}

func sameNormalizedString(actual, expected map[string]any, key string, trimSlash bool) bool {
	want, wantExists := expected[key]
	got, gotExists := actual[key]
	if !wantExists {
		return true
	}
	if !gotExists {
		return false
	}
	left := strings.TrimSpace(jsonStringValue(got))
	right := strings.TrimSpace(jsonStringValue(want))
	if trimSlash {
		left = strings.TrimRight(left, "/")
		right = strings.TrimRight(right, "/")
	}
	return left == right
}

func sameBoolean(actual, expected map[string]any, key string) bool {
	want, wantExists := expected[key]
	if !wantExists {
		return true
	}
	got, gotExists := actual[key]
	return gotExists && boolValue(got) == boolValue(want)
}

func jsonValuesEquivalent(key string, left, right any) bool {
	if key == "plan_type" || key == "credential_kind" || key == "email" {
		return strings.EqualFold(strings.TrimSpace(jsonStringValue(left)), strings.TrimSpace(jsonStringValue(right)))
	}
	if key == "expires_at" {
		return strings.TrimSpace(jsonStringValue(left)) == strings.TrimSpace(jsonStringValue(right))
	}
	if key == "fedramp" || key == "websockets" {
		return boolValue(left) == boolValue(right)
	}
	return reflect.DeepEqual(left, right) || strings.TrimSpace(jsonStringValue(left)) == strings.TrimSpace(jsonStringValue(right))
}

func (m *Manager) restoreManagedFiles(files map[string][]byte) error {
	if len(files) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), managementVerifyWindow+2*time.Second)
	defer cancel()
	return m.restoreManagedFilesWithContext(ctx, files)
}

func (m *Manager) restoreManagedFilesWithContext(ctx context.Context, files map[string][]byte) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := safeAuthFileName(name); !ok {
			return errors.New("CPA auth-file restore name is invalid")
		}
		if err := m.uploadAuthFile(ctx, name, files[name]); err != nil {
			return err
		}
		if _, err := m.waitForAuthFile(ctx, name, func(raw []byte) bool {
			return equivalentJSON(raw, files[name])
		}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) rollbackManagedFiles(name string, previous []byte, existed bool, deleted map[string][]byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), managementVerifyWindow+2*time.Second)
	defer cancel()
	var firstErr error
	if name != "" {
		if existed {
			if err := m.uploadAuthFile(ctx, name, previous); err != nil {
				firstErr = err
			} else if _, err := m.waitForAuthFile(ctx, name, func(raw []byte) bool {
				return equivalentJSON(raw, previous)
			}); err != nil && firstErr == nil {
				firstErr = err
			}
		} else {
			if err := m.deleteAuthFile(ctx, name); err != nil && firstErr == nil {
				firstErr = err
			}
			if err := m.waitForAuthFileAbsent(ctx, name); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := m.restoreManagedFilesWithContext(ctx, deleted); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func managementRetryDelay(attempt int) time.Duration {
	delay := managementRetryInitial
	for index := 0; index < attempt && delay < managementRetryMaximum; index++ {
		if delay > managementRetryMaximum/2 {
			return managementRetryMaximum
		}
		delay *= 2
	}
	if delay > managementRetryMaximum {
		return managementRetryMaximum
	}
	return delay
}

func waitManagementRetry(ctx context.Context, delay time.Duration) error {
	ctx = nonNilContext(ctx)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type managementStatusError struct {
	operation string
	status    int
}

func (e *managementStatusError) Error() string {
	if e == nil {
		return "CPA management request failed"
	}
	return fmt.Sprintf("%s: status %d", e.operation, e.status)
}

func retryableManagementError(err error, retryNotFound bool) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var statusErr *managementStatusError
	if errors.As(err, &statusErr) {
		switch {
		case statusErr.status == http.StatusNotFound:
			return retryNotFound
		case statusErr.status == http.StatusRequestTimeout,
			statusErr.status == http.StatusTooEarly,
			statusErr.status == http.StatusTooManyRequests,
			statusErr.status >= 500:
			return true
		default:
			return false
		}
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func (m *Manager) waitForAuthFile(ctx context.Context, name string, predicate func([]byte) bool) ([]byte, error) {
	if predicate == nil {
		return nil, errors.New("CPA auth-file verification predicate is required")
	}
	if _, ok := safeAuthFileName(name); !ok {
		return nil, errors.New("CPA auth-file verification name is invalid")
	}
	ctx = nonNilContext(ctx)
	verifyCtx, cancel := context.WithTimeout(ctx, managementVerifyWindow)
	defer cancel()
	interval := managementRetryInitial
	var lastErr error
	for {
		raw, exists, err := m.downloadAuthFileWithOptions(verifyCtx, name, true)
		if err == nil {
			if exists && predicate(raw) {
				return raw, nil
			}
			lastErr = errors.New("CPA auth-file contents are not yet visible")
		} else {
			if !retryableManagementError(err, true) {
				return nil, err
			}
			lastErr = err
		}
		if verifyCtx.Err() != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("CPA auth-file update did not persist: %w", lastErr)
		}
		if err := waitManagementRetry(verifyCtx, interval); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("CPA auth-file update did not persist: %w", lastErr)
		}
		if interval < managementRetryMaximum {
			interval *= 2
			if interval > managementRetryMaximum {
				interval = managementRetryMaximum
			}
		}
	}
}

func (m *Manager) waitForAuthFileAbsent(ctx context.Context, name string) error {
	if _, ok := safeAuthFileName(name); !ok {
		return errors.New("CPA auth-file verification name is invalid")
	}
	ctx = nonNilContext(ctx)
	verifyCtx, cancel := context.WithTimeout(ctx, managementVerifyWindow)
	defer cancel()
	interval := managementRetryInitial
	var lastErr error
	for {
		_, exists, err := m.downloadAuthFileWithOptions(verifyCtx, name, true)
		if err == nil {
			if !exists {
				return nil
			}
			lastErr = errors.New("CPA auth-file is still present")
		} else {
			if !retryableManagementError(err, true) {
				return err
			}
			lastErr = err
		}
		if verifyCtx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("CPA auth-file deletion did not persist: %w", lastErr)
		}
		if err := waitManagementRetry(verifyCtx, interval); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("CPA auth-file deletion did not persist: %w", lastErr)
		}
		if interval < managementRetryMaximum {
			interval *= 2
			if interval > managementRetryMaximum {
				interval = managementRetryMaximum
			}
		}
	}
}

func (m *Manager) listAuthFileEntries(ctx context.Context) ([]authFileEntry, error) {
	ctx = nonNilContext(ctx)
	var lastErr error
	for attempt := 0; attempt < managementRetryAttempts; attempt++ {
		request, err := m.newRequest(ctx, http.MethodGet, "/auth-files", nil, "")
		if err != nil {
			return nil, err
		}
		response, err := m.client.Do(request)
		if err != nil {
			lastErr = fmt.Errorf("list CPA auth files: %w", err)
		} else {
			body := response.Body
			if response.StatusCode != http.StatusOK {
				_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<20))
				_ = body.Close()
				lastErr = &managementStatusError{operation: "list CPA auth files", status: response.StatusCode}
			} else {
				var wrapper struct {
					Files []authFileEntry `json:"files"`
				}
				decodeErr := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&wrapper)
				_ = body.Close()
				if decodeErr != nil {
					return nil, errors.New("list CPA auth files: invalid response")
				}
				sort.SliceStable(wrapper.Files, func(i, j int) bool {
					return wrapper.Files[i].Name < wrapper.Files[j].Name
				})
				return wrapper.Files, nil
			}
		}
		if attempt+1 >= managementRetryAttempts || !retryableManagementError(lastErr, false) {
			return nil, lastErr
		}
		if err := waitManagementRetry(ctx, managementRetryDelay(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (m *Manager) downloadAuthFile(ctx context.Context, name string) ([]byte, bool, error) {
	return m.downloadAuthFileWithOptions(ctx, name, true)
}

func (m *Manager) downloadAuthFileWithOptions(ctx context.Context, name string, retryNotFound bool) ([]byte, bool, error) {
	if _, ok := safeAuthFileName(name); !ok {
		return nil, false, errors.New("CPA auth-file name is invalid")
	}
	ctx = nonNilContext(ctx)
	var lastErr error
	for attempt := 0; attempt < managementRetryAttempts; attempt++ {
		request, err := m.newRequest(ctx, http.MethodGet, "/auth-files/download", nil, name)
		if err != nil {
			return nil, false, err
		}
		response, err := m.client.Do(request)
		if err != nil {
			lastErr = fmt.Errorf("download CPA auth file: %w", err)
		} else if response.StatusCode == http.StatusNotFound {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if !retryNotFound || attempt+1 >= managementRetryAttempts {
				return nil, false, nil
			}
			lastErr = &managementStatusError{operation: "download CPA auth file", status: http.StatusNotFound}
		} else if response.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			lastErr = &managementStatusError{operation: "download CPA auth file", status: response.StatusCode}
		} else {
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
			_ = response.Body.Close()
			if readErr != nil {
				lastErr = fmt.Errorf("download CPA auth file: %w", readErr)
			} else {
				return raw, true, nil
			}
		}
		if attempt+1 >= managementRetryAttempts || !retryableManagementError(lastErr, retryNotFound) {
			return nil, false, lastErr
		}
		if err := waitManagementRetry(ctx, managementRetryDelay(attempt)); err != nil {
			return nil, false, err
		}
	}
	return nil, false, lastErr
}

func (m *Manager) uploadAuthFile(ctx context.Context, name string, raw []byte) error {
	if _, ok := safeAuthFileName(name); !ok {
		return errors.New("CPA auth-file name is invalid")
	}
	ctx = nonNilContext(ctx)
	var lastErr error
	for attempt := 0; attempt < managementRetryAttempts; attempt++ {
		request, err := m.newRequest(ctx, http.MethodPost, "/auth-files", bytes.NewReader(raw), name)
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := m.client.Do(request)
		if err != nil {
			lastErr = fmt.Errorf("upload CPA auth file: %w", err)
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			lastErr = &managementStatusError{operation: "upload CPA auth file", status: response.StatusCode}
		}
		if attempt+1 >= managementRetryAttempts || !retryableManagementError(lastErr, false) {
			return lastErr
		}
		if err := waitManagementRetry(ctx, managementRetryDelay(attempt)); err != nil {
			return err
		}
	}
	return lastErr
}

func (m *Manager) deleteAuthFile(ctx context.Context, name string) error {
	if _, ok := safeAuthFileName(name); !ok {
		return errors.New("CPA auth-file name is invalid")
	}
	ctx = nonNilContext(ctx)
	var lastErr error
	for attempt := 0; attempt < managementRetryAttempts; attempt++ {
		request, err := m.newRequest(ctx, http.MethodDelete, "/auth-files", nil, name)
		if err != nil {
			return err
		}
		response, err := m.client.Do(request)
		if err != nil {
			lastErr = fmt.Errorf("delete CPA auth file: %w", err)
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if response.StatusCode == http.StatusNotFound || (response.StatusCode >= 200 && response.StatusCode < 300) {
				return nil
			}
			lastErr = &managementStatusError{operation: "delete CPA auth file", status: response.StatusCode}
		}
		if attempt+1 >= managementRetryAttempts || !retryableManagementError(lastErr, false) {
			return lastErr
		}
		if err := waitManagementRetry(ctx, managementRetryDelay(attempt)); err != nil {
			return err
		}
	}
	return lastErr
}

func (m *Manager) newRequest(ctx context.Context, method, endpoint string, body io.Reader, name string) (*http.Request, error) {
	target := *m.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + endpoint
	query := target.Query()
	if name != "" {
		query.Set("name", name)
	}
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+m.managementKey)
	request.Header.Set("Accept", "application/json")
	return request, nil
}
