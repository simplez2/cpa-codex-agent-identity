package identity

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

const (
	JWTIssuer   = "https://chatgpt.com/codex-backend/agent-identity"
	JWTAudience = "codex-app-server"

	DefaultJWKSURL    = "https://chatgpt.com/backend-api/wham/agent-identities/jwks"
	DefaultAuthAPIURL = "https://auth.openai.com/api/accounts"

	defaultJWKSCacheTTL = 10 * time.Minute
)

// Claims are the verified claims carried by an Agent Identity JWT.
// AgentPrivateKey is secret material and must never be logged.
type Claims struct {
	AgentRuntimeID          string `json:"agent_runtime_id"`
	AgentPrivateKey         string `json:"agent_private_key"`
	AccountID               string `json:"account_id"`
	ChatGPTUserID           string `json:"chatgpt_user_id"`
	Email                   string `json:"email,omitempty"`
	PlanType                string `json:"plan_type,omitempty"`
	ChatGPTAccountIsFedRAMP bool   `json:"chatgpt_account_is_fedramp"`
	Issuer                  string `json:"iss"`
	Audience                any    `json:"aud"`
	ExpiresAt               int64  `json:"exp"`
	IssuedAt                int64  `json:"iat,omitempty"`
}

// Material is the verified runtime representation of an Agent Identity token.
type Material struct {
	Claims     Claims
	PrivateKey ed25519.PrivateKey
	TokenHash  string
}

// JWK is the RSA subset required for RS256 verification.
type JWK struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS is a JSON Web Key Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

func (j *JWKS) find(kid string) *JWK {
	if j == nil {
		return nil
	}
	for i := range j.Keys {
		if j.Keys[i].Kid == kid {
			return &j.Keys[i]
		}
	}
	return nil
}

func (j *JWK) rsaPublicKey() (*rsa.PublicKey, error) {
	if j == nil || !strings.EqualFold(strings.TrimSpace(j.Kty), "RSA") {
		return nil, errors.New("agent identity jwk must be RSA")
	}
	if alg := strings.TrimSpace(j.Alg); alg != "" && !strings.EqualFold(alg, "RS256") {
		return nil, errors.New("agent identity jwk alg must be RS256")
	}
	if use := strings.TrimSpace(j.Use); use != "" && !strings.EqualFold(use, "sig") {
		return nil, errors.New("agent identity jwk use must be sig")
	}
	n, err := decodeBase64URL(j.N)
	if err != nil || len(n) == 0 {
		return nil, errors.New("agent identity jwk modulus is invalid")
	}
	e, err := decodeBase64URL(j.E)
	if err != nil || len(e) == 0 {
		return nil, errors.New("agent identity jwk exponent is invalid")
	}
	var exponent int
	for _, b := range e {
		exponent = exponent<<8 + int(b)
	}
	modulus := new(big.Int).SetBytes(n)
	if exponent <= 0 || modulus.Sign() <= 0 {
		return nil, errors.New("agent identity jwk values are invalid")
	}
	return &rsa.PublicKey{N: modulus, E: exponent}, nil
}

func decodeBase64URL(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(strings.TrimSpace(value))
}

// VerifyJWT verifies the RS256 signature and Agent Identity claims.
func VerifyJWT(token string, jwks *JWKS, now time.Time) (*Claims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, errors.New("invalid agent identity JWT format")
	}
	headerJSON, err := decodeBase64URL(parts[0])
	if err != nil {
		return nil, errors.New("invalid agent identity JWT header")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err = json.Unmarshal(headerJSON, &header); err != nil {
		return nil, errors.New("invalid agent identity JWT header")
	}
	if !strings.EqualFold(header.Alg, "RS256") || strings.TrimSpace(header.Kid) == "" {
		return nil, errors.New("agent identity JWT header is not trusted")
	}
	jwk := jwks.find(header.Kid)
	if jwk == nil {
		return nil, errors.New("agent identity JWT kid is not trusted")
	}
	publicKey, err := jwk.rsaPublicKey()
	if err != nil {
		return nil, err
	}
	signature, err := decodeBase64URL(parts[2])
	if err != nil {
		return nil, errors.New("invalid agent identity JWT signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err = rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		return nil, errors.New("agent identity JWT signature verification failed")
	}
	payload, err := decodeBase64URL(parts[1])
	if err != nil {
		return nil, errors.New("invalid agent identity JWT payload")
	}
	var claims Claims
	if err = json.Unmarshal(payload, &claims); err != nil {
		return nil, errors.New("invalid agent identity JWT claims")
	}
	if strings.TrimSpace(claims.Issuer) != JWTIssuer {
		return nil, errors.New("agent identity JWT issuer mismatch")
	}
	if !audienceContains(claims.Audience, JWTAudience) {
		return nil, errors.New("agent identity JWT audience mismatch")
	}
	if claims.ExpiresAt == 0 || !now.Before(time.Unix(claims.ExpiresAt, 0)) {
		return nil, errors.New("agent identity JWT expired")
	}
	if strings.TrimSpace(claims.AgentRuntimeID) == "" ||
		strings.TrimSpace(claims.AgentPrivateKey) == "" ||
		strings.TrimSpace(claims.AccountID) == "" ||
		strings.TrimSpace(claims.ChatGPTUserID) == "" {
		return nil, errors.New("agent identity JWT claims are incomplete")
	}
	return &claims, nil
}

func audienceContains(audience any, want string) bool {
	switch value := audience.(type) {
	case string:
		return strings.TrimSpace(value) == want
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) == want {
				return true
			}
		}
	}
	return false
}

// ParsePrivateKey decodes a standard-base64 PKCS#8 Ed25519 private key.
func ParsePrivateKey(encoded string) (ed25519.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, errors.New("agent private key is not valid base64")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("agent private key is not valid PKCS#8")
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("agent private key is not Ed25519")
	}
	return privateKey, nil
}

// ParseMaterial verifies a token and parses its private signing key.
func ParseMaterial(token string, jwks *JWKS, now time.Time) (*Material, error) {
	claims, err := VerifyJWT(token, jwks, now)
	if err != nil {
		return nil, err
	}
	privateKey, err := ParsePrivateKey(claims.AgentPrivateKey)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return &Material{
		Claims:     *claims,
		PrivateKey: privateKey,
		TokenHash:  hex.EncodeToString(digest[:]),
	}, nil
}

// BuildAssertion returns an AgentAssertion Authorization header value.
func BuildAssertion(material *Material, taskID string, now time.Time) (string, error) {
	if material == nil || len(material.PrivateKey) != ed25519.PrivateKeySize || strings.TrimSpace(taskID) == "" {
		return "", errors.New("agent assertion material is invalid")
	}
	timestamp := now.UTC().Format("2006-01-02T15:04:05Z")
	payload := material.Claims.AgentRuntimeID + ":" + taskID + ":" + timestamp
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(material.PrivateKey, []byte(payload)))
	raw, err := json.Marshal(struct {
		AgentRuntimeID string `json:"agent_runtime_id"`
		Signature      string `json:"signature"`
		TaskID         string `json:"task_id"`
		Timestamp      string `json:"timestamp"`
	}{
		AgentRuntimeID: material.Claims.AgentRuntimeID,
		Signature:      signature,
		TaskID:         taskID,
		Timestamp:      timestamp,
	})
	if err != nil {
		return "", errors.New("failed to encode agent assertion")
	}
	return "AgentAssertion " + base64.RawURLEncoding.EncodeToString([]byte(raw)), nil
}

type registerTaskResponse struct {
	TaskID               string `json:"task_id"`
	TaskIDCamel          string `json:"taskId"`
	EncryptedTaskID      string `json:"encrypted_task_id"`
	EncryptedTaskIDCamel string `json:"encryptedTaskId"`
}

// RegisterTask registers a new task for a verified Agent Identity.
func RegisterTask(ctx context.Context, client *http.Client, authAPIBaseURL string, material *Material, now time.Time) (string, error) {
	if material == nil {
		return "", errors.New("agent identity material is nil")
	}
	timestamp := now.UTC().Format("2006-01-02T15:04:05Z")
	signingPayload := material.Claims.AgentRuntimeID + ":" + timestamp
	body, err := json.Marshal(map[string]string{
		"timestamp": timestamp,
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(material.PrivateKey, []byte(signingPayload))),
	})
	if err != nil {
		return "", errors.New("failed to encode task registration")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(authAPIBaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultAuthAPIURL
	}
	endpoint := fmt.Sprintf("%s/v1/agent/%s/task/register", baseURL, material.Claims.AgentRuntimeID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("failed to create task registration request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		var networkError net.Error
		return "", &transportError{timeout: errors.As(err, &networkError) && networkError.Timeout()}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return "", errors.New("failed to read task registration response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", &HTTPError{Operation: "agent task registration", StatusCode: response.StatusCode}
	}
	var decoded registerTaskResponse
	if err = json.Unmarshal(responseBody, &decoded); err != nil {
		return "", errors.New("task registration response is invalid")
	}
	if taskID := strings.TrimSpace(decoded.TaskID); taskID != "" {
		return taskID, nil
	}
	if taskID := strings.TrimSpace(decoded.TaskIDCamel); taskID != "" {
		return taskID, nil
	}
	encrypted := strings.TrimSpace(decoded.EncryptedTaskID)
	if encrypted == "" {
		encrypted = strings.TrimSpace(decoded.EncryptedTaskIDCamel)
	}
	if encrypted == "" {
		return "", errors.New("task registration response omitted task id")
	}
	return decryptTaskID(material.PrivateKey, encrypted)
}

func decryptTaskID(privateKey ed25519.PrivateKey, encrypted string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encrypted))
	if err != nil {
		return "", errors.New("encrypted task id is invalid")
	}
	digest := sha512.Sum512(privateKey.Seed())
	var curvePrivateKey [32]byte
	copy(curvePrivateKey[:], digest[:32])
	curvePrivateKey[0] &= 248
	curvePrivateKey[31] &= 127
	curvePrivateKey[31] |= 64
	curvePublicBytes, err := curve25519.X25519(curvePrivateKey[:], curve25519.Basepoint)
	if err != nil {
		return "", errors.New("failed to derive task decryption key")
	}
	var curvePublicKey [32]byte
	copy(curvePublicKey[:], curvePublicBytes)
	plaintext, ok := box.OpenAnonymous(nil, ciphertext, &curvePublicKey, &curvePrivateKey)
	if !ok || !utf8.Valid(plaintext) {
		return "", errors.New("failed to decrypt task id")
	}
	return string(plaintext), nil
}

// HTTPError is a non-secret HTTP failure from an Agent Identity endpoint.
type HTTPError struct {
	Operation  string
	StatusCode int
}

type transportError struct {
	timeout bool
}

func (e *transportError) Error() string   { return "agent identity network request failed" }
func (e *transportError) Timeout() bool   { return e != nil && e.timeout }
func (e *transportError) Temporary() bool { return true }

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s failed: status=%d", e.Operation, e.StatusCode)
}

// Retryable reports whether task registration can be retried safely.
func Retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var httpError *HTTPError
	if errors.As(err, &httpError) {
		return httpError.StatusCode == http.StatusTooManyRequests || httpError.StatusCode >= 500
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

// JWKSCache caches the official Agent Identity signing keys.
type JWKSCache struct {
	mu        sync.Mutex
	url       string
	client    *http.Client
	cached    *JWKS
	expiresAt time.Time
}

// NewJWKSCache creates a shared JWKS cache.
func NewJWKSCache(url string, client *http.Client) *JWKSCache {
	return &JWKSCache{url: strings.TrimSpace(url), client: client}
}

// Get returns a cached or freshly downloaded JWKS document.
func (c *JWKSCache) Get(ctx context.Context, force bool) (*JWKS, error) {
	if c == nil || c.client == nil || c.url == "" {
		return nil, errors.New("JWKS cache is not configured")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.cached != nil && time.Now().Before(c.expiresAt) {
		return c.cached, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, ErrCredentialServiceUnavailable
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, ErrCredentialServiceUnavailable
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, ErrCredentialServiceUnavailable
	}
	var jwks JWKS
	if err = json.Unmarshal(body, &jwks); err != nil || len(jwks.Keys) == 0 {
		return nil, ErrCredentialServiceUnavailable
	}
	ttl := defaultJWKSCacheTTL
	for _, item := range strings.Split(response.Header.Get("Cache-Control"), ",") {
		item = strings.TrimSpace(item)
		if strings.HasPrefix(strings.ToLower(item), "max-age=") {
			var seconds int
			if _, err = fmt.Sscanf(item, "max-age=%d", &seconds); err == nil && seconds > 0 {
				ttl = time.Duration(seconds) * time.Second
			}
		}
	}
	c.cached = &jwks
	c.expiresAt = time.Now().Add(ttl)
	return c.cached, nil
}

// ParseWithCache verifies a token, refreshing JWKS once for a new kid.
func ParseWithCache(ctx context.Context, token string, cache *JWKSCache, now time.Time) (*Material, error) {
	jwks, err := cache.Get(ctx, false)
	if err != nil {
		return nil, err
	}
	material, err := ParseMaterial(token, jwks, now)
	if err != nil && strings.Contains(err.Error(), "kid is not trusted") {
		refreshed, refreshErr := cache.Get(ctx, true)
		if refreshErr != nil {
			return nil, refreshErr
		}
		return ParseMaterial(token, refreshed, now)
	}
	return material, err
}
