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
	minimumSidecarVersion = "0.3.2"
	readyMessageType      = "cpa-codex-agent-identity:ready"
	themeMessageType      = "cpa-codex-agent-identity:theme"
)

var (
	pluginVersion = "0.3.2"
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
    <section class="status"><div class="panel">正在连接 Codex Agent Identity...</div></section>
    <section class="fallback"><div class="panel"><h1>Codex Agent Identity 暂时不可用</h1><p>请确认 <span class="code">sidecar_url</span> 指向可访问的 sidecar 管理页，并已允许被 CPA 管理中心嵌入。</p><p>要求 sidecar __MIN_VERSION__ 或更高版本。建议通过与 CPAMC 相同的域名发布，例如 <span class="code">/agent-identity/</span>。</p><div class="actions"><button id="retry" class="primary" type="button">重试</button><button id="open" type="button">新窗口打开</button></div></div></section>
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
	return `<!doctype html><html lang="zh-CN" data-theme="white"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Codex Agent Identity</title><style>:root{color-scheme:light;font:14px system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;--bg-primary:#fff;--bg-secondary:#fff;--bg-tertiary:#f6f6f6;--text-primary:#2d2a26;--text-secondary:#6d6760;--border-color:#e5e5e5;--primary-color:#8b8680}:root[data-theme="dark"]{color-scheme:dark;--bg-primary:#1d1b18;--bg-secondary:#151412;--bg-tertiary:#262320;--text-primary:#f6f4f1;--text-secondary:#c9c3bb;--border-color:#3a3530;--primary-color:#8b8680}*{box-sizing:border-box}html,body{margin:0;min-height:100%;background:var(--bg-primary);color:var(--text-primary)}body{min-height:100vh;display:grid;place-items:center;padding:20px}.panel{width:min(620px,100%);padding:28px;border:1px solid var(--border-color);border-radius:12px;background:var(--bg-secondary);line-height:1.65}.panel p{color:var(--text-secondary)}.code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--bg-tertiary);color:var(--text-primary);border-radius:6px;padding:2px 6px}</style></head><body><main class="panel"><h1>Codex Agent Identity</h1><p>插件配置尚未完成：</p><p>` + html.EscapeString(message) + `</p><p>请在 CPA 插件配置中设置 <span class="code">sidecar_url</span>。</p></main><script>(function(){'use strict';const key='cli-proxy-theme';const root=document.documentElement;const media=window.matchMedia('(prefers-color-scheme: dark)');const names=['--bg-primary','--bg-secondary','--bg-tertiary','--text-primary','--text-secondary','--border-color','--primary-color'];function normalized(value){return String(value||'').toLowerCase()==='dark'?'dark':'white'}function parentRoot(){try{if(window.parent!==window&&window.parent.document)return window.parent.document.documentElement}catch(_){}return null}function stored(){try{const raw=localStorage.getItem(key);if(!raw)return '';const payload=JSON.parse(raw);const state=payload&&payload.state?payload.state:payload;if(!state||typeof state!=='object')return '';if(!state.theme||state.theme==='auto'||state.theme==='system')return media.matches?'dark':'white';return normalized(state.theme||state.resolvedTheme)}catch(_){return ''}}function apply(){const source=parentRoot();const attr=source&&source.getAttribute('data-theme');root.dataset.theme=attr?normalized(attr):(stored()||(media.matches?'dark':'white'));names.forEach(function(name){root.style.removeProperty(name)});if(source){try{const view=source.ownerDocument&&source.ownerDocument.defaultView?source.ownerDocument.defaultView:window;const styles=view.getComputedStyle(source);names.forEach(function(name){const value=styles.getPropertyValue(name).trim();if(value&&value.length<=256)root.style.setProperty(name,value)})}catch(_){}}}const source=parentRoot();if(source&&typeof MutationObserver==='function')new MutationObserver(apply).observe(source,{attributes:true,attributeFilter:['data-theme','style','class']});window.addEventListener('storage',function(event){if(event.key===key)apply()});if(typeof media.addEventListener==='function')media.addEventListener('change',apply);else if(typeof media.addListener==='function')media.addListener(apply);apply()})();</script></body></html>`
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
