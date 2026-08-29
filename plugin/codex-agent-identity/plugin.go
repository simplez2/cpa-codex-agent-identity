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
	pluginID                  = "codex-agent-identity"
	pluginProviderID          = credential.PluginProvider
	runtimeProviderID         = credential.RuntimeProvider
	pluginName                = "Codex Agent Identity"
	pluginAuthor              = "simplez2"
	pluginRepository          = "https://github.com/simplez2/cpa-codex-agent-identity"
	pluginLogo                = "https://raw.githubusercontent.com/simplez2/cpa-codex-agent-identity/main/assets/logo.svg"
	managementOpenPath        = "/codex-agent-identity/open"
	managementOpenFullPath    = "/v0/management" + managementOpenPath
	managementAPICallPath     = "/codex-agent-identity/api-call"
	managementAPICallFullPath = "/v0/management" + managementAPICallPath
	sidecarAPICallPath        = "/v0/management/api-call"
	sidecarRelativeAPICall    = "/api/cpa-api-call"
	resourceOpenFullPath      = "/v0/resource/plugins/" + pluginID + "/open"
	legacyResourceOpenPath    = "/v0/resource/plugins/" + pluginID + managementOpenPath
	configSidecarURL          = "sidecar_url"
	configSidecarAPIURL       = "sidecar_api_url"
	defaultSidecarURL         = "http://127.0.0.1:18787/agent-identity/"
	defaultSidecarOrigin      = "http://127.0.0.1:18787"
	defaultSidecarAPIURL      = defaultSidecarOrigin + sidecarAPICallPath
	defaultSidecarHTTPPort    = 8787
	defaultSidecarEmbedURL    = defaultSidecarURL + "?embed=cpamc"
	maxForwardBodyBytes       = 1 << 20
	minimumSidecarVersion     = "0.3.2"
	readyMessageType          = "cpa-codex-agent-identity:ready"
	themeMessageType          = "cpa-codex-agent-identity:theme"
)

var (
	pluginVersion = "0.3.7"
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
	sidecarURL    string
	sidecarAPIURL string
	embedURL      string
	frameSource   string
	configError   string
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
				// A fresh installation uses the local/reverse-proxy defaults; the legacy
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
	if apiURL == "" {
		// An explicit sidecar_url should still derive its API endpoint from the
		// same origin. Only override that derivation when the container runtime
		// supplied an internal sidecar endpoint.
		apiURL = configuredRuntimeSidecarAPIURL()
	}
	next.sidecarURL = normalized
	next.sidecarAPIURL = apiURL
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
	target, err := sidecarManagementAPIURL(current, request.Headers)
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
		return &http.Client{Transport: transport, Timeout: 60 * time.Second}
	}
	return &http.Client{Transport: &http.Transport{}, Timeout: 60 * time.Second}
}

func sidecarManagementAPIURL(current runtimeState, headers http.Header) (string, error) {
	if configured := strings.TrimSpace(current.sidecarAPIURL); configured != "" {
		return configured, nil
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
		return uiURL.String(), nil
	}
	origin := strings.TrimSpace(headers.Get("Origin"))
	if origin == "" {
		origin = strings.TrimSpace(headers.Get("Referer"))
	}
	base, err := url.Parse(origin)
	if err != nil || !base.IsAbs() || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return "", errors.New("sidecar_api_url is required when sidecar_url is relative and no browser origin is available")
	}
	base.Path = strings.TrimRight(uiURL.Path, "/") + sidecarRelativeAPICall
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
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
	case "host", "content-length", "content-encoding", "accept-encoding", "connection", "cookie", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto":
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
	csp := "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; frame-src " + current.frameSource + "; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
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
    const rootURL=__ROOT_URL__;
    const root=document.documentElement;
    const frame=document.getElementById('identityFrame');
    const retry=document.getElementById('retry');
    const open=document.getElementById('open');
    const media=window.matchMedia('(prefers-color-scheme: dark)');
    const variableNames=['--bg-primary','--bg-secondary','--bg-tertiary','--bg-hover','--bg-quinary','--floating-surface','--floating-shadow','--text-primary','--text-secondary','--text-tertiary','--text-quaternary','--text-muted','--border-color','--border-secondary','--border-primary','--border-hover','--primary-color','--primary-hover','--primary-active','--primary-contrast','--success-color','--quota-medium-color','--warning-color','--error-color','--danger-color','--info-color','--warning-bg','--warning-border','--warning-text','--success-badge-bg','--success-badge-text','--success-badge-border','--failure-badge-bg','--failure-badge-text','--failure-badge-border','--count-badge-bg','--count-badge-text','--shadow','--shadow-lg','--primary-8','--primary-10','--primary-30','--amber-color','--amber-text','--amber-10','--amber-30','--destructive-color','--destructive-10','--destructive-30','--muted-bg','--muted-foreground','--accent-bg','--glass-bg','--glass-bg-secondary','--glass-border'];
    let timer=0;
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
    function syncTheme(){
      applyShellTheme(resolveTheme(),resolveVariables());
      postTheme();
    }
    function themedURL(raw,theme){
      const value=new URL(raw,window.location.href);
      value.searchParams.set('theme',normalizeTheme(theme));
      return value;
    }
    function setFrameSource(){
      const value=themedURL(frame.dataset.src,currentTheme);
      childOrigin=value.origin&&value.origin!=='null'?value.origin:'*';
      frame.src=value.href;
    }
    function connecting(){root.removeAttribute('data-ready');root.removeAttribute('data-failed')}
    function ready(){clearTimeout(timer);root.removeAttribute('data-failed');root.setAttribute('data-ready','true');postTheme()}
    function failed(){root.removeAttribute('data-ready');root.setAttribute('data-failed','true')}
    function start(){clearTimeout(timer);timer=setTimeout(failed,10000)}

    parentRoot=accessibleParentRoot();
    if(parentRoot&&typeof MutationObserver==='function'){
      new MutationObserver(syncTheme).observe(parentRoot,{attributes:true,attributeFilter:['data-theme','style','class']});
    }
    window.addEventListener('message',function(event){
      const data=event.data||{};
      if(frame&&event.source===frame.contentWindow&&data.type===readyType){ready();return}
      if(window.parent!==window&&event.source===window.parent&&data.type===themeType){
        inheritedTheme=data.theme;
        inheritedVariables=data.variables&&typeof data.variables==='object'?data.variables:null;
        syncTheme();
      }
    });
    window.addEventListener('storage',function(event){if(event.key===storageKey&&!inheritedTheme)syncTheme()});
    const mediaChanged=function(){if(!inheritedTheme)syncTheme()};
    if(typeof media.addEventListener==='function')media.addEventListener('change',mediaChanged);else if(typeof media.addListener==='function')media.addListener(mediaChanged);
    frame.addEventListener('load',postTheme);
    retry.addEventListener('click',function(){connecting();applyShellTheme(resolveTheme(),resolveVariables());setFrameSource();start()});
    open.addEventListener('click',function(){const value=themedURL(rootURL,currentTheme);window.open(value.href,'_blank','noopener')});
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
