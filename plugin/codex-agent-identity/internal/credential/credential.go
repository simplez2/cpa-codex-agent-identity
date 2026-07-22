package credential

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const AuthMode = "agent_identity_sidecar"

// Parsed is the safe routing material extracted from a sidecar-owned CPA auth file.
type Parsed struct {
	ID          string
	FileName    string
	Label       string
	Prefix      string
	StorageJSON []byte
	Metadata    map[string]any
	Attributes  map[string]string
}

type filePayload struct {
	Type          string `json:"type"`
	AuthMode      string `json:"auth_mode"`
	Email         string `json:"email"`
	AccessToken   string `json:"access_token"`
	BaseURL       string `json:"base_url"`
	IdentityID    string `json:"agent_identity_id"`
	Websockets    bool   `json:"websockets"`
	Disabled      bool   `json:"disabled"`
	AccountID     string `json:"account_id"`
	ChatGPTUserID string `json:"chatgpt_user_id"`
	ExpiresAt     string `json:"expires_at"`
	FedRAMP       bool   `json:"fedramp"`
	Prefix        string `json:"prefix"`
}

// Parse recognizes only the tightly scoped auth-file format emitted by the sidecar.
func Parse(provider, fileName string, raw []byte) (*Parsed, bool, error) {
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return nil, false, nil
	}
	var payload filePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(payload.Type), "codex") ||
		!strings.EqualFold(strings.TrimSpace(payload.AuthMode), AuthMode) {
		return nil, false, nil
	}
	if !validIdentityID(payload.IdentityID) {
		return nil, true, errors.New("agent identity id is invalid")
	}
	accessToken := strings.TrimSpace(payload.AccessToken)
	if !strings.HasPrefix(accessToken, "cais_") || len(accessToken) < len("cais_")+32 {
		return nil, true, errors.New("sidecar client key is invalid")
	}
	baseURL, err := validateBaseURL(payload.BaseURL)
	if err != nil {
		return nil, true, err
	}
	fileName = filepath.Base(strings.TrimSpace(fileName))
	if fileName == "." || !strings.HasSuffix(strings.ToLower(fileName), ".json") {
		return nil, true, errors.New("auth file name is invalid")
	}
	var metadata map[string]any
	if err = json.Unmarshal(raw, &metadata); err != nil {
		return nil, true, errors.New("auth file metadata is invalid")
	}
	label := strings.TrimSpace(payload.Email)
	if label == "" {
		label = payload.IdentityID
	}
	prefix := strings.Trim(strings.TrimSpace(payload.Prefix), "/")
	if strings.Contains(prefix, "/") {
		return nil, true, errors.New("auth model prefix is invalid")
	}
	return &Parsed{
		ID:          fileName,
		FileName:    fileName,
		Label:       label,
		Prefix:      prefix,
		StorageJSON: append([]byte(nil), raw...),
		Metadata:    metadata,
		Attributes: map[string]string{
			"api_key":    accessToken,
			"base_url":   baseURL,
			"auth_mode":  AuthMode,
			"websockets": "true",
		},
	}, true, nil
}

func validIdentityID(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "agent-") || len(value) <= len("agent-") {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "agent-") {
		if !((character >= 'a' && character <= 'f') || (character >= '0' && character <= '9')) {
			return false
		}
	}
	return true
}

func validateBaseURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("sidecar base url is invalid")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if _, allowed := allowedSidecarHosts()[hostname]; !allowed {
		return "", errors.New("sidecar base url host is not allowed")
	}
	if parsed.Port() != "8787" || parsed.EscapedPath() != "/backend-api/codex" {
		return "", errors.New("sidecar base url endpoint is invalid")
	}
	return parsed.String(), nil
}

func allowedSidecarHosts() map[string]struct{} {
	result := map[string]struct{}{
		"codex-agent-identity-sidecar":        {},
		"codex-agent-identity-sidecar-canary": {},
	}
	for _, candidate := range strings.Split(os.Getenv("CODEX_AGENT_IDENTITY_SIDECAR_HOSTS"), ",") {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if validSidecarHostname(candidate) {
			result[candidate] = struct{}{}
		}
	}
	return result
}

func validSidecarHostname(value string) bool {
	if value == "" || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '.') {
			return false
		}
	}
	return true
}
