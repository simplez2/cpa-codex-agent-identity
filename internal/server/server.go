package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/simplez2/cpa-codex-agent-identity/internal/cpa"
	"github.com/simplez2/cpa-codex-agent-identity/internal/identity"
	"github.com/simplez2/cpa-codex-agent-identity/internal/quota"
	identitystore "github.com/simplez2/cpa-codex-agent-identity/internal/store"
)

//go:embed ui/*
var uiFiles embed.FS

const (
	proxyPathPrefix      = "/backend-api/codex"
	maxImportBodyBytes   = 1 << 20
	maxAPICallBodyBytes  = 1 << 20
	defaultReplayBodyMax = 16 << 20
)

type requestIdentityState struct {
	IdentityID string
	Token      string
	TokenHash  string
	SessionID  string
	Kind       identity.CredentialKind
}

type requestIdentityStateKey struct{}

// Config configures the HTTP sidecar boundary.
type Config struct {
	UpstreamOrigin      *url.URL
	PublicCPABaseURL    string
	ManagementKey       string
	MaxReplayBodyBytes  int64
	OutboundTransport   http.RoundTripper
	Logger              *log.Logger
	CPAChannels         *cpa.Manager
	EmbedAllowedOrigins []string
}

// Server exposes a management import API and an Agent Identity reverse proxy.
type Server struct {
	config     Config
	store      *identitystore.Store
	manager    *identity.Manager
	channels   *cpa.Manager
	proxy      *httputil.ReverseProxy
	upstream   *http.Client
	handler    http.Handler
	mutationMu sync.Mutex
}

// New creates the standalone sidecar HTTP handler.
func New(config Config, store *identitystore.Store, manager *identity.Manager) (*Server, error) {
	if store == nil || manager == nil {
		return nil, errors.New("identity store and manager are required")
	}
	if config.UpstreamOrigin == nil || config.UpstreamOrigin.Scheme == "" || config.UpstreamOrigin.Host == "" {
		return nil, errors.New("upstream origin is required")
	}
	if config.UpstreamOrigin.Scheme != "https" && config.UpstreamOrigin.Scheme != "http" {
		return nil, errors.New("upstream origin must use HTTP or HTTPS")
	}
	if strings.TrimSpace(config.ManagementKey) == "" {
		return nil, errors.New("management key is required")
	}
	if config.MaxReplayBodyBytes <= 0 {
		config.MaxReplayBodyBytes = defaultReplayBodyMax
	}
	if config.OutboundTransport == nil {
		config.OutboundTransport = http.DefaultTransport
	}
	if config.Logger == nil {
		config.Logger = log.New(os.Stderr, "codex-agent-identity: ", log.LstdFlags|log.LUTC)
	}
	config.EmbedAllowedOrigins = normalizeEmbedOrigins(config.EmbedAllowedOrigins)

	result := &Server{config: config, store: store, manager: manager, channels: config.CPAChannels}
	retryTransport := &authorizationRetryTransport{base: config.OutboundTransport, manager: manager}
	result.upstream = &http.Client{Transport: retryTransport, Timeout: 60 * time.Second}
	result.proxy = &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(config.UpstreamOrigin)
			request.Out.Host = config.UpstreamOrigin.Host
			request.Out.Header.Del("X-Agent-Identity-Sidecar-Key")
			request.Out.Header.Del("X-Forwarded-For")
			request.Out.Header.Del("X-Forwarded-Host")
			request.Out.Header.Del("X-Forwarded-Proto")
			request.Out.Header.Del("X-Agent-Identity-ID")
		},
		Transport:     retryTransport,
		FlushInterval: -1,
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			config.Logger.Printf("upstream request failed path=%q", request.URL.Path)
			writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "upstream unavailable"})
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", result.handleHealth)
	mux.HandleFunc("/admin/v1/identities/import", result.handleImport)
	mux.HandleFunc("/admin/v1/identities/import/batch", result.handleBatchImport)
	mux.HandleFunc("/admin/v1/identities", result.handleIdentities)
	mux.HandleFunc("/admin/v1/identities/", result.handleIdentity)
	mux.HandleFunc("/agent-identity/api/identities/import", result.handleImport)
	mux.HandleFunc("/agent-identity/api/identities/import/batch", result.handleBatchImport)
	mux.HandleFunc("/agent-identity/api/identities", result.handleIdentities)
	mux.HandleFunc("/agent-identity/api/identities/", result.handleIdentity)
	mux.HandleFunc("/v0/management/api-call", result.handleCPAAPICall)
	mux.HandleFunc("/agent-identity", result.handleUI)
	mux.HandleFunc("/agent-identity/", result.handleUI)
	mux.HandleFunc(proxyPathPrefix, result.handleProxy)
	mux.HandleFunc(proxyPathPrefix+"/", result.handleProxy)
	result.handler = securityHeaders(mux)
	return result, nil
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleUI(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if request.URL.Path == "/agent-identity" {
		http.Redirect(writer, request, "/agent-identity/", http.StatusTemporaryRedirect)
		return
	}
	asset := strings.TrimPrefix(request.URL.Path, "/agent-identity/")
	if asset == "" {
		asset = "index.html"
	}
	if strings.Contains(asset, "/") || (asset != "index.html" && asset != "app.js" && asset != "style.css" && asset != "theme.js") {
		http.NotFound(writer, request)
		return
	}
	data, err := uiFiles.ReadFile("ui/" + asset)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	embed := strings.EqualFold(strings.TrimSpace(request.URL.Query().Get("embed")), "cpamc")
	frameAncestors := "'none'"
	if embed {
		frameAncestors = "'self'"
		if len(s.config.EmbedAllowedOrigins) > 0 {
			frameAncestors += " " + strings.Join(s.config.EmbedAllowedOrigins, " ")
		}
	}
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; form-action 'none'; frame-ancestors "+frameAncestors+"; base-uri 'none'")
	writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if !embed {
		writer.Header().Set("X-Frame-Options", "DENY")
	}
	if contentType := mime.TypeByExtension(path.Ext(asset)); contentType != "" {
		writer.Header().Set("Content-Type", contentType)
	} else if asset == "index.html" {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(data)
	}
}

type importRequest struct {
	CodexAccessToken      string `json:"codex_access_token"`
	UpperCodexAccessToken string `json:"CODEX_ACCESS_TOKEN"`
	AgentIdentity         string `json:"agent_identity"`
	AccessToken           string `json:"access_token"`
}

func (s *Server) handleImport(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if !s.authorizeManagement(request) {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxImportBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	token, err := parseImportToken(body)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.mutationMu.Lock()
	result, importErr := s.importTokenLocked(request.Context(), token, false)
	s.mutationMu.Unlock()
	if importErr != nil {
		writeJSON(writer, importErr.StatusCode, map[string]any{"error": importErr.Message})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":          "ok",
		"identity":        result.PublicIdentity,
		"credential_kind": result.Credential.Kind,
		"client_api_key":  result.ClientKey,
		"cpa": map[string]any{
			"credential_type": "codex-auth-file",
			"base_url":        strings.TrimRight(strings.TrimSpace(s.config.PublicCPABaseURL), "/"),
			"managed":         s.channels != nil,
			"synced":          s.channels != nil,
		},
	})
}

func parseImportToken(body []byte) (string, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return "", errors.New("codex_access_token is required")
	}
	if body[0] != '{' && body[0] != '"' {
		return strings.TrimSpace(string(body)), nil
	}
	if body[0] == '"' {
		var token string
		if json.Unmarshal(body, &token) != nil || strings.TrimSpace(token) == "" {
			return "", errors.New("invalid request body")
		}
		return strings.TrimSpace(token), nil
	}
	var decoded importRequest
	if json.Unmarshal(body, &decoded) != nil {
		return "", errors.New("invalid request body")
	}
	for _, token := range []string{
		decoded.CodexAccessToken,
		decoded.UpperCodexAccessToken,
		decoded.AgentIdentity,
		decoded.AccessToken,
	} {
		if token = strings.TrimSpace(token); token != "" {
			return token, nil
		}
	}
	return "", errors.New("codex_access_token is required")
}

func (s *Server) handleIdentities(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if !s.authorizeManagement(request) {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	identities := s.store.List()
	sort.Slice(identities, func(i, j int) bool { return identities[i].ID < identities[j].ID })
	type listedIdentity struct {
		ID              string     `json:"id"`
		CreatedAt       time.Time  `json:"created_at"`
		CredentialKind  string     `json:"credential_kind,omitempty"`
		Email           string     `json:"email,omitempty"`
		PlanType        string     `json:"plan_type,omitempty"`
		ExpiresAt       *time.Time `json:"expires_at,omitempty"`
		Expired         bool       `json:"expired"`
		FedRAMP         bool       `json:"fedramp,omitempty"`
		ChannelManaged  bool       `json:"channel_managed"`
		ChannelSynced   bool       `json:"channel_synced"`
		ChannelDisabled bool       `json:"channel_disabled"`
		ChannelAuthFile string     `json:"channel_auth_file,omitempty"`
	}
	listed := make([]listedIdentity, 0, len(identities))
	states := map[string]cpa.IdentityState{}
	var syncError string
	if s.channels != nil {
		ids := make([]string, 0, len(identities))
		for _, item := range identities {
			ids = append(ids, item.ID)
		}
		var err error
		states, err = s.channels.IdentityStates(request.Context(), ids)
		if err != nil {
			syncError = "CPA Codex credential status unavailable"
		}
	}
	summary := map[string]int{"total": len(identities), "active": 0, "disabled": 0, "agent_identity": 0, "personal_access_token": 0, "unsynced": 0}
	for _, item := range identities {
		state := states[item.ID]
		kind := strings.TrimSpace(item.Kind)
		if kind == string(identity.CredentialKindAgentIdentity) {
			summary["agent_identity"]++
		} else if kind == string(identity.CredentialKindPersonalAccessToken) {
			summary["personal_access_token"]++
		}
		if state.Disabled {
			summary["disabled"]++
		} else if state.Synced || s.channels == nil {
			summary["active"]++
		}
		if s.channels != nil && !state.Synced {
			summary["unsynced"]++
		}
		expired := item.ExpiresAt != nil && !item.ExpiresAt.IsZero() && time.Now().After(*item.ExpiresAt)
		listed = append(listed, listedIdentity{
			ID:              item.ID,
			CreatedAt:       item.CreatedAt,
			CredentialKind:  kind,
			Email:           maskedEmail(item.Email),
			PlanType:        item.PlanType,
			ExpiresAt:       item.ExpiresAt,
			Expired:         expired,
			FedRAMP:         item.FedRAMP,
			ChannelManaged:  s.channels != nil,
			ChannelSynced:   state.Synced,
			ChannelDisabled: state.Disabled,
			ChannelAuthFile: state.AuthFile,
		})
	}
	payload := map[string]any{"identities": listed, "summary": summary, "channel_management_enabled": s.channels != nil}
	if syncError != "" {
		payload["channel_sync_error"] = syncError
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (s *Server) handleIdentity(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizeManagement(request) {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	id, actionPath := identityRequestPath(request.URL.Path)
	if id == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "identity id is required"})
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	previous, exists := s.store.GetByID(id)
	if !exists {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "identity not found"})
		return
	}
	if request.Method == http.MethodDelete && actionPath == "" {
		if err := s.store.Delete(id); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(writer, http.StatusNotFound, map[string]any{"error": "identity not found"})
				return
			}
			writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": "failed to delete identity"})
			return
		}
		if s.channels != nil {
			if err := s.channels.RemoveIdentity(request.Context(), id); err != nil {
				_ = s.store.Restore(previous)
				writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "failed to remove CPA Codex credential"})
				return
			}
		}
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	if request.Method != http.MethodPost || actionPath != "actions" {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if json.NewDecoder(io.LimitReader(request.Body, 4096)).Decode(&body) != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid action request"})
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	if s.channels == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "CPA channel management unavailable"})
		return
	}
	switch action {
	case "enable", "disable":
		if err := s.channels.SetIdentityDisabled(request.Context(), id, action == "disable"); err != nil {
			writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "failed to update CPA Codex credential state"})
			return
		}
	case "refresh":
		credential, err := s.manager.Inspect(request.Context(), previous.Token)
		if err != nil {
			if identity.CredentialServiceUnavailable(err) {
				writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "credential validation service is unavailable"})
			} else {
				writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "stored codex credential is no longer valid"})
			}
			return
		}
		if strings.TrimSpace(previous.ClientKey) == "" {
			writeJSON(writer, http.StatusConflict, map[string]any{"error": "legacy credential must be re-imported"})
			return
		}
		if err = s.store.UpdateMetadata(id, storeMetadata(credential)); err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": "failed to update credential metadata"})
			return
		}
		if err = s.channels.UpsertIdentity(request.Context(), cpaCredential(id, previous.ClientKey, credential)); err != nil {
			writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "failed to synchronize CPA Codex credential"})
			return
		}
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "unsupported identity action"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "action": action, "identity_id": id})
}

func identityRequestPath(requestPath string) (string, string) {
	relative := strings.TrimPrefix(requestPath, "/admin/v1/identities/")
	if relative == requestPath {
		relative = strings.TrimPrefix(requestPath, "/agent-identity/api/identities/")
	}
	parts := strings.Split(strings.Trim(relative, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func (s *Server) authorizeManagement(request *http.Request) bool {
	provided := bearerToken(request.Header.Get("Authorization"))
	if provided == "" {
		provided = strings.TrimSpace(request.Header.Get("X-Management-Key"))
	}
	want := strings.TrimSpace(s.config.ManagementKey)
	if provided == "" || len(provided) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(want)) == 1
}

type cpaAPICallRequest struct {
	AuthIndexSnake  *string           `json:"auth_index"`
	AuthIndexCamel  *string           `json:"authIndex"`
	AuthIndexPascal *string           `json:"AuthIndex"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Header          map[string]string `json:"header"`
	Data            string            `json:"data"`
}

type cpaAPICallResponse struct {
	StatusCode int         `json:"status_code"`
	Header     http.Header `json:"header"`
	Body       string      `json:"body"`
}

func (s *Server) handleCPAAPICall(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if !s.authorizeManagement(request) {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if s.channels == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "CPA channel management unavailable"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, maxAPICallBodyBytes+1))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid body"})
		return
	}
	if len(raw) > maxAPICallBodyBytes {
		writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]any{"error": "body too large"})
		return
	}
	var call cpaAPICallRequest
	if json.Unmarshal(raw, &call) != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid body"})
		return
	}
	method := strings.ToUpper(strings.TrimSpace(call.Method))
	quotaTarget, intercept := quota.Resolve(s.config.UpstreamOrigin, method, call.URL)
	if !intercept {
		s.forwardCPAAPICall(writer, request, raw)
		return
	}
	authIndex := firstNonEmpty(call.AuthIndexSnake, call.AuthIndexCamel, call.AuthIndexPascal)
	identityID, managed, err := s.channels.IdentityIDForAuthIndex(request.Context(), authIndex)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "CPA credential lookup failed"})
		return
	}
	storedIdentity, exists := s.store.GetByID(identityID)
	if !managed || !exists {
		s.forwardCPAAPICall(writer, request, raw)
		return
	}

	authorization, err := s.manager.Authorize(request.Context(), storedIdentity.ID, storedIdentity.Token, "quota")
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "codex credential authorization unavailable"})
		return
	}
	var body io.Reader
	if call.Data != "" {
		body = strings.NewReader(call.Data)
	}
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), method, quotaTarget.URL.String(), body)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "failed to build request"})
		return
	}
	for name, value := range call.Header {
		if blockedForwardHeader(name) {
			continue
		}
		upstreamRequest.Header.Set(name, value)
	}
	upstreamRequest.Header.Set("Authorization", authorization.Header)
	upstreamRequest.Header.Set("ChatGPT-Account-ID", authorization.AccountID)
	if authorization.FedRAMP {
		upstreamRequest.Header.Set("X-OpenAI-Fedramp", "true")
	}
	state := &requestIdentityState{
		IdentityID: storedIdentity.ID,
		Token:      storedIdentity.Token,
		TokenHash:  authorization.TokenHash,
		SessionID:  "quota",
		Kind:       authorization.Kind,
	}
	upstreamRequest = upstreamRequest.WithContext(context.WithValue(upstreamRequest.Context(), requestIdentityStateKey{}, state))
	response, err := s.upstream.Do(upstreamRequest)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "request failed"})
		return
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	_ = response.Body.Close()
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "failed to read response"})
		return
	}
	responseStatus := response.StatusCode
	responseHeaders := response.Header.Clone()
	if authorization.Kind == identity.CredentialKindPersonalAccessToken &&
		quotaTarget.Operation == quota.OperationResetCreditDetails &&
		(responseStatus == http.StatusUnauthorized || responseStatus == http.StatusForbidden) {
		if fallbackBody, fallbackHeaders, ok := s.personalAccessTokenResetCreditsFallback(upstreamRequest); ok {
			responseStatus = http.StatusOK
			responseHeaders = fallbackHeaders
			responseBody = fallbackBody
		}
	}
	writeJSON(writer, http.StatusOK, cpaAPICallResponse{
		StatusCode: responseStatus,
		Header:     responseHeaders,
		Body:       string(responseBody),
	})
}

// personalAccessTokenResetCreditsFallback mirrors the official Codex app-server
// behavior: if the detailed reset-credit request is unavailable, use the
// summary already exposed by the usage endpoint. Personal access tokens can
// read that summary even when access enforcement rejects the detail endpoint.
func (s *Server) personalAccessTokenResetCreditsFallback(original *http.Request) ([]byte, http.Header, bool) {
	usageURL := *original.URL
	usageURL.Path = "/backend-api/wham/usage"
	usageURL.RawPath = ""
	usageURL.RawQuery = ""
	usageURL.Fragment = ""

	usageRequest, err := http.NewRequestWithContext(original.Context(), http.MethodGet, usageURL.String(), nil)
	if err != nil {
		return nil, nil, false
	}
	usageRequest.Header = original.Header.Clone()
	usageRequest.Header.Del("Content-Length")
	usageRequest.Header.Del("Content-Type")

	response, err := s.upstream.Do(usageRequest)
	if err != nil {
		return nil, nil, false
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK {
		return nil, nil, false
	}

	fallbackBody, ok := quota.ResetCreditDetailsFromUsage(responseBody)
	if !ok {
		return nil, nil, false
	}
	fallbackHeaders := response.Header.Clone()
	fallbackHeaders.Set("Content-Type", "application/json")
	fallbackHeaders.Del("Content-Length")
	fallbackHeaders.Del("Content-Encoding")
	fallbackHeaders.Del("Transfer-Encoding")
	return fallbackBody, fallbackHeaders, true
}

func (s *Server) forwardCPAAPICall(writer http.ResponseWriter, request *http.Request, raw []byte) {
	status, headers, body, err := s.channels.ForwardAPICall(request.Context(), raw)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "CPA api-call unavailable"})
		return
	}
	if contentType := headers.Get("Content-Type"); contentType != "" {
		writer.Header().Set("Content-Type", contentType)
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}

func firstNonEmpty(values ...*string) string {
	for _, value := range values {
		if value != nil && strings.TrimSpace(*value) != "" {
			return strings.TrimSpace(*value)
		}
	}
	return ""
}

func blockedForwardHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "chatgpt-account-id", "x-openai-fedramp", "host", "content-length", "connection", "proxy-authorization", "proxy-authenticate", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func (s *Server) handleProxy(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != proxyPathPrefix && !strings.HasPrefix(request.URL.Path, proxyPathPrefix+"/") {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	clientKey := bearerToken(request.Header.Get("Authorization"))
	storedIdentity, ok := s.store.Lookup(clientKey)
	if !ok {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "invalid sidecar client key"})
		return
	}
	if err := prepareReplayableBody(request, s.config.MaxReplayBodyBytes); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "failed to read request body"})
		return
	}
	sessionID := logicalSessionID(request.Header)
	authorization, err := s.manager.Authorize(request.Context(), storedIdentity.ID, storedIdentity.Token, sessionID)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "codex credential authorization unavailable"})
		return
	}
	request.Header.Set("Authorization", authorization.Header)
	request.Header.Set("ChatGPT-Account-ID", authorization.AccountID)
	if authorization.FedRAMP {
		request.Header.Set("X-OpenAI-Fedramp", "true")
	} else {
		request.Header.Del("X-OpenAI-Fedramp")
	}
	state := &requestIdentityState{
		IdentityID: storedIdentity.ID,
		Token:      storedIdentity.Token,
		TokenHash:  authorization.TokenHash,
		SessionID:  sessionID,
		Kind:       authorization.Kind,
	}
	request = request.WithContext(context.WithValue(request.Context(), requestIdentityStateKey{}, state))
	if isCodexDirectImagePath(request.URL.Path) {
		s.handleCodexDirectImage(writer, request)
		return
	}
	s.proxy.ServeHTTP(writer, request)
}

func bearerToken(value string) string {
	parts := strings.SplitN(strings.TrimSpace(value), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func logicalSessionID(headers http.Header) string {
	for _, name := range []string{"Session_id", "Session-Id", "Conversation_id", "Conversation-Id"} {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return "default"
}

type multiReadCloser struct {
	io.Reader
	closer io.Closer
}

func (m *multiReadCloser) Close() error {
	return m.closer.Close()
}

func prepareReplayableBody(request *http.Request, limit int64) error {
	if request == nil || request.Body == nil || request.Body == http.NoBody {
		return nil
	}
	prefix, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(prefix)) > limit {
		request.Body = &multiReadCloser{Reader: io.MultiReader(bytes.NewReader(prefix), request.Body), closer: request.Body}
		request.GetBody = nil
		return nil
	}
	if err = request.Body.Close(); err != nil {
		return err
	}
	body := bytes.Clone(prefix)
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	request.ContentLength = int64(len(body))
	return nil
}

type authorizationRetryTransport struct {
	base    http.RoundTripper
	manager *identity.Manager
}

func (t *authorizationRetryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		return response, err
	}
	state, _ := request.Context().Value(requestIdentityStateKey{}).(*requestIdentityState)
	if state == nil {
		return response, nil
	}
	if state.Kind == identity.CredentialKindPersonalAccessToken {
		return response, nil
	}
	t.manager.InvalidateTask(state.IdentityID, state.TokenHash, state.SessionID)
	if request.Body != nil && request.Body != http.NoBody && request.GetBody == nil {
		return response, nil
	}
	authorization, authorizeErr := t.manager.Authorize(request.Context(), state.IdentityID, state.Token, state.SessionID)
	if authorizeErr != nil {
		return response, nil
	}
	retry := request.Clone(request.Context())
	retry.Header = request.Header.Clone()
	retry.Header.Set("Authorization", authorization.Header)
	retry.Header.Set("ChatGPT-Account-ID", authorization.AccountID)
	if authorization.FedRAMP {
		retry.Header.Set("X-OpenAI-Fedramp", "true")
	} else {
		retry.Header.Del("X-OpenAI-Fedramp")
	}
	if request.GetBody != nil {
		retry.Body, authorizeErr = request.GetBody()
		if authorizeErr != nil {
			return response, nil
		}
	}
	_ = response.Body.Close()
	return t.base.RoundTrip(retry)
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}
