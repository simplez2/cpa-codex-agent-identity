package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/simplez2/cpa-codex-agent-identity/plugin/codex-agent-identity/internal/credential"
	"gopkg.in/yaml.v3"
)

const (
	pluginID              = "codex-agent-identity"
	pluginName            = "Codex Agent Identity"
	pluginMenu            = "Agent Identity"
	pluginAuthor          = "simplez2"
	pluginRepository      = "https://github.com/simplez2/cpa-codex-agent-identity"
	pluginLogo            = "https://raw.githubusercontent.com/simplez2/cpa-codex-agent-identity/main/assets/logo.svg"
	resourcePath          = "/open"
	configSidecarURL      = "sidecar_url"
	minimumSidecarVersion = "0.3.0"
	readyMessageType      = "cpa-codex-agent-identity:ready"
)

var (
	pluginVersion = "0.3.0"
	stateMu       sync.RWMutex
	state         = runtimeState{configError: "sidecar_url is required"}
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
	SidecarURL string `yaml:"sidecar_url"`
}

type runtimeState struct {
	sidecarURL  string
	embedURL    string
	frameSource string
	configError string
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
	Resources []managementResource `json:"resources,omitempty"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
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
			SchemaVersion: pluginabi.SchemaVersion,
			Metadata: pluginapi.Metadata{
				Name:             pluginName,
				Version:          pluginVersion,
				Author:           pluginAuthor,
				GitHubRepository: pluginRepository,
				Logo:             pluginLogo,
				ConfigFields: []pluginapi.ConfigField{{
					Name:        configSidecarURL,
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Public Codex Agent Identity sidecar root URL, preferably same-origin under /agent-identity/.",
				}},
			},
			Capabilities: registrationCapability{AuthProvider: true, ManagementAPI: true},
		})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{Resources: []managementResource{{
			Path:        resourcePath,
			Menu:        pluginMenu,
			Description: "Manage Codex Agent Identity and PAT credentials. Requires sidecar " + minimumSidecarVersion + " or later.",
		}}})
	case pluginabi.MethodManagementHandle:
		return okEnvelope(currentManagementResponse())
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: "codex"})
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
		return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{
			Provider:    "codex",
			ID:          parsed.ID,
			FileName:    parsed.FileName,
			Label:       parsed.Label,
			Prefix:      parsed.Prefix,
			StorageJSON: parsed.StorageJSON,
			Metadata:    parsed.Metadata,
			Attributes:  parsed.Attributes,
		}})
	case pluginabi.MethodAuthRefresh:
		var req pluginapi.AuthRefreshRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, errors.New("invalid auth refresh request")
		}
		fileName := req.AuthID
		if rawName, ok := req.Metadata["file_name"].(string); ok && rawName != "" {
			fileName = rawName
		}
		return okEnvelope(pluginapi.AuthRefreshResponse{
			Auth: pluginapi.AuthData{
				Provider:    "codex",
				ID:          req.AuthID,
				FileName:    fileName,
				Label:       labelFromMetadata(req.Metadata, req.AuthID),
				StorageJSON: req.StorageJSON,
				Metadata:    req.Metadata,
				Attributes:  req.Attributes,
			},
			NextRefreshAfter: time.Now().Add(24 * time.Hour).UTC(),
		})
	case pluginabi.MethodAuthLoginStart:
		return okEnvelope(pluginapi.AuthLoginStartResponse{Provider: "codex", ExpiresAt: time.Now().UTC()})
	case pluginabi.MethodAuthLoginPoll:
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "use the Agent Identity management page"})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func applyConfig(request []byte) {
	next := runtimeState{}
	if len(bytes.TrimSpace(request)) == 0 {
		next.configError = "sidecar_url is required"
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
	next.sidecarURL = normalized
	next.embedURL = embedURL
	next.frameSource = frameSource
	setRuntimeState(next)
}

func normalizeSidecarURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("sidecar_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("sidecar_url is invalid: %w", err)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("sidecar_url must not contain credentials, query parameters, or a fragment")
	}
	frameSource := "'self'"
	if u.IsAbs() {
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", "", errors.New("sidecar_url must use http:// or https://")
		}
		if u.Host == "" {
			return "", "", errors.New("sidecar_url host is required")
		}
		frameSource = u.Scheme + "://" + u.Host
	} else if !strings.HasPrefix(u.Path, "/") {
		return "", "", errors.New("sidecar_url must be absolute or start with /")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), frameSource, nil
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

func currentManagementResponse() managementResponse {
	current := currentRuntimeState()
	if current.configError != "" {
		return managementResponse{
			StatusCode: http.StatusServiceUnavailable,
			Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       []byte(configFallbackHTML(current.configError)),
		}
	}
	csp := "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; frame-src " + current.frameSource + "; base-uri 'none'; form-action 'none'"
	return managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"Content-Security-Policy": []string{csp},
			"Referrer-Policy":         []string{"no-referrer"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"Cache-Control":           []string{"no-store"},
		},
		Body: []byte(managementHTML(current.sidecarURL, current.embedURL)),
	}
}

func managementHTML(sidecarURL, embedURL string) string {
	escapedURL := html.EscapeString(embedURL)
	jsURL, _ := json.Marshal(sidecarURL)
	template := `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Codex Agent Identity</title><style>html,body{margin:0;width:100%;height:100%;font:14px system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#070b12;color:#e5edf8}.shell{position:fixed;inset:0;overflow:hidden}.identity-frame{display:block;width:100%;height:100%;border:0;background:#070b12}.status,.fallback{position:absolute;inset:0;display:grid;place-items:center;background:#070b12}.panel{max-width:620px;padding:28px;line-height:1.65;text-align:center}.fallback{display:none}.fallback h1{margin:0 0 12px;font-size:22px}.code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;background:#172033;border-radius:6px;padding:2px 6px}.actions{display:flex;gap:12px;justify-content:center;margin-top:18px}.actions button{border:1px solid #334155;border-radius:9px;background:#111827;color:#e5edf8;cursor:pointer;padding:9px 15px}.actions .primary{border-color:#2563eb;background:#2563eb;color:#fff}html[data-ready="true"] .status{display:none}html[data-failed="true"] .status{display:none}html[data-failed="true"] .fallback{display:grid}</style></head><body><main class="shell"><iframe id="identityFrame" class="identity-frame" title="Codex Agent Identity" src="__EMBED_URL__"></iframe><section class="status"><div class="panel">正在连接 Codex Agent Identity...</div></section><section class="fallback"><div class="panel"><h1>Codex Agent Identity 暂时不可用</h1><p>请确认 <span class="code">sidecar_url</span> 指向可访问的 sidecar 管理页，并已允许被 CPA 管理中心嵌入。</p><p>首版要求 sidecar __MIN_VERSION__ 或更高版本。建议通过与 CPAMC 相同的域名发布，例如 <span class="code">/agent-identity/</span>。</p><div class="actions"><button id="retry" class="primary" type="button">重试</button><button id="open" type="button">新窗口打开</button></div></div></section></main><script>(function(){const root=__ROOT_URL__;const frame=document.getElementById('identityFrame');const retry=document.getElementById('retry');const open=document.getElementById('open');let timer=0;function connecting(){document.documentElement.removeAttribute('data-ready');document.documentElement.removeAttribute('data-failed')}function ready(){clearTimeout(timer);document.documentElement.removeAttribute('data-failed');document.documentElement.setAttribute('data-ready','true')}function failed(){document.documentElement.removeAttribute('data-ready');document.documentElement.setAttribute('data-failed','true')}function start(){clearTimeout(timer);timer=setTimeout(failed,10000)}window.addEventListener('message',function(event){if(!frame||event.source!==frame.contentWindow)return;const data=event.data||{};if(data.type==='__READY_TYPE__')ready()});retry.addEventListener('click',function(){connecting();frame.src=frame.src;start()});open.addEventListener('click',function(){window.open(root,'_blank','noopener')});start()})();</script></body></html>`
	return strings.NewReplacer(
		"__EMBED_URL__", escapedURL,
		"__MIN_VERSION__", minimumSidecarVersion,
		"__ROOT_URL__", string(jsURL),
		"__READY_TYPE__", readyMessageType,
	).Replace(template)
}

func configFallbackHTML(message string) string {
	if strings.TrimSpace(message) == "" {
		message = "sidecar_url is not configured"
	}
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Codex Agent Identity</title><style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#070b12;color:#e5edf8;font:14px system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.panel{max-width:620px;padding:28px;line-height:1.65}.code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;background:#172033;border-radius:6px;padding:2px 6px}</style></head><body><main class="panel"><h1>Codex Agent Identity</h1><p>插件配置尚未完成：</p><p>` + html.EscapeString(message) + `</p><p>请在 CPA 插件配置中设置 <span class="code">sidecar_url</span>。</p></main></body></html>`
}

func labelFromMetadata(metadata map[string]any, fallback string) string {
	if email, ok := metadata["email"].(string); ok && email != "" {
		return email
	}
	return fallback
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
