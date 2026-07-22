package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const registerAttempts = 3

type taskFlight struct {
	done   chan struct{}
	taskID string
	err    error
}

// Authorization is the per-request upstream Agent Identity authorization state.
type Authorization struct {
	Header    string
	AccountID string
	FedRAMP   bool
	TokenHash string
	Kind      CredentialKind
}

// Manager verifies identities, caches task registrations, and creates assertions.
type Manager struct {
	mu                             sync.Mutex
	materials                      map[string]*Material
	tasks                          map[string]string
	flights                        map[string]*taskFlight
	jwks                           *JWKSCache
	authAPIBase                    string
	personalAccessTokens           map[string]*CredentialInfo
	personalAccessTokenAuthAPIBase string
	client                         *http.Client
	now                            func() time.Time
}

// NewManager creates an Agent Identity manager.
func NewManager(jwksURL, authAPIBase string, client *http.Client) *Manager {
	return NewManagerWithPersonalAccessTokenAPI(jwksURL, authAPIBase, DefaultPersonalAccessTokenAuthAPIURL, client)
}

// NewManagerWithPersonalAccessTokenAPI creates a Codex credential manager with
// independently configurable Agent Identity and Personal Access Token endpoints.
func NewManagerWithPersonalAccessTokenAPI(jwksURL, authAPIBase, personalAccessTokenAuthAPIBase string, client *http.Client) *Manager {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if strings.TrimSpace(jwksURL) == "" {
		jwksURL = DefaultJWKSURL
	}
	if strings.TrimSpace(authAPIBase) == "" {
		authAPIBase = DefaultAuthAPIURL
	}
	if strings.TrimSpace(personalAccessTokenAuthAPIBase) == "" {
		personalAccessTokenAuthAPIBase = DefaultPersonalAccessTokenAuthAPIURL
	}
	return &Manager{
		materials:                      make(map[string]*Material),
		tasks:                          make(map[string]string),
		flights:                        make(map[string]*taskFlight),
		jwks:                           NewJWKSCache(jwksURL, client),
		authAPIBase:                    strings.TrimRight(strings.TrimSpace(authAPIBase), "/"),
		personalAccessTokens:           make(map[string]*CredentialInfo),
		personalAccessTokenAuthAPIBase: strings.TrimRight(strings.TrimSpace(personalAccessTokenAuthAPIBase), "/"),
		client:                         client,
		now:                            time.Now,
	}
}

// Validate verifies a token without registering a task.
func (m *Manager) Validate(ctx context.Context, token string) (*Material, error) {
	return m.material(ctx, token)
}

func (m *Manager) material(ctx context.Context, token string) (*Material, error) {
	token = strings.TrimSpace(token)
	digest := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(digest[:])
	m.mu.Lock()
	material := m.materials[tokenHash]
	if material != nil && m.now().Unix() < material.Claims.ExpiresAt {
		m.mu.Unlock()
		return material, nil
	}
	delete(m.materials, tokenHash)
	m.mu.Unlock()

	material, err := ParseWithCache(ctx, token, m.jwks, m.now())
	if err != nil {
		if CredentialServiceUnavailable(err) {
			return nil, err
		}
		return nil, ErrCredentialInvalid
	}
	m.mu.Lock()
	m.materials[tokenHash] = material
	m.mu.Unlock()
	return material, nil
}

// Authorize creates a fresh AgentAssertion for one upstream request.
func (m *Manager) Authorize(ctx context.Context, identityID, token, sessionID string) (*Authorization, error) {
	if IsPersonalAccessToken(token) {
		credential, err := m.personalAccessToken(ctx, token)
		if err != nil {
			return nil, err
		}
		return &Authorization{
			Header:    "Bearer " + strings.TrimSpace(token),
			AccountID: credential.AccountID,
			FedRAMP:   credential.FedRAMP,
			TokenHash: credential.TokenHash,
			Kind:      credential.Kind,
		}, nil
	}
	material, err := m.material(ctx, token)
	if err != nil {
		return nil, err
	}
	taskKey := m.taskKey(identityID, material.TokenHash, sessionID)
	taskID, err := m.task(ctx, taskKey, material)
	if err != nil {
		return nil, err
	}
	header, err := BuildAssertion(material, taskID, m.now())
	if err != nil {
		return nil, errors.New("failed to build agent assertion")
	}
	return &Authorization{
		Header:    header,
		AccountID: material.Claims.AccountID,
		FedRAMP:   material.Claims.ChatGPTAccountIsFedRAMP,
		TokenHash: material.TokenHash,
		Kind:      CredentialKindAgentIdentity,
	}, nil
}

func (m *Manager) task(ctx context.Context, taskKey string, material *Material) (string, error) {
	m.mu.Lock()
	if taskID := m.tasks[taskKey]; taskID != "" {
		m.mu.Unlock()
		return taskID, nil
	}
	if flight := m.flights[taskKey]; flight != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-flight.done:
			return flight.taskID, flight.err
		}
	}
	flight := &taskFlight{done: make(chan struct{})}
	m.flights[taskKey] = flight
	m.mu.Unlock()

	flight.taskID, flight.err = m.registerTask(ctx, material)
	m.mu.Lock()
	if flight.err == nil && flight.taskID != "" {
		m.tasks[taskKey] = flight.taskID
	}
	delete(m.flights, taskKey)
	close(flight.done)
	m.mu.Unlock()
	return flight.taskID, flight.err
}

func (m *Manager) registerTask(ctx context.Context, material *Material) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= registerAttempts; attempt++ {
		taskID, err := RegisterTask(ctx, m.client, m.authAPIBase, material, m.now())
		if err == nil && strings.TrimSpace(taskID) != "" {
			return taskID, nil
		}
		lastErr = err
		if !Retryable(err) || attempt == registerAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(attempt*attempt) * 100 * time.Millisecond):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("agent task registration failed")
	}
	return "", lastErr
}

// InvalidateTask clears one cached task after an upstream authorization failure.
func (m *Manager) InvalidateTask(identityID, tokenHash, sessionID string) {
	if m == nil {
		return
	}
	key := m.taskKey(identityID, tokenHash, sessionID)
	m.mu.Lock()
	delete(m.tasks, key)
	m.mu.Unlock()
}

func (m *Manager) taskKey(identityID, tokenHash, sessionID string) string {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		identityID = "default"
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "default"
	}
	sessionDigest := sha256.Sum256([]byte(sessionID))
	return identityID + "|" + tokenHash + "|" + hex.EncodeToString(sessionDigest[:8])
}
