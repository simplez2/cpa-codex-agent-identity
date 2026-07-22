package cpa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

const (
	authMode       = "agent_identity_sidecar"
	authFilePrefix = "codex-agent-identity-"
	pluginSettle   = 750 * time.Millisecond
)

// Credential is the non-secret CPA-facing representation of an Agent Identity.
// ClientKey is an opaque sidecar key; the original Agent Identity JWT never enters CPA.
type Credential struct {
	IdentityID string
	ClientKey  string
	Kind       string
	AccountID  string
	UserID     string
	Email      string
	PlanType   string
	ExpiresAt  time.Time
	FedRAMP    bool
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

// IdentityState is the non-secret CPA synchronization state for one sidecar identity.
type IdentityState struct {
	Synced   bool   `json:"synced"`
	Disabled bool   `json:"disabled"`
	AuthFile string `json:"auth_file,omitempty"`
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

// UpsertIdentity creates or replaces the sidecar-owned Codex auth file for an identity.
func (m *Manager) UpsertIdentity(ctx context.Context, credential Credential) error {
	credential.IdentityID = strings.TrimSpace(credential.IdentityID)
	credential.ClientKey = strings.TrimSpace(credential.ClientKey)
	credential.AccountID = strings.TrimSpace(credential.AccountID)
	credential.UserID = strings.TrimSpace(credential.UserID)
	credential.Email = strings.TrimSpace(credential.Email)
	credential.PlanType = strings.TrimSpace(credential.PlanType)
	credential.Kind = strings.TrimSpace(credential.Kind)
	if credential.IdentityID == "" || credential.ClientKey == "" {
		return errors.New("identity ID and client key are required")
	}
	if !strings.HasPrefix(credential.ClientKey, "cais_") {
		return errors.New("sidecar client key is invalid")
	}
	name, err := authFileName(credential)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	managedBefore, err := m.managedAuthFiles(ctx)
	if err != nil {
		return err
	}
	previous, existed, err := m.downloadAuthFile(ctx, name)
	if err != nil {
		return err
	}
	if existed && !isManagedCredential(previous, credential.IdentityID) {
		return errors.New("refusing to overwrite an unmanaged CPA auth file")
	}
	disabled := managedCredentialDisabled(previous, existed)
	if !existed {
		for oldName, old := range managedBefore {
			if oldName != name && old.IdentityID == credential.IdentityID {
				disabled = managedCredentialDisabled(old.Raw, true)
				break
			}
		}
	}
	body, err := m.credentialJSONWithDisabled(credential, disabled)
	if err != nil {
		return err
	}
	if existed && equivalentJSON(previous, body) {
		for oldName, old := range managedBefore {
			if old.IdentityID == credential.IdentityID && oldName != name {
				if deleteErr := m.deleteAuthFile(ctx, oldName); deleteErr != nil {
					return deleteErr
				}
			}
		}
		return m.verifyIdentity(ctx, credential.IdentityID, true)
	}
	staged, err := disabledCredentialJSON(body)
	if err != nil {
		return err
	}
	if err = m.uploadAuthFile(ctx, name, staged); err != nil {
		m.rollbackAuthFile(name, previous, existed)
		return err
	}
	select {
	case <-ctx.Done():
		m.rollbackAuthFile(name, previous, existed)
		return ctx.Err()
	case <-time.After(pluginSettle):
	}
	if !disabled {
		if err = m.patchAuthFileField(ctx, name, "disabled", false); err != nil {
			m.rollbackAuthFile(name, previous, existed)
			return err
		}
	}
	if current, currentExists, downloadErr := m.downloadAuthFile(ctx, name); downloadErr != nil || !currentExists || !isManagedCredential(current, credential.IdentityID) {
		m.rollbackAuthFile(name, previous, existed)
		if downloadErr != nil {
			return downloadErr
		}
		return errors.New("CPA auth-file update did not persist")
	}
	deleted := make(map[string][]byte)
	for oldName, old := range managedBefore {
		if old.IdentityID != credential.IdentityID || oldName == name {
			continue
		}
		if err = m.deleteAuthFile(ctx, oldName); err != nil {
			for deletedName, deletedRaw := range deleted {
				_ = m.uploadAuthFile(context.Background(), deletedName, deletedRaw)
			}
			m.rollbackAuthFile(name, previous, existed)
			return err
		}
		deleted[oldName] = old.Raw
	}
	if err = m.verifyIdentity(ctx, credential.IdentityID, true); err != nil {
		for deletedName, deletedRaw := range deleted {
			_ = m.uploadAuthFile(context.Background(), deletedName, deletedRaw)
		}
		m.rollbackAuthFile(name, previous, existed)
		return err
	}
	return nil
}

// RemoveIdentity removes only the sidecar-owned auth file for this identity.
func (m *Manager) RemoveIdentity(ctx context.Context, identityID string) error {
	identityID = strings.TrimSpace(identityID)
	if err := validateIdentityID(identityID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	managed, err := m.managedAuthFiles(ctx)
	if err != nil {
		return err
	}
	deleted := make(map[string][]byte)
	for name, file := range managed {
		if file.IdentityID != identityID {
			continue
		}
		if err = m.deleteAuthFile(ctx, name); err != nil {
			for deletedName, deletedRaw := range deleted {
				_ = m.uploadAuthFile(context.Background(), deletedName, deletedRaw)
			}
			return err
		}
		deleted[name] = file.Raw
	}
	if err = m.verifyIdentity(ctx, identityID, false); err != nil {
		for deletedName, deletedRaw := range deleted {
			_ = m.uploadAuthFile(context.Background(), deletedName, deletedRaw)
		}
		return err
	}
	return nil
}

// SetIdentityDisabled changes the native CPA auth-file state without rotating the sidecar key.
func (m *Manager) SetIdentityDisabled(ctx context.Context, identityID string, disabled bool) error {
	identityID = strings.TrimSpace(identityID)
	if err := validateIdentityID(identityID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	managed, err := m.managedAuthFiles(ctx)
	if err != nil {
		return err
	}
	name := ""
	for fileName, file := range managed {
		if file.IdentityID == identityID {
			name = fileName
			break
		}
	}
	if name == "" {
		return errors.New("CPA Codex credential is not synchronized")
	}
	if err = m.patchAuthFileField(ctx, name, "disabled", disabled); err != nil {
		return err
	}
	raw, exists, err := m.downloadAuthFile(ctx, name)
	if err != nil {
		return err
	}
	storedID, okManaged := managedCredentialIdentity(raw)
	if !exists || !okManaged || storedID != identityID || managedCredentialDisabled(raw, true) != disabled {
		return errors.New("CPA Codex credential state did not persist")
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
	files, err := m.managedAuthFiles(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]IdentityState, len(identityIDs))
	for _, id := range identityIDs {
		result[strings.TrimSpace(id)] = IdentityState{}
	}
	for name, file := range files {
		if _, tracked := result[file.IdentityID]; !tracked {
			continue
		}
		result[file.IdentityID] = IdentityState{
			Synced:   true,
			Disabled: managedCredentialDisabled(file.Raw, true),
			AuthFile: filepath.Base(name),
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
	payload := map[string]any{
		"type":              "codex",
		"auth_mode":         authMode,
		"email":             email,
		"access_token":      credential.ClientKey,
		"base_url":          m.sidecarBaseURL,
		"websockets":        true,
		"disabled":          disabled,
		"agent_identity_id": credential.IdentityID,
		"note":              "Agent Identity via sidecar",
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
	name := "codex-" + email
	if planType != "" {
		name += "-" + planType
	}
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

func isManagedCredential(raw []byte, identityID string) bool {
	managedIdentityID, managed := managedCredentialIdentity(raw)
	return managed && managedIdentityID == strings.TrimSpace(identityID)
}

func managedCredentialIdentity(raw []byte) (string, bool) {
	var payload struct {
		Type       string `json:"type"`
		AuthMode   string `json:"auth_mode"`
		IdentityID string `json:"agent_identity_id"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return "", false
	}
	managed := strings.EqualFold(strings.TrimSpace(payload.Type), "codex") &&
		strings.EqualFold(strings.TrimSpace(payload.AuthMode), authMode) &&
		validateIdentityID(payload.IdentityID) == nil
	return strings.TrimSpace(payload.IdentityID), managed
}

func managedCredentialDisabled(raw []byte, exists bool) bool {
	if !exists {
		return false
	}
	var payload struct {
		Disabled bool `json:"disabled"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	return payload.Disabled
}

func equivalentJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func disabledCredentialJSON(raw []byte) ([]byte, error) {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return nil, errors.New("encode staged CPA auth file")
	}
	payload["disabled"] = true
	return json.MarshalIndent(payload, "", "  ")
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

func (m *Manager) managedAuthFiles(ctx context.Context) (map[string]managedAuthFile, error) {
	entries, err := m.listAuthFileEntries(ctx)
	if err != nil {
		return nil, err
	}
	files := make(map[string]managedAuthFile)
	for _, item := range entries {
		name := filepath.Base(strings.TrimSpace(item.Name))
		if !strings.HasPrefix(strings.ToLower(name), "codex-") || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		raw, exists, downloadErr := m.downloadAuthFile(ctx, name)
		if downloadErr != nil {
			return nil, downloadErr
		}
		if !exists {
			continue
		}
		identityID, managed := managedCredentialIdentity(raw)
		if managed {
			files[name] = managedAuthFile{IdentityID: identityID, Raw: raw}
		}
	}
	return files, nil
}

func (m *Manager) listAuthFileEntries(ctx context.Context) ([]authFileEntry, error) {
	request, err := m.newRequest(ctx, http.MethodGet, "/auth-files", nil, "")
	if err != nil {
		return nil, err
	}
	response, err := m.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list CPA auth files: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list CPA auth files: status %d", response.StatusCode)
	}
	var wrapper struct {
		Files []authFileEntry `json:"files"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&wrapper) != nil {
		return nil, errors.New("list CPA auth files: invalid response")
	}
	return wrapper.Files, nil
}

func (m *Manager) downloadAuthFile(ctx context.Context, name string) ([]byte, bool, error) {
	request, err := m.newRequest(ctx, http.MethodGet, "/auth-files/download", nil, name)
	if err != nil {
		return nil, false, err
	}
	response, err := m.client.Do(request)
	if err != nil {
		return nil, false, fmt.Errorf("download CPA auth file: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("download CPA auth file: status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, false, errors.New("download CPA auth file: invalid response")
	}
	return raw, true, nil
}

func (m *Manager) uploadAuthFile(ctx context.Context, name string, raw []byte) error {
	request, err := m.newRequest(ctx, http.MethodPost, "/auth-files", bytes.NewReader(raw), name)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := m.client.Do(request)
	if err != nil {
		return fmt.Errorf("upload CPA auth file: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("upload CPA auth file: status %d", response.StatusCode)
	}
	return nil
}

func (m *Manager) deleteAuthFile(ctx context.Context, name string) error {
	request, err := m.newRequest(ctx, http.MethodDelete, "/auth-files", nil, name)
	if err != nil {
		return err
	}
	response, err := m.client.Do(request)
	if err != nil {
		return fmt.Errorf("delete CPA auth file: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("delete CPA auth file: status %d", response.StatusCode)
	}
	return nil
}

func (m *Manager) patchAuthFileField(ctx context.Context, name, field string, value any) error {
	body, err := json.Marshal(map[string]any{"name": name, field: value})
	if err != nil {
		return errors.New("encode CPA auth-file field update")
	}
	request, err := m.newRequest(ctx, http.MethodPatch, "/auth-files/fields", bytes.NewReader(body), "")
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := m.client.Do(request)
	if err != nil {
		return fmt.Errorf("patch CPA auth file: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("patch CPA auth file: status %d", response.StatusCode)
	}
	return nil
}

func (m *Manager) rollbackAuthFile(name string, previous []byte, existed bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if existed {
		_ = m.uploadAuthFile(ctx, name, previous)
		return
	}
	_ = m.deleteAuthFile(ctx, name)
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
