package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// PersonalAccessTokenPrefix matches the current official Codex CLI token classifier.
	PersonalAccessTokenPrefix = "at-"
	// DefaultPersonalAccessTokenAuthAPIURL is the official account credential API base.
	DefaultPersonalAccessTokenAuthAPIURL = "https://auth.openai.com/api/accounts"
	personalAccessTokenWhoAmIPath        = "/v1/user-auth-credential/whoami"
)

// CredentialKind identifies the upstream authentication protocol for a stored token.
type CredentialKind string

const (
	CredentialKindAgentIdentity       CredentialKind = "agent_identity"
	CredentialKindPersonalAccessToken CredentialKind = "personal_access_token"
)

// CredentialInfo is verified non-secret metadata used for CPA channel synchronization.
type CredentialInfo struct {
	Kind      CredentialKind
	AccountID string
	UserID    string
	Email     string
	PlanType  string
	ExpiresAt time.Time
	FedRAMP   bool
	TokenHash string
}

type personalAccessTokenWhoAmI struct {
	Email                   string `json:"email"`
	ChatGPTUserID           string `json:"chatgpt_user_id"`
	ChatGPTAccountID        string `json:"chatgpt_account_id"`
	ChatGPTPlanType         string `json:"chatgpt_plan_type"`
	ChatGPTAccountIsFedRAMP bool   `json:"chatgpt_account_is_fedramp"`
}

// IsPersonalAccessToken reports whether a token uses the official opaque PAT prefix.
func IsPersonalAccessToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), PersonalAccessTokenPrefix)
}

// Inspect verifies a supported Codex access token and returns display/routing metadata.
func (m *Manager) Inspect(ctx context.Context, token string) (*CredentialInfo, error) {
	if IsPersonalAccessToken(token) {
		return m.personalAccessToken(ctx, token)
	}
	material, err := m.material(ctx, token)
	if err != nil {
		return nil, err
	}
	return &CredentialInfo{
		Kind:      CredentialKindAgentIdentity,
		AccountID: material.Claims.AccountID,
		UserID:    material.Claims.ChatGPTUserID,
		Email:     material.Claims.Email,
		PlanType:  material.Claims.PlanType,
		ExpiresAt: time.Unix(material.Claims.ExpiresAt, 0),
		FedRAMP:   material.Claims.ChatGPTAccountIsFedRAMP,
		TokenHash: material.TokenHash,
	}, nil
}

func (m *Manager) personalAccessToken(ctx context.Context, token string) (*CredentialInfo, error) {
	if m == nil {
		return nil, errors.New("credential manager is unavailable")
	}
	token = strings.TrimSpace(token)
	if !IsPersonalAccessToken(token) {
		return nil, errors.New("personal access token is invalid")
	}
	digest := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(digest[:])
	m.mu.Lock()
	if cached := m.personalAccessTokens[tokenHash]; cached != nil {
		copyCredential := *cached
		m.mu.Unlock()
		return &copyCredential, nil
	}
	m.mu.Unlock()

	endpoint := strings.TrimRight(m.personalAccessTokenAuthAPIBase, "/") + personalAccessTokenWhoAmIPath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrCredentialServiceUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := m.client.Do(request)
	if err != nil {
		return nil, ErrCredentialServiceUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return nil, ErrCredentialServiceUnavailable
		}
		return nil, ErrCredentialInvalid
	}
	var metadata personalAccessTokenWhoAmI
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&metadata) != nil {
		return nil, ErrCredentialServiceUnavailable
	}
	metadata.ChatGPTAccountID = strings.TrimSpace(metadata.ChatGPTAccountID)
	metadata.ChatGPTUserID = strings.TrimSpace(metadata.ChatGPTUserID)
	if metadata.ChatGPTAccountID == "" || metadata.ChatGPTUserID == "" {
		return nil, ErrCredentialServiceUnavailable
	}
	credential := &CredentialInfo{
		Kind:      CredentialKindPersonalAccessToken,
		AccountID: metadata.ChatGPTAccountID,
		UserID:    metadata.ChatGPTUserID,
		Email:     strings.TrimSpace(metadata.Email),
		PlanType:  strings.TrimSpace(metadata.ChatGPTPlanType),
		FedRAMP:   metadata.ChatGPTAccountIsFedRAMP,
		TokenHash: tokenHash,
	}
	m.mu.Lock()
	m.personalAccessTokens[tokenHash] = credential
	m.mu.Unlock()
	copyCredential := *credential
	return &copyCredential, nil
}
