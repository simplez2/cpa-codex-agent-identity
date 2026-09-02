package credential

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// PluginProvider is the private provider identifier claimed by this plugin.
	// RuntimeProvider is the first-class CPA provider that executes the parsed auth.
	// Keeping these identifiers separate is essential: claiming "codex" would make
	// CPA route its native Codex OAuth login and refresh through this plugin.
	PluginProvider                      = "codex-agent-identity"
	RuntimeProvider                     = "codex"
	LegacyPluginProvider                = "codex" // legacy sidecar files emitted before provider separation
	AuthMode                            = "agent_identity_sidecar"
	SidecarClientKeyField               = "sidecar_client_key"
	SidecarAuthorizationHeaderAttribute = "header:Authorization"
	defaultRefreshInterval              = 24 * time.Hour
	refreshLead                         = 5 * time.Minute
	managedAuthClassification           = "oauth"
)

// Parsed is the safe routing material extracted from a sidecar-owned CPA auth file.
type Parsed struct {
	ID               string
	FileName         string
	Label            string
	Prefix           string
	ProxyURL         string
	Disabled         bool
	StorageJSON      []byte
	Metadata         map[string]any
	Attributes       map[string]string
	NextRefreshAfter time.Time
}

// Parse recognizes only the tightly scoped auth-file format emitted by the sidecar.
// Ordinary Codex OAuth files deliberately return handled=false so CPA's native
// parser remains responsible for them.
func Parse(provider, fileName string, raw []byte) (*Parsed, bool, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	payload, err := decodeObject(raw)
	if err != nil {
		return nil, false, nil
	}
	payloadType := strings.ToLower(strings.TrimSpace(stringValue(payload["type"])))
	// CPA persists plugin-auth data using the runtime provider name ("codex")
	// rather than the plugin identifier. Accept that narrow, explicitly marked
	// sidecar form so a restart or a host refresh does not turn the cais_ client
	// key into a native ChatGPT OAuth token. Ordinary Codex OAuth files do not
	// carry auth_mode=agent_identity_sidecar and continue to the built-in parser.
	if !isManagedProvider(provider) || !isManagedPayloadType(payloadType) || !strings.EqualFold(stringValue(payload["auth_mode"]), AuthMode) {
		return nil, false, nil
	}

	identityID := strings.TrimSpace(stringValue(payload["agent_identity_id"]))
	if !validIdentityID(identityID) {
		return nil, true, errors.New("agent identity id is invalid")
	}
	upstreamToken := strings.TrimSpace(stringValue(payload["access_token"]))
	if upstreamToken == "" || strings.ContainsAny(upstreamToken, "\r\n") {
		return nil, true, errors.New("upstream access token is invalid")
	}
	sidecarClientKey := strings.TrimSpace(stringValue(payload[SidecarClientKeyField]))
	if sidecarClientKey == "" {
		// Legacy auth files stored the sidecar key in access_token.
		sidecarClientKey = upstreamToken
	}
	if !strings.HasPrefix(sidecarClientKey, "cais_") || len(sidecarClientKey) < len("cais_")+32 {
		return nil, true, errors.New("sidecar client key is invalid")
	}
	baseURL, err := validateBaseURL(stringValue(payload["base_url"]))
	if err != nil {
		return nil, true, err
	}

	fileName = filepath.Base(strings.TrimSpace(fileName))
	if fileName == "." || fileName == "" || !strings.HasSuffix(strings.ToLower(fileName), ".json") {
		return nil, true, errors.New("auth file name is invalid")
	}

	// Older sidecar auth files did not serialize websockets explicitly; they
	// were still treated as WebSocket-capable by the original plugin parser.
	websockets := true
	if rawWebsockets, exists := payload["websockets"]; exists {
		websockets, err = boolValue(rawWebsockets)
		if err != nil {
			return nil, true, errors.New("auth file websockets field is invalid")
		}
	}
	disabled, err := boolValue(payload["disabled"])
	if err != nil {
		return nil, true, errors.New("auth file disabled field is invalid")
	}
	fedramp, err := boolValue(payload["fedramp"])
	if err != nil {
		return nil, true, errors.New("auth file fedramp field is invalid")
	}
	expiresAt, err := timeValue(payload["expires_at"])
	if err != nil {
		return nil, true, errors.New("auth file expires_at field is invalid")
	}

	label := strings.TrimSpace(stringValue(payload["email"]))
	if label == "" {
		label = identityID
	}
	prefix := strings.Trim(strings.TrimSpace(stringValue(payload["prefix"])), "/")
	if strings.Contains(prefix, "/") {
		return nil, true, errors.New("auth model prefix is invalid")
	}
	proxyURL := strings.TrimSpace(stringValue(payload["proxy_url"]))
	planType := strings.ToLower(strings.TrimSpace(stringValue(payload["plan_type"])))
	credentialKind := strings.TrimSpace(stringValue(payload["credential_kind"]))
	accountID := strings.TrimSpace(stringValue(payload["account_id"]))
	chatGPTUserID := strings.TrimSpace(stringValue(payload["chatgpt_user_id"]))

	// Keep the complete provider-owned JSON untouched, while making the runtime
	// classification explicit for CPA versions that inspect metadata first.
	metadata := cloneMap(payload)
	metadata["auth_kind"] = managedAuthClassification
	// Keep CPA's auth classification OAuth/file-backed so the native Codex
	// executor continues to apply Codex Header Defaults and per-auth identity
	// remapping. The executor initially reads metadata.access_token, then CPA's
	// standard custom-header pass replaces only the sidecar-bound Authorization
	// header with the opaque client key. The sidecar replaces it again with the
	// real upstream authorization before forwarding to OpenAI.
	attributes := map[string]string{
		SidecarAuthorizationHeaderAttribute: "Bearer " + sidecarClientKey,
		"base_url":                          baseURL,
		"auth_mode":                         AuthMode,
		"auth_kind":                         managedAuthClassification,
		"runtime_only":                      "true",
		"websockets":                        strconv.FormatBool(websockets),
		"fedramp":                           strconv.FormatBool(fedramp),
	}
	setOptionalAttribute(attributes, "account_id", accountID)
	setOptionalAttribute(attributes, "chatgpt_user_id", chatGPTUserID)
	setOptionalAttribute(attributes, "plan_type", planType)
	setOptionalAttribute(attributes, "credential_kind", credentialKind)

	return &Parsed{
		ID:               fileName,
		FileName:         fileName,
		Label:            label,
		Prefix:           prefix,
		ProxyURL:         proxyURL,
		Disabled:         disabled,
		StorageJSON:      append([]byte(nil), raw...),
		Metadata:         metadata,
		Attributes:       attributes,
		NextRefreshAfter: nextRefreshAfter(expiresAt),
	}, true, nil
}

func isManagedProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case PluginProvider, RuntimeProvider:
		return true
	default:
		return false
	}
}

func isManagedPayloadType(payloadType string) bool {
	switch strings.ToLower(strings.TrimSpace(payloadType)) {
	case PluginProvider, LegacyPluginProvider:
		return true
	default:
		return false
	}
}

func decodeObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("auth file must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("auth file has trailing data")
	}
	return value, nil
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(value)+1)
	for key, item := range value {
		result[key] = item
	}
	return result
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func boolValue(value any) (bool, error) {
	switch typed := value.(type) {
	case nil:
		return false, nil
	case bool:
		return typed, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "", "0", "false", "no", "off":
			return false, nil
		case "1", "true", "yes", "on":
			return true, nil
		default:
			return false, errors.New("invalid boolean")
		}
	case json.Number:
		return boolNumber(typed.String())
	case float64:
		return boolNumber(strconv.FormatFloat(typed, 'f', -1, 64))
	default:
		return false, errors.New("invalid boolean")
	}
}

func boolNumber(value string) (bool, error) {
	switch strings.TrimSpace(value) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errors.New("invalid boolean number")
	}
}

func timeValue(value any) (time.Time, error) {
	switch typed := value.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return typed.UTC(), nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, trimmed)
		if err != nil {
			return time.Time{}, err
		}
		return parsed.UTC(), nil
	case json.Number:
		seconds, err := typed.Int64()
		if err != nil {
			return time.Time{}, err
		}
		return time.Unix(seconds, 0).UTC(), nil
	case float64:
		return time.Unix(int64(typed), 0).UTC(), nil
	default:
		return time.Time{}, errors.New("invalid timestamp")
	}
}

func nextRefreshAfter(expiresAt time.Time) time.Time {
	now := time.Now().UTC()
	next := now.Add(defaultRefreshInterval)
	if expiresAt.IsZero() {
		return next
	}
	candidate := expiresAt.Add(-refreshLead)
	if candidate.After(now) && candidate.Before(next) {
		return candidate
	}
	return next
}

func setOptionalAttribute(attributes map[string]string, key, value string) {
	if strings.TrimSpace(value) != "" {
		attributes[key] = value
	}
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
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery || parsed.Opaque != "" {
		return "", errors.New("sidecar base url is invalid")
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if (scheme != "http" && scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" {
		return "", errors.New("sidecar base url is invalid")
	}
	parsed.Scheme = scheme
	hostname := canonicalSidecarHostname(parsed.Hostname())
	if _, allowed := allowedSidecarHosts()[hostname]; !allowed {
		return "", errors.New("sidecar base url host is not allowed")
	}
	if parsed.EscapedPath() != "/backend-api/codex" {
		return "", errors.New("sidecar base url endpoint is invalid")
	}
	port := parsed.Port()
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", errors.New("sidecar base url port is invalid")
		}
	}
	if scheme == "http" && !allowedSidecarHTTPPort(hostname, port) {
		return "", errors.New("sidecar base url HTTP port is not allowed")
	}
	return parsed.String(), nil
}

func allowedSidecarHosts() map[string]struct{} {
	result := map[string]struct{}{
		"codex-agent-identity-sidecar":        {},
		"codex-agent-identity-sidecar-canary": {},
		// `sidecar` is the conventional Docker Compose service alias used by
		// CPA deployments. Keep it built in so a Plugin Store installation
		// works without requiring an environment-variable override.
		"sidecar":   {},
		"localhost": {},
		"127.0.0.1": {},
		"::1":       {},
	}
	for _, candidate := range strings.Split(os.Getenv("CODEX_AGENT_IDENTITY_SIDECAR_HOSTS"), ",") {
		candidate = canonicalSidecarHostname(candidate)
		if validSidecarHostname(candidate) {
			result[candidate] = struct{}{}
		}
	}
	return result
}

func allowedSidecarHTTPPort(hostname, port string) bool {
	if port == "8787" {
		return true
	}
	if isLoopbackSidecarHost(hostname) && port == "18787" {
		return true
	}
	for _, candidate := range strings.Split(os.Getenv("CODEX_AGENT_IDENTITY_SIDECAR_HTTP_PORTS"), ",") {
		candidate = strings.TrimSpace(candidate)
		portNumber, err := strconv.Atoi(candidate)
		if err == nil && portNumber >= 1 && portNumber <= 65535 && candidate == port {
			return true
		}
	}
	return false
}

func isLoopbackSidecarHost(value string) bool {
	value = canonicalSidecarHostname(value)
	if value == "localhost" {
		return true
	}
	ip := net.ParseIP(value)
	return ip != nil && ip.IsLoopback()
}

func canonicalSidecarHostname(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	if ip := net.ParseIP(value); ip != nil {
		return strings.ToLower(ip.String())
	}
	return value
}

func validSidecarHostname(value string) bool {
	value = canonicalSidecarHostname(value)
	if net.ParseIP(value) != nil {
		return true
	}
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
