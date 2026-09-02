package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/simplez2/cpa-codex-agent-identity/plugin/codex-agent-identity/internal/credential"
	"gopkg.in/yaml.v3"
)

const (
	pluginID                      = "codex-agent-identity"
	pluginProviderID              = credential.PluginProvider
	runtimeProviderID             = credential.RuntimeProvider
	pluginName                    = "Codex Agent Identity"
	pluginAuthor                  = "simplez2"
	pluginRepository              = "https://github.com/simplez2/cpa-codex-agent-identity"
	pluginLogo                    = "https://raw.githubusercontent.com/simplez2/cpa-codex-agent-identity/main/assets/logo.svg"
	managementOpenPath            = "/codex-agent-identity/open"
	managementOpenFullPath        = "/v0/management" + managementOpenPath
	managementAPICallPath         = "/codex-agent-identity/api-call"
	managementAPICallFullPath     = "/v0/management" + managementAPICallPath
	sidecarAPICallPath            = "/v0/management/api-call"
	resourceOpenFullPath          = "/v0/resource/plugins/" + pluginID + "/open"
	legacyResourceOpenPath        = "/v0/resource/plugins/" + pluginID + managementOpenPath
	configSidecarURL              = "sidecar_url"
	configSidecarAPIURL           = "sidecar_api_url"
	defaultSidecarURL             = "/agent-identity/"
	legacyLocalSidecarURL         = "http://127.0.0.1:18787/agent-identity/"
	legacyLocalhostSidecarURL     = "http://localhost:18787/agent-identity/"
	legacyIPv6SidecarURL          = "http://[::1]:18787/agent-identity/"
	legacyLocalSidecarAPIURL      = "http://127.0.0.1:18787/v0/management/api-call"
	legacyLocalSidecarOrigin      = "http://127.0.0.1:18787"
	legacyLocalhostOrigin         = "http://localhost:18787"
	legacyIPv6Origin              = "http://[::1]:18787"
	defaultSidecarOrigin          = "'self'"
	defaultSidecarAPIURL          = ""
	defaultSidecarHTTPPort        = 8787
	defaultSidecarEmbedURL        = defaultSidecarURL + "?embed=cpamc"
	maxForwardBodyBytes           = 1 << 20
	minimumSidecarVersion         = "0.3.10"
	readyMessageType              = "cpa-codex-agent-identity:ready"
	themeMessageType              = "cpa-codex-agent-identity:theme"
	managementKeyMessageType      = "cpa-codex-agent-identity:management-key"
	managementBridgeQueryKey      = "cpa_bridge"
	secureStoragePrefix           = "enc::v1::"
	secureStorageSalt             = "cli-proxy-api-webui::secure-storage"
	authStorageKey                = "cli-proxy-auth"
	authScopePrefix               = authStorageKey + ":scope:"
	authSelectionPrefix           = authStorageKey + ":selection:"
	legacyManagementKeyStorageKey = "managementKey"
)

var (
	pluginVersion = "0.3.11"
	stateMu       sync.RWMutex
	state         = runtimeState{
		sidecarURL:    defaultSidecarURL,
		sidecarAPIURL: defaultSidecarAPIURL,
		embedURL:      defaultSidecarEmbedURL,
		frameSource:   defaultSidecarOrigin,
	}
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type pluginConfig struct {
	SidecarURL    string `yaml:"sidecar_url"`
	SidecarAPIURL string `yaml:"sidecar_api_url"`
}

type runtimeState struct {
	sidecarURL          string
	sidecarAPIURL       string
	sidecarAPIURLSource string
	embedURL            string
	frameSource         string
	configError         string
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	AuthProvider  bool `json:"auth_provider"`
	ManagementAPI bool `json:"management_api"`
}

type managementRegistration struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description"`
}

type resourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRequest struct {
	Method  string      `json:"Method"`
	Path    string      `json:"Path"`
	Headers http.Header `json:"Headers,omitempty"`
	Query   url.Values  `json:"Query,omitempty"`
	Body    []byte      `json:"Body,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers,omitempty"`
	Body       []byte      `json:"Body,omitempty"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		applyConfig(request)
		return okEnvelope(registration{
			SchemaVersion: negotiatedSchemaVersion(request),
			Metadata: pluginapi.Metadata{
				Name:             pluginName,
				Version:          pluginVersion,
				Author:           pluginAuthor,
				GitHubRepository: pluginRepository,
				Logo:             pluginLogo,
				// Sidecar endpoints are deliberately not exposed as plugin-store fields.
				// A fresh installation uses the same-origin reverse-proxy default; the legacy
				// sidecar_url and sidecar_api_url YAML keys remain accepted for upgrades.
				ConfigFields: nil,
			},
			Capabilities: registrationCapability{AuthProvider: true, ManagementAPI: true},
		})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{
			Routes: []managementRoute{{
				Method:      http.MethodGet,
				Path:        managementOpenPath,
				Description: "Authenticated Codex Agent Identity management UI. Requires sidecar " + minimumSidecarVersion + " or later.",
			}, {
				Method:      http.MethodPost,
				Path:        managementAPICallPath,
				Description: "CPA-compatible Codex quota API bridge for sidecar-managed auth files.",
			}},
			Resources: []resourceRoute{{
				Path:        "/open",
				Menu:        "Codex Agent Identity",
				Description: "Open the Codex Agent Identity management page.",
			}},
		})
	case pluginabi.MethodManagementHandle:
		return okEnvelope(handleManagementRequest(request))
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: pluginProviderID})
	case pluginabi.MethodAuthParse:
		var req pluginapi.AuthParseRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, errors.New("invalid auth parse request")
		}
		parsed, handled, err := credential.Parse(req.Provider, req.FileName, req.RawJSON)
		if err != nil {
			return nil, err
		}
		if !handled || parsed == nil {
			return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
		}
		return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: authDataFromParsed(parsed)})
	case pluginabi.MethodAuthRefresh:
		var req pluginapi.AuthRefreshRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, errors.New("invalid auth refresh request")
		}
		fileName := req.AuthID
		if rawName, ok := req.Metadata["file_name"].(string); ok && strings.TrimSpace(rawName) != "" {
			fileName = rawName
		}
		refreshed, err := authDataForRefresh(req, fileName)
		if err != nil {
			return nil, err
		}
		return okEnvelope(pluginapi.AuthRefreshResponse{
			Auth:             refreshed,
			NextRefreshAfter: refreshed.NextRefreshAfter,
		})
	case pluginabi.MethodAuthLoginStart:
		return nil, errors.New("Codex Agent Identity login is managed through the plugin management page; native Codex OAuth login is unchanged")
	case pluginabi.MethodAuthLoginPoll:
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "use the Agent Identity management page"})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func negotiatedSchemaVersion(request []byte) uint32 {
	// Returning the host's requested schema keeps the plugin loadable by older
	// CPA releases while retaining the newest contract when the host supports it.
	// This plugin only relies on the v1 auth and management contracts.
	var req lifecycleRequest
	if err := json.Unmarshal(request, &req); err != nil || req.SchemaVersion == 0 {
		return 1
	}
	if req.SchemaVersion > pluginabi.SchemaVersion {
		return pluginabi.SchemaVersion
	}
	return req.SchemaVersion
}

func applyConfig(request []byte) {
	next := runtimeState{}
	if len(bytes.TrimSpace(request)) == 0 {
		applyDefaultRuntimeState(&next)
		setRuntimeState(next)
		return
	}
	var req lifecycleRequest
	if err := json.Unmarshal(request, &req); err != nil {
		next.configError = "invalid plugin lifecycle request"
		setRuntimeState(next)
		return
	}
	var cfg pluginConfig
	if len(bytes.TrimSpace(req.ConfigYAML)) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			next.configError = "invalid plugin YAML config"
			setRuntimeState(next)
			return
		}
	}
	normalized, frameSource, err := normalizeSidecarURL(cfg.SidecarURL)
	if err != nil {
		next.configError = err.Error()
		setRuntimeState(next)
		return
	}
	embedURL, err := embedURLForSidecarURL(normalized)
	if err != nil {
		next.configError = err.Error()
		setRuntimeState(next)
		return
	}
	apiURL, err := normalizeSidecarAPIURL(cfg.SidecarAPIURL)
	if err != nil {
		next.configError = err.Error()
		setRuntimeState(next)
		return
	}
	apiURLSource := ""
	if apiURL != "" {
		apiURLSource = "config"
	} else if runtimeAPIURL := configuredRuntimeSidecarAPIURL(); runtimeAPIURL != "" {
		// Runtime routing is trusted process configuration and is preferred over
		// deriving a backend destination from browser request headers.
		apiURL = runtimeAPIURL
		apiURLSource = "runtime"
	}
	next.sidecarURL = normalized
	next.sidecarAPIURL = apiURL
	next.sidecarAPIURLSource = apiURLSource
	next.embedURL = embedURL
	next.frameSource = frameSource
	setRuntimeState(next)
}

func normalizeSidecarURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultSidecarURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("sidecar_url is invalid: %w", err)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", "", errors.New("sidecar_url must not contain credentials, query parameters, or a fragment")
	}
	if !u.IsAbs() && u.Host != "" {
		return "", "", errors.New("sidecar_url must be absolute or start with /")
	}
	frameSource := "'self'"
	if u.IsAbs() {
		scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
		if scheme != "http" && scheme != "https" {
			return "", "", errors.New("sidecar_url must use http:// or https://")
		}
		if u.Host == "" {
			return "", "", errors.New("sidecar_url host is required")
		}
		u.Scheme = scheme
		frameSource = scheme + "://" + u.Host
	} else if !strings.HasPrefix(u.Path, "/") {
		return "", "", errors.New("sidecar_url must be absolute or start with /")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/agent-identity/"
	} else if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawPath = ""
	return u.String(), frameSource, nil
}

func applyDefaultRuntimeState(next *runtimeState) {
	if next == nil {
		return
	}
	next.sidecarURL = defaultSidecarURL
	next.sidecarAPIURL = defaultRuntimeSidecarAPIURL()
	if next.sidecarAPIURL != "" {
		next.sidecarAPIURLSource = "runtime"
	}
	next.embedURL = defaultSidecarEmbedURL
	next.frameSource = defaultSidecarOrigin
}

func embedURLForSidecarURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("sidecar_url is invalid")
	}
	query := u.Query()
	query.Set("embed", "cpamc")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func setRuntimeState(next runtimeState) {
	stateMu.Lock()
	state = next
	stateMu.Unlock()
}

func currentRuntimeState() runtimeState {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return state
}

func handleManagementRequest(raw []byte) managementResponse {
	var request managementRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return managementErrorResponse(http.StatusBadRequest, "invalid management request")
	}
	method := request.Method
	requestPath := request.Path
	if method == "" || requestPath == "" {
		return managementErrorResponse(http.StatusBadRequest, "management method and path are required")
	}
	if requestPath == managementAPICallFullPath {
		if method != http.MethodPost {
			response := managementErrorResponse(http.StatusMethodNotAllowed, "method not allowed")
			response.Headers.Set("Allow", http.MethodPost)
			return response
		}
		return forwardSidecarAPICall(request)
	}
	if requestPath != managementOpenFullPath && requestPath != resourceOpenFullPath && requestPath != legacyResourceOpenPath {
		return managementErrorResponse(http.StatusNotFound, "management route not found")
	}
	if method != http.MethodGet {
		response := managementErrorResponse(http.StatusMethodNotAllowed, "method not allowed")
		response.Headers.Set("Allow", http.MethodGet)
		return response
	}
	return currentManagementResponse()
}

func forwardSidecarAPICall(request managementRequest) managementResponse {
	if len(request.Body) > maxForwardBodyBytes {
		return managementErrorResponse(http.StatusRequestEntityTooLarge, "management request body is too large")
	}
	current := currentRuntimeState()
	target, err := sidecarManagementAPIURL(current)
	if err != nil {
		return managementErrorResponse(http.StatusServiceUnavailable, err.Error())
	}
	upstreamRequest, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(request.Body))
	if err != nil {
		return managementErrorResponse(http.StatusBadGateway, "failed to build sidecar request")
	}
	for name, values := range request.Headers {
		if blockedForwardHeader(name) {
			continue
		}
		for _, value := range values {
			upstreamRequest.Header.Add(name, value)
		}
	}
	// The plugin host and CPA both serialize the response body themselves.
	// Avoid a compressed upstream response and never forward transport framing
	// headers that describe the sidecar connection rather than this response.
	upstreamRequest.Header.Set("Accept-Encoding", "identity")
	upstreamRequest.Header.Set("Content-Type", "application/json")
	upstreamRequest.Header.Del("Content-Length")
	response, err := sidecarHTTPClient().Do(upstreamRequest)
	if err != nil {
		return managementErrorResponse(http.StatusBadGateway, "sidecar management API unavailable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(maxForwardBodyBytes)+1))
	if err != nil {
		return managementErrorResponse(http.StatusBadGateway, "failed to read sidecar response")
	}
	if len(body) > maxForwardBodyBytes {
		return managementErrorResponse(http.StatusBadGateway, "sidecar response is too large")
	}
	return managementResponse{StatusCode: response.StatusCode, Headers: sanitizeResponseHeaders(response.Header), Body: body}
}

var sidecarClient = newSidecarHTTPClient()

func sidecarHTTPClient() *http.Client {
	return sidecarClient
}

func newSidecarHTTPClient() *http.Client {
	// The sidecar endpoint is an internal control-plane hop. Do not route it
	// through HTTP(S)_PROXY from the CPA container, which can make a Docker
	// service name unreachable or leak management traffic to an external proxy.
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport := base.Clone()
		transport.Proxy = nil
		return &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: rejectSidecarRedirect}
	}
	return &http.Client{Transport: &http.Transport{}, Timeout: 60 * time.Second, CheckRedirect: rejectSidecarRedirect}
}

func rejectSidecarRedirect(_ *http.Request, _ []*http.Request) error {
	return errors.New("sidecar management API redirects are disabled")
}

func sidecarManagementAPIURL(current runtimeState) (string, error) {
	if configured := strings.TrimSpace(current.sidecarAPIURL); configured != "" {
		return validateTrustedSidecarAPIURL(configured, current.sidecarAPIURLSource)
	}
	uiURL, err := url.Parse(strings.TrimSpace(current.sidecarURL))
	if err != nil {
		return "", errors.New("sidecar_url is invalid")
	}
	if uiURL.IsAbs() {
		uiURL.Path = sidecarAPICallPath
		uiURL.RawPath = ""
		uiURL.RawQuery = ""
		uiURL.Fragment = ""
		return validateTrustedSidecarAPIURL(uiURL.String(), "sidecar_url")
	}
	if runtimeAPIURL := configuredRuntimeSidecarAPIURL(); runtimeAPIURL != "" {
		return validateTrustedSidecarAPIURL(runtimeAPIURL, "runtime")
	}
	// A fresh Plugin Store installation has no visible sidecar URL field.
	// Keep the historical host-install contract working without trusting
	// browser Origin/Referer headers: only the fixed loopback listener is used.
	if localSidecarFallbackEnabled(current.sidecarURL) {
		return validateTrustedSidecarAPIURL(legacyLocalSidecarAPIURL, "default-loopback")
	}
	return "", errors.New("sidecar_api_url or CODEX_AGENT_IDENTITY_SIDECAR_HOSTS is required when sidecar_url is relative")
}

func validateTrustedSidecarAPIURL(raw, source string) (string, error) {
	normalized, err := normalizeSidecarAPIURL(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(normalized)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "", errors.New("sidecar API target must be an absolute URL")
	}
	// HTTPS is required for configured remote targets. Plain HTTP remains
	// available only for loopback legacy installs and trusted container routing
	// supplied by the process environment. Browser Origin/Referer values never
	// participate in target selection.
	if u.Scheme == "http" && source != "runtime" && !isLoopbackHost(u.Hostname()) {
		return "", errors.New("sidecar API target must use HTTPS unless it targets loopback or trusted runtime routing")
	}
	return normalized, nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func normalizeSidecarAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("sidecar_api_url must be an absolute http(s) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("sidecar_api_url must not contain credentials, query parameters, or a fragment")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = sidecarAPICallPath
	}
	u.RawPath = ""
	return u.String(), nil
}

func blockedForwardHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "host", "content-length", "content-encoding", "accept-encoding", "connection", "cookie", "keep-alive", "origin", "referer", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto":
		return true
	default:
		return false
	}
}

func sanitizeResponseHeaders(headers http.Header) http.Header {
	sanitized := headers.Clone()
	for name := range sanitized {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "content-length", "content-encoding", "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "set-cookie":
			deleteHeaderKey(sanitized, name)
		}
	}
	return sanitized
}

func deleteHeaderKey(headers http.Header, name string) {
	delete(headers, name)
	for key := range headers {
		if strings.EqualFold(key, name) {
			delete(headers, key)
		}
	}
}

func defaultRuntimeSidecarAPIURL() string {
	if configured := configuredRuntimeSidecarAPIURL(); configured != "" {
		return configured
	}
	return defaultSidecarAPIURL
}

func configuredRuntimeSidecarAPIURL() string {
	if explicit := strings.TrimSpace(os.Getenv("CODEX_AGENT_IDENTITY_SIDECAR_API_URL")); explicit != "" {
		if normalized, err := normalizeSidecarAPIURL(explicit); err == nil {
			return normalized
		}
	}
	hosts := splitRuntimeList(os.Getenv("CODEX_AGENT_IDENTITY_SIDECAR_HOSTS"))
	if len(hosts) == 0 {
		return ""
	}
	ports := splitRuntimeList(os.Getenv("CODEX_AGENT_IDENTITY_SIDECAR_HTTP_PORTS"))
	for index, host := range hosts {
		host = strings.TrimSpace(strings.Trim(host, "[]"))
		if host == "" || strings.ContainsAny(host, "/?#@\\") || strings.IndexFunc(host, unicodeSpace) >= 0 {
			continue
		}
		port := defaultSidecarHTTPPort
		if index < len(ports) {
			candidate, err := strconv.Atoi(ports[index])
			if err != nil || candidate < 1 || candidate > 65535 {
				continue
			}
			port = candidate
		}
		u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(port)), Path: sidecarAPICallPath}
		return u.String()
	}
	return ""
}

func splitRuntimeList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicodeSpace(r) })
}

func unicodeSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == '\n'
}

func managementErrorResponse(status int, message string) managementResponse {
	return managementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":            []string{"text/plain; charset=utf-8"},
			"Content-Security-Policy": []string{"default-src 'none'; frame-ancestors 'none'"},
			"Referrer-Policy":         []string{"no-referrer"},
			"Cache-Control":           []string{"no-store"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"X-Frame-Options":         []string{"DENY"},
		},
		Body: []byte(message),
	}
}

func managementFrameSources(source string) string {
	source = strings.TrimSpace(source)
	if source == "" || source == "'self'" {
		return "'self'"
	}
	if strings.Contains(source, "'self'") {
		return source
	}
	// Keep the local sidecar origin as a compatibility fallback while also
	// allowing the same-origin reverse-proxy path used by remote CPA hosts.
	return "'self' " + source
}

func managementFrameSourcesForState(current runtimeState) string {
	sources := managementFrameSources(current.frameSource)
	if !localSidecarFallbackEnabled(current.sidecarURL) {
		return sources
	}
	for _, candidate := range []string{legacyLocalSidecarOrigin, legacyLocalhostOrigin, legacyIPv6Origin} {
		if !containsCSPSource(sources, candidate) {
			sources += " " + candidate
		}
	}
	return sources
}

func containsCSPSource(sources, candidate string) bool {
	for _, value := range strings.Fields(sources) {
		if value == candidate {
			return true
		}
	}
	return false
}

func localSidecarFallbackEnabled(raw string) bool {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	defaultPath := strings.TrimRight(defaultSidecarURL, "/")
	if raw == "" || raw == defaultPath {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || !isLoopbackHost(u.Hostname()) {
		return false
	}
	for _, candidate := range []string{legacyLocalSidecarURL, legacyLocalhostSidecarURL, legacyIPv6SidecarURL} {
		legacy, parseErr := url.Parse(candidate)
		if parseErr == nil && u.Scheme == legacy.Scheme && u.Host == legacy.Host && u.Path == strings.TrimRight(legacy.Path, "/") {
			return true
		}
	}
	return false
}

func currentManagementResponse() managementResponse {
	current := currentRuntimeState()
	if current.configError != "" {
		return managementResponse{
			StatusCode: http.StatusServiceUnavailable,
			Headers: http.Header{
				"Content-Type":            []string{"text/html; charset=utf-8"},
				"Content-Security-Policy": []string{"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"},
				"Referrer-Policy":         []string{"no-referrer"},
				"X-Content-Type-Options":  []string{"nosniff"},
				"X-Frame-Options":         []string{"SAMEORIGIN"},
				"Cache-Control":           []string{"no-store"},
			},
			Body: []byte(configFallbackHTML(current.configError)),
		}
	}
	csp := "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; frame-src " + managementFrameSourcesForState(current) + "; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
	return managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"Content-Security-Policy": []string{csp},
			"Referrer-Policy":         []string{"no-referrer"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"X-Frame-Options":         []string{"SAMEORIGIN"},
			"Cache-Control":           []string{"no-store"},
		},
		Body: []byte(managementHTML(current.sidecarURL, current.embedURL)),
	}
}

func managementHTML(sidecarURL, embedURL string) string {
	escapedURL := html.EscapeString(embedURL)
	jsURL, _ := json.Marshal(sidecarURL)
	localDefaultURL, _ := json.Marshal(defaultSidecarURL)
	legacyLocalURL, _ := json.Marshal(legacyLocalSidecarURL)
	localLoopbackURLs, _ := json.Marshal([]string{legacyLocalSidecarURL, legacyLocalhostSidecarURL, legacyIPv6SidecarURL})
	sameOriginPath, _ := json.Marshal("/agent-identity/")
	authType, _ := json.Marshal(managementKeyMessageType)
	bridgeQueryKey, _ := json.Marshal(managementBridgeQueryKey)
	securePrefix, _ := json.Marshal(secureStoragePrefix)
	secureSalt, _ := json.Marshal(secureStorageSalt)
	authStorage, _ := json.Marshal(authStorageKey)
	authScope, _ := json.Marshal(authScopePrefix)
	authSelection, _ := json.Marshal(authSelectionPrefix)
	managementOpenURLPath, _ := json.Marshal(managementOpenFullPath)
	legacyManagementKeyStorage, _ := json.Marshal(legacyManagementKeyStorageKey)
	template := `<!doctype html>
<html lang="zh-CN" data-theme="white">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Codex Agent Identity</title>
  <style>
    :root{color-scheme:light;font:14px system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;--bg-primary:#fff;--bg-secondary:#fff;--bg-tertiary:#f6f6f6;--bg-hover:#f6f6f6;--text-primary:#2d2a26;--text-secondary:#6d6760;--text-tertiary:#a29c95;--border-color:#e5e5e5;--border-primary:#d9d9d9;--border-hover:#ccc;--primary-color:#8b8680;--primary-hover:#7f7a74;--primary-active:#726d67;--primary-contrast:#fff;--shadow:0 1px 2px #00000014;--shadow-lg:0 10px 18px -3px #0000001a}
    :root[data-theme="dark"]{color-scheme:dark;--bg-primary:#1d1b18;--bg-secondary:#151412;--bg-tertiary:#262320;--bg-hover:#2e2a26;--text-primary:#f6f4f1;--text-secondary:#c9c3bb;--text-tertiary:#9c958d;--border-color:#3a3530;--border-primary:#4a453f;--border-hover:#5a544d;--primary-color:#8b8680;--primary-hover:#9a948e;--primary-active:#a6a099;--primary-contrast:#fff;--shadow:0 1px 3px #0000004d;--shadow-lg:0 10px 15px -3px #0000004d}
    *{box-sizing:border-box}
    html,body{margin:0;width:100%;height:100%;overflow:hidden;background:var(--bg-primary);color:var(--text-primary);transition:background-color .18s ease,color .18s ease}
    .shell{position:fixed;inset:0;overflow:hidden;background:var(--bg-primary)}
    .identity-frame{display:block;width:100%;height:100%;border:0;background:var(--bg-primary)}
    .status,.fallback{position:absolute;inset:0;display:grid;place-items:center;padding:20px;background:var(--bg-primary)}
    .panel{width:min(620px,100%);padding:28px;border:1px solid var(--border-color);border-radius:12px;background:var(--bg-secondary);box-shadow:var(--shadow-lg);line-height:1.65;text-align:center}
    .fallback{display:none}.fallback h1{margin:0 0 12px;font-size:22px}.panel p{color:var(--text-secondary)}
    .code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--bg-tertiary);color:var(--text-primary);border-radius:6px;padding:2px 6px}
    .actions{display:flex;flex-wrap:wrap;gap:12px;justify-content:center;margin-top:18px}
    .actions button{border:1px solid var(--border-primary);border-radius:8px;background:var(--bg-tertiary);color:var(--text-primary);cursor:pointer;padding:9px 15px;font:inherit;font-weight:700}
    .actions button:hover{border-color:var(--border-hover);background:var(--bg-hover)}
    .actions .primary{border-color:var(--primary-color);background:var(--primary-color);color:var(--primary-contrast)}
    .actions .primary:hover{border-color:var(--primary-hover);background:var(--primary-hover)}
    html[data-ready="true"] .status{display:none}html[data-failed="true"] .status{display:none}html[data-failed="true"] .fallback{display:grid}
    @media(prefers-reduced-motion:reduce){html,body{transition:none}}
  </style>
</head>
<body>
  <main class="shell">
    <iframe id="identityFrame" class="identity-frame" title="Codex Agent Identity" data-src="__EMBED_URL__"></iframe>
    <section class="status"><div class="panel">Connecting to Codex Agent Identity...</div></section>
    <section class="fallback"><div class="panel"><h1>Codex Agent Identity temporarily unavailable</h1><p>Confirm <span class="code">sidecar_url</span> points to an accessible sidecar management page and allows embedding by CPA Management Center.</p><p>Requires sidecar __MIN_VERSION__ or later. Prefer serving it under the same origin as CPAMC, for example <span class="code">/agent-identity/</span>.</p><div class="actions"><button id="retry" class="primary" type="button">Retry</button><button id="open" type="button">Open in new window</button></div></div></section>
  </main>
  <script>
  (function(){
    'use strict';
    const storageKey='cli-proxy-theme';
    const themeType='__THEME_TYPE__';
    const readyType='__READY_TYPE__';
    const authType=__AUTH_TYPE__;
    const bridgeQueryKey=__BRIDGE_QUERY_KEY__;
    const secureStoragePrefix=__SECURE_STORAGE_PREFIX__;
    const secureStorageSalt=__SECURE_STORAGE_SALT__;
    const authStorageKey=__AUTH_STORAGE_KEY__;
    const authScopePrefix=__AUTH_SCOPE_PREFIX__;
    const authSelectionPrefix=__AUTH_SELECTION_PREFIX__;
    const managementOpenURLPath=__MANAGEMENT_OPEN_URL_PATH__;
    const legacyManagementKeyStorageKey=__LEGACY_MANAGEMENT_KEY_STORAGE_KEY__;
    const rootURL=__ROOT_URL__;
    const localDefaultURL=__LOCAL_DEFAULT_URL__;
    const legacyLocalURL=__LEGACY_LOCAL_URL__;
    const sameOriginPath=__SAME_ORIGIN_PATH__;
    const root=document.documentElement;
    const frame=document.getElementById('identityFrame');
    const retry=document.getElementById('retry');
    const open=document.getElementById('open');
    const media=window.matchMedia('(prefers-color-scheme: dark)');
    const variableNames=['--bg-primary','--bg-secondary','--bg-tertiary','--bg-hover','--bg-quinary','--floating-surface','--floating-shadow','--text-primary','--text-secondary','--text-tertiary','--text-quaternary','--text-muted','--border-color','--border-secondary','--border-primary','--border-hover','--primary-color','--primary-hover','--primary-active','--primary-contrast','--success-color','--quota-medium-color','--warning-color','--error-color','--danger-color','--info-color','--warning-bg','--warning-border','--warning-text','--success-badge-bg','--success-badge-text','--success-badge-border','--failure-badge-bg','--failure-badge-text','--failure-badge-border','--count-badge-bg','--count-badge-text','--shadow','--shadow-lg','--primary-8','--primary-10','--primary-30','--amber-color','--amber-text','--amber-10','--amber-30','--destructive-color','--destructive-10','--destructive-30','--muted-bg','--muted-foreground','--accent-bg','--glass-bg','--glass-bg-secondary','--glass-border'];
    let timer=0;
    let candidateIndex=0;
    let childOrigin='*';
    let currentTheme='white';
    let parentRoot=null;
    let inheritedTheme='';
    let inheritedVariables=null;

    function normalizeTheme(value){return String(value||'').toLowerCase()==='dark'?'dark':'white'}
    function themeFromRoot(node){
      if(!node)return '';
      const value=String(node.getAttribute('data-theme')||'').toLowerCase();
      if(value==='dark')return 'dark';
      if(value==='white'||value==='light')return 'white';
      return '';
    }
    function storedTheme(){
      try{
        const raw=localStorage.getItem(storageKey);
        if(!raw)return {theme:'',automatic:false};
        const payload=JSON.parse(raw);
        const state=payload&&payload.state?payload.state:payload;
        if(!state||typeof state!=='object')return {theme:'',automatic:false};
        const automatic=!state.theme||state.theme==='auto'||state.theme==='system';
        if(automatic)return {theme:media.matches?'dark':'white',automatic:true};
        if(state.theme)return {theme:normalizeTheme(state.theme),automatic:false};
        if(state.resolvedTheme)return {theme:normalizeTheme(state.resolvedTheme),automatic:false};
      }catch(_){}
      return {theme:'',automatic:false};
    }
    function accessibleParentRoot(){
      try{
        if(window.parent!==window&&window.parent.document)return window.parent.document.documentElement;
      }catch(_){}
      return null;
    }
    function collectVariables(node){
      const variables={};
      if(!node)return variables;
      try{
        const view=node.ownerDocument&&node.ownerDocument.defaultView?node.ownerDocument.defaultView:window;
        const styles=view.getComputedStyle(node);
        variableNames.forEach(function(name){
          const value=styles.getPropertyValue(name).trim();
          if(value&&value.length<=256)variables[name]=value;
        });
      }catch(_){}
      return variables;
    }
    function applyVariables(variables){
      variableNames.forEach(function(name){root.style.removeProperty(name)});
      if(!variables||typeof variables!=='object')return;
      variableNames.forEach(function(name){
        const value=typeof variables[name]==='string'?variables[name].trim():'';
        if(value&&value.length<=256)root.style.setProperty(name,value);
      });
    }
    function resolveTheme(){
      if(inheritedTheme)return normalizeTheme(inheritedTheme);
      const parentTheme=themeFromRoot(parentRoot);
      if(parentTheme)return parentTheme;
      const stored=storedTheme();
      if(stored.theme)return stored.theme;
      return media.matches?'dark':'white';
    }
    function resolveVariables(){
      if(inheritedVariables&&typeof inheritedVariables==='object')return inheritedVariables;
      if(parentRoot)return collectVariables(parentRoot);
      return null;
    }
    function applyShellTheme(theme,variables){
      currentTheme=normalizeTheme(theme);
      root.dataset.theme=currentTheme;
      root.style.colorScheme=currentTheme==='dark'?'dark':'light';
      applyVariables(variables);
    }
    function messageVariables(){return collectVariables(root)}
    function postTheme(){
      if(!frame||!frame.contentWindow)return;
      frame.contentWindow.postMessage({type:themeType,theme:currentTheme,variables:messageVariables()},childOrigin);
    }
    function createBridgeNonce(){
      try{
        if(window.crypto&&typeof window.crypto.randomUUID==='function')return window.crypto.randomUUID();
        if(window.crypto&&typeof window.crypto.getRandomValues==='function'){
          const bytes=new Uint8Array(16);
          window.crypto.getRandomValues(bytes);
          return Array.from(bytes).map(function(value){return value.toString(16).padStart(2,'0')}).join('');
        }
      }catch(_){}
      return String(Date.now())+'-'+Math.random().toString(36).slice(2);
    }
    const bridgeNonce=createBridgeNonce();
    function localStorageValue(key){
      try{return window.localStorage.getItem(key)||''}catch(_){return ''}
    }
    function decodeUTF8(bytes){
      try{return new TextDecoder().decode(bytes)}catch(_){}
      let binary='';
      for(let index=0;index<bytes.length;index++)binary+=String.fromCharCode(bytes[index]);
      try{return decodeURIComponent(escape(binary))}catch(_){return binary}
    }
    function decodeStoredValue(raw){
      if(!raw||raw.indexOf(secureStoragePrefix)!==0)return raw;
      try{
        const binary=atob(raw.slice(secureStoragePrefix.length));
        const keyBytes=new TextEncoder().encode(secureStorageSalt+'|'+window.location.host+'|'+navigator.userAgent);
        const bytes=new Uint8Array(binary.length);
        for(let index=0;index<binary.length;index++)bytes[index]=binary.charCodeAt(index)^keyBytes[index%keyBytes.length];
        return decodeUTF8(bytes);
      }catch(_){return ''}
    }
    function normalizedManagementKey(value){
      if(typeof value!=='string')return '';
      const candidate=value.trim();
      return candidate&&candidate.length<=4096&&!/[\r\n]/.test(candidate)?candidate:'';
    }
    function parsedStoredValue(raw){
      const decoded=decodeStoredValue(raw);
      if(!decoded)return null;
      try{return JSON.parse(decoded)}catch(_){return decoded}
    }
    function authStateFromStoredValue(raw){
      const parsed=parsedStoredValue(raw);
      if(!parsed||typeof parsed!=='object')return null;
      return parsed.state&&typeof parsed.state==='object'?parsed.state:parsed;
    }
    function managementKeyFromStoredValue(raw){
      const parsed=parsedStoredValue(raw);
      if(typeof parsed==='string')return normalizedManagementKey(parsed);
      const state=parsed&&typeof parsed==='object'&&parsed.state&&typeof parsed.state==='object'?parsed.state:parsed;
      return normalizedManagementKey(state&&state.managementKey);
    }
    function normalizedAPIBase(raw){
      let value=String(raw||'').trim();
      if(!value)return '';
      value=value.replace(/\/?v0\/management\/?$/i,'').replace(/\/+$/,'');
      if(!/^https?:\/\//i.test(value))value='http://'+value;
      try{
        const parsed=new URL(value);
        parsed.hash='';
        parsed.search='';
        return parsed.href.replace(/\/+$/,'');
      }catch(_){return ''}
    }
    function apiBasePathFromPage(pathname){
      const value=String(pathname||'/');
      const path=value.startsWith('/')?value:'/'+value;
      const hadTrailingSlash=/\/$/.test(path);
      const trimmed=path.replace(/\/+$/,'');
      if(!trimmed)return '';
      if(hadTrailingSlash)return trimmed;
      const separator=trimmed.lastIndexOf('/');
      const filename=trimmed.slice(separator+1);
      const directory=trimmed.slice(0,separator);
      const managementPage=/^(?:management|index)\.html?$/i.test(filename);
      const staticAsset=/(?:^|\/)(?:assets|static)$/.test(directory)&&/\.(?:css|[cm]?js|json|map|svg|png|jpe?g|gif|webp|avif|ico|wasm|txt|xml|woff2?|ttf|eot)$/i.test(filename);
      return managementPage||staticAsset?directory.replace(/\/+$/,''):trimmed;
    }
    function apiScopeFromPageURL(raw){
      try{
        const value=new URL(raw,window.location.href);
        if(value.origin!==window.location.origin)return '';
        return normalizedAPIBase(value.origin+apiBasePathFromPage(value.pathname));
      }catch(_){return ''}
    }
    function apiScopeFromWrapperURL(raw){
      try{
        const value=new URL(raw,window.location.href);
        const lowerPath=value.pathname.toLowerCase();
        const resourceMarker='/v0/resource/plugins/';
        const resourceIndex=lowerPath.lastIndexOf(resourceMarker);
        if(resourceIndex>=0){
          value.pathname=value.pathname.slice(0,resourceIndex)||'/';
        }else if(lowerPath.endsWith(managementOpenURLPath.toLowerCase())){
          value.pathname=value.pathname.slice(0,value.pathname.length-managementOpenURLPath.length)||'/';
        }
        value.hash='';
        value.search='';
        return normalizedAPIBase(value.href);
      }catch(_){return ''}
    }
    function storageSelectionScopes(){
      const scopes=[];
      try{
        const pagePaths=[];
        pagePaths.push(new URL(window.location.href).pathname);
        if(window.parent!==window)pagePaths.push(new URL(window.parent.location.href).pathname);
        for(let index=0;index<window.localStorage.length;index++){
          const key=window.localStorage.key(index)||'';
          if(key.indexOf(authSelectionPrefix)!==0)continue;
          let scope='';
          try{scope=normalizedAPIBase(decodeURIComponent(key.slice(authSelectionPrefix.length)))}catch(_){continue}
          if(!scope)continue;
          const scopeURL=new URL(scope);
          if(scopeURL.origin!==window.location.origin)continue;
          const basePath=scopeURL.pathname.replace(/\/+$/,'');
          const related=pagePaths.some(function(path){return !basePath||basePath==='/'||path===basePath||path.indexOf(basePath+'/')===0});
          if(related&&!scopes.includes(scope))scopes.push(scope);
        }
      }catch(_){}
      return scopes.sort(function(left,right){
        try{return new URL(right).pathname.length-new URL(left).pathname.length}catch(_){return 0}
      });
    }
    function candidateCPAScopes(){
      const scopes=[];
      function add(scope){if(scope&&!scopes.includes(scope))scopes.push(scope)}
      try{if(window.parent!==window)add(apiScopeFromPageURL(window.parent.location.href))}catch(_){}
      add(apiScopeFromWrapperURL(window.location.href));
      storageSelectionScopes().forEach(add);
      return scopes;
    }
    function scopedManagementKey(scope){
      const normalizedScope=normalizedAPIBase(scope);
      if(!normalizedScope)return '';
      const selectionKey=authSelectionPrefix+encodeURIComponent(normalizedScope);
      const selectedValue=parsedStoredValue(localStorageValue(selectionKey));
      const selectedAPIBase=typeof selectedValue==='string'?normalizedAPIBase(selectedValue):'';
      if(!selectedAPIBase)return '';
      const scopedKey=authScopePrefix+encodeURIComponent(normalizedScope)+':'+encodeURIComponent(selectedAPIBase);
      const raw=localStorageValue(scopedKey);
      const state=authStateFromStoredValue(raw);
      if(!state)return '';
      const storedAPIBase=normalizedAPIBase(state.apiBase);
      if(storedAPIBase&&storedAPIBase!==selectedAPIBase)return '';
      return normalizedManagementKey(state.managementKey);
    }
    function readStoredManagementKey(){
      const scopes=candidateCPAScopes();
      for(let index=0;index<scopes.length;index++){
        const key=scopedManagementKey(scopes[index]);
        if(key)return key;
      }
      return managementKeyFromStoredValue(localStorageValue(authStorageKey)) ||
        managementKeyFromStoredValue(localStorageValue(legacyManagementKeyStorageKey));
    }
    function postManagementKey(){
      if(!frame||!frame.contentWindow||!bridgeNonce||childOrigin==='*')return;
      const key=readStoredManagementKey();
      if(!key||key.length>4096||/[\r\n]/.test(key))return;
      frame.contentWindow.postMessage({type:authType,nonce:bridgeNonce,managementKey:key},childOrigin);
    }
    function syncTheme(){
      applyShellTheme(resolveTheme(),resolveVariables());
      postTheme();
    }
    function themedURL(raw,theme){
      const value=new URL(raw,window.location.href);
      value.searchParams.set('theme',normalizeTheme(theme));
      return value;
    }
    const candidateURLs=[];
    const localLoopbackURLs=__LOCAL_LOOPBACK_URLS__;
    function addCandidate(raw){
      try{
        const value=new URL(raw,window.location.href);
        if(!candidateURLs.includes(value.href))candidateURLs.push(value.href);
      }catch(_){}
    }
    function isLoopbackHost(raw){
      const host=String(raw||'').toLowerCase().replace(/^\[|\]$/g,'');
      return host==='localhost'||host==='127.0.0.1'||host==='::1';
    }
    function isLocalCPAPage(){
      try{return isLoopbackHost(new URL(window.location.href).hostname)}catch(_){return false}
    }
    function addEmbeddedCandidate(raw){
      try{
        const value=new URL(raw,window.location.href);
        value.searchParams.set('embed','cpamc');
        addCandidate(value.href);
      }catch(_){}
    }
    try{
      const configured=new URL(frame.dataset.src,window.location.href);
      const localDefault=new URL(localDefaultURL,window.location.href);
      const legacyLocal=new URL(legacyLocalURL,window.location.href);
      const isSameOriginDefault=configured.origin===localDefault.origin&&configured.pathname===localDefault.pathname;
      const isLegacyLocal=
        (configured.origin===legacyLocal.origin&&configured.pathname===legacyLocal.pathname) ||
        localLoopbackURLs.some(function(raw){
          try{
            const value=new URL(raw,window.location.href);
            return configured.origin===value.origin&&configured.pathname===value.pathname;
          }catch(_){return false}
        });
      const canUseLocalFallback=isSameOriginDefault||isLegacyLocal;
      if(canUseLocalFallback){
        const sameOrigin=new URL(sameOriginPath,window.location.href);
        sameOrigin.searchParams.set('embed','cpamc');
        addCandidate(sameOrigin.href);
      }
      // CPA Plugin Store artifacts do not start the sidecar. On a local CPA
      // page, retain the native same-origin route first, then try the legacy
      // loopback sidecar before the explicitly configured URL. Never make a
      // remote CPA page reach into a user's browser localhost, and never add
      // loopback fallbacks for an explicitly configured remote sidecar.
      if(canUseLocalFallback&&isLocalCPAPage())localLoopbackURLs.forEach(addEmbeddedCandidate);
    }catch(_){}
    addCandidate(frame.dataset.src);
    function currentCandidate(){return candidateURLs[candidateIndex]||frame.dataset.src}
    function embeddedURL(raw,theme){
      const value=themedURL(raw,theme);
      value.searchParams.set(bridgeQueryKey,bridgeNonce);
      return value;
    }
    function setFrameSource(){
      const value=embeddedURL(currentCandidate(),currentTheme);
      childOrigin=value.origin&&value.origin!=='null'?value.origin:'*';
      frame.src=value.href;
    }
    function connecting(){root.removeAttribute('data-ready');root.removeAttribute('data-failed')}
    function ready(){clearTimeout(timer);root.removeAttribute('data-failed');root.setAttribute('data-ready','true');postTheme();postManagementKey()}
    function failed(){root.removeAttribute('data-ready');root.setAttribute('data-failed','true')}
    function tryNextCandidate(){
      if(candidateIndex+1<candidateURLs.length){
        candidateIndex+=1;
        connecting();
        setFrameSource();
        start();
        return;
      }
      failed();
    }
    function start(){clearTimeout(timer);timer=setTimeout(tryNextCandidate,5000)}

    parentRoot=accessibleParentRoot();
    if(parentRoot&&typeof MutationObserver==='function'){
      new MutationObserver(syncTheme).observe(parentRoot,{attributes:true,attributeFilter:['data-theme','style','class']});
    }
    window.addEventListener('message',function(event){
      const data=event.data||{};
      if(frame&&event.source===frame.contentWindow&&data.type===readyType){
        if(childOrigin!=='*'&&event.origin!==childOrigin)return;
        if(data.nonce!==bridgeNonce)return;
        ready();
        return;
      }
      if(window.parent!==window&&event.source===window.parent&&data.type===themeType){
        inheritedTheme=data.theme;
        inheritedVariables=data.variables&&typeof data.variables==='object'?data.variables:null;
        syncTheme();
      }
    });
    window.addEventListener('storage',function(event){
      if(event.key===storageKey&&!inheritedTheme)syncTheme();
      if(event.key===authStorageKey||event.key===legacyManagementKeyStorageKey||String(event.key||'').indexOf(authScopePrefix)===0||String(event.key||'').indexOf(authSelectionPrefix)===0)postManagementKey();
    });
    const mediaChanged=function(){if(!inheritedTheme)syncTheme()};
    if(typeof media.addEventListener==='function')media.addEventListener('change',mediaChanged);else if(typeof media.addListener==='function')media.addListener(mediaChanged);
    frame.addEventListener('load',function(){postTheme();postManagementKey()});
    retry.addEventListener('click',function(){candidateIndex=0;connecting();applyShellTheme(resolveTheme(),resolveVariables());setFrameSource();start()});
    open.addEventListener('click',function(){const value=themedURL(currentCandidate(),currentTheme);window.open(value.href,'_blank','noopener')});
    applyShellTheme(resolveTheme(),resolveVariables());
    setFrameSource();
    start();
  })();
  </script>
</body>
</html>`
	return strings.NewReplacer(
		"__EMBED_URL__", escapedURL,
		"__MIN_VERSION__", minimumSidecarVersion,
		"__ROOT_URL__", string(jsURL),
		"__LOCAL_DEFAULT_URL__", string(localDefaultURL),
		"__LEGACY_LOCAL_URL__", string(legacyLocalURL),
		"__LOCAL_LOOPBACK_URLS__", string(localLoopbackURLs),
		"__SAME_ORIGIN_PATH__", string(sameOriginPath),
		"__AUTH_TYPE__", string(authType),
		"__BRIDGE_QUERY_KEY__", string(bridgeQueryKey),
		"__SECURE_STORAGE_PREFIX__", string(securePrefix),
		"__SECURE_STORAGE_SALT__", string(secureSalt),
		"__AUTH_STORAGE_KEY__", string(authStorage),
		"__AUTH_SCOPE_PREFIX__", string(authScope),
		"__AUTH_SELECTION_PREFIX__", string(authSelection),
		"__MANAGEMENT_OPEN_URL_PATH__", string(managementOpenURLPath),
		"__LEGACY_MANAGEMENT_KEY_STORAGE_KEY__", string(legacyManagementKeyStorage),
		"__READY_TYPE__", readyMessageType,
		"__THEME_TYPE__", themeMessageType,
	).Replace(template)
}

func configFallbackHTML(message string) string {
	if strings.TrimSpace(message) == "" {
		message = "sidecar_url is not configured"
	}
	escapedMessage := html.EscapeString(message)
	return `<!doctype html>
<html lang="zh-CN" data-theme="white">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Codex Agent Identity</title>
  <style>
    :root{color-scheme:light;font:14px system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;--bg:#fff;--panel:#fff;--muted:#6d6760;--text:#2d2a26;--border:#e5e5e5;--code:#f6f6f6;--accent:#2563eb;--error-bg:#fff7ed;--error:#9a3412}
    :root[data-theme="dark"]{color-scheme:dark;--bg:#1d1b18;--panel:#151412;--muted:#c9c3bb;--text:#f6f4f1;--border:#3a3530;--code:#262320;--accent:#93c5fd;--error-bg:#3b2417;--error:#fdba74}
    *{box-sizing:border-box}html,body{margin:0;min-height:100%;background:var(--bg);color:var(--text)}body{min-height:100vh;display:grid;place-items:center;padding:20px}.panel{width:min(680px,100%);padding:28px;border:1px solid var(--border);border-radius:14px;background:var(--panel);line-height:1.65;box-shadow:0 10px 30px rgba(0,0,0,.08)}h1{margin:0 0 8px;font-size:22px}p,li{color:var(--muted)}.error{padding:10px 12px;border:1px solid color-mix(in srgb,var(--error) 28%,var(--border));border-radius:8px;background:var(--error-bg);color:var(--error)}ol{padding-left:24px}code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--code);color:var(--text);border-radius:6px;padding:2px 6px;overflow-wrap:anywhere}.note{margin-bottom:0;font-size:13px}
  </style>
</head>
<body>
  <main class="panel">
    <h1>Codex Agent Identity</h1>
    <p>The plugin is installed but cannot connect to the sidecar.</p>
    <p class="error">` + escapedMessage + `</p>
    <ol>
      <li>Start the Codex Agent Identity sidecar separately and configure it to use the same management key as CPA.</li>
      <li>In CPA plugin settings, enter a browser-reachable <code>sidecar_url</code>. A same-origin reverse proxy path such as <code>/agent-identity/</code> is recommended; direct local access can use <code>http://127.0.0.1:18787/agent-identity/</code>.</li>
      <li>Save the configuration and reopen this management page. Batch import and auth-file synchronization start here.</li>
    </ol>
    <p class="note">The CPA Plugin Store installs only the .so; it does not create the sidecar container, Docker network, keys, or data directory. CPA native Codex OAuth login remains managed by CPA and is not intercepted by this plugin.</p>
  </main>
  <script>
    (function(){'use strict';
      const key='cli-proxy-theme',root=document.documentElement,media=window.matchMedia('(prefers-color-scheme: dark)');
      function normalize(value){return String(value||'').toLowerCase()==='dark'?'dark':'white'}
      function parentTheme(){try{if(window.parent!==window&&window.parent.document)return window.parent.document.documentElement.getAttribute('data-theme')}catch(_){}return ''}
      function storedTheme(){try{const raw=localStorage.getItem(key);if(!raw)return '';const value=JSON.parse(raw);const state=value&&value.state?value.state:value;if(!state||typeof state!=='object')return '';return state.theme||state.resolvedTheme||''}catch(_){}return ''}
      function apply(){root.dataset.theme=normalize(parentTheme()||storedTheme()||(media.matches?'dark':'white'))}
      const parent=window.parent===window?null:(function(){try{return window.parent.document.documentElement}catch(_){return null}})();
      if(parent&&typeof MutationObserver==='function')new MutationObserver(apply).observe(parent,{attributes:true,attributeFilter:['data-theme']});
      window.addEventListener('storage',function(event){if(event.key===key)apply()});
      if(typeof media.addEventListener==='function')media.addEventListener('change',apply);else if(typeof media.addListener==='function')media.addListener(apply);apply();
    })();
  </script>
</body>
</html>`
}
func authDataFromParsed(parsed *credential.Parsed) pluginapi.AuthData {
	if parsed == nil {
		return pluginapi.AuthData{}
	}
	return pluginapi.AuthData{
		Provider:         runtimeProviderID,
		ID:               parsed.ID,
		FileName:         parsed.FileName,
		Label:            parsed.Label,
		Prefix:           parsed.Prefix,
		ProxyURL:         parsed.ProxyURL,
		Disabled:         parsed.Disabled,
		StorageJSON:      append([]byte(nil), parsed.StorageJSON...),
		Metadata:         cloneAnyMap(parsed.Metadata),
		Attributes:       cloneStringMap(parsed.Attributes),
		NextRefreshAfter: parsed.NextRefreshAfter,
	}
}

func authDataForRefresh(req pluginapi.AuthRefreshRequest, fileName string) (pluginapi.AuthData, error) {
	provider := strings.TrimSpace(req.AuthProvider)
	if provider == "" {
		provider = runtimeProviderID
	}
	if len(bytes.TrimSpace(req.StorageJSON)) > 0 {
		parsed, handled, err := credential.Parse(provider, fileName, req.StorageJSON)
		if err != nil {
			return pluginapi.AuthData{}, err
		}
		if handled && parsed != nil {
			return authDataFromParsed(parsed), nil
		}
	}
	metadata := cloneAnyMap(req.Metadata)
	attributes := cloneStringMap(req.Attributes)
	refreshed := pluginapi.AuthData{
		Provider:    provider,
		ID:          strings.TrimSpace(req.AuthID),
		FileName:    strings.TrimSpace(fileName),
		Label:       labelFromMetadata(metadata, req.AuthID),
		ProxyURL:    firstNonEmptyString(metadata["proxy_url"], attributes["proxy_url"]),
		Disabled:    boolFromValue(metadata["disabled"]) || strings.EqualFold(strings.TrimSpace(attributes["disabled"]), "true"),
		StorageJSON: append([]byte(nil), req.StorageJSON...),
		Metadata:    metadata,
		Attributes:  attributes,
		// A non-sidecar Codex file belongs to CPA's native OAuth refresher. A
		// zero value tells the host to retain its existing refresh schedule.
		NextRefreshAfter: time.Time{},
	}
	return refreshed, nil
}

func labelFromMetadata(metadata map[string]any, fallback string) string {
	if value := firstNonEmptyString(metadata["email"]); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func firstNonEmptyString(values ...any) string {
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func boolFromValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true") || strings.TrimSpace(typed) == "1"
	case float64:
		return typed == 1
	case json.Number:
		return typed.String() == "1"
	default:
		return false
	}
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}
