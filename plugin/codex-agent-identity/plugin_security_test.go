package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSidecarUIQueryRejectsMalformedBeforeForwarding(t *testing.T) {
	requests := 0
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.RawQuery != "atomic=false&preview=true" {
			t.Errorf("forwarded query is not canonical: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"preview":true}`)
	}))
	defer sidecar.Close()
	configurePluginForTest(t, "sidecar_api_url: "+sidecar.URL)
	for _, path := range []string{
		"diagnostics?x;y=1",
		"diagnostics?foo=%zz",
		"identities/import/batch?preview=true&x;y=1",
		"identities/import/batch?preview=true&unknown=%zz",
		"identities/import/batch?preview=true&preview=false",
		"identities/import/batch?preview=true&atomic=false&extra=true",
		"identities/import%2fbatch?preview=true",
		"identities/%252e%252e/diagnostics",
	} {
		raw, err := json.Marshal(sidecarUIAPIRequest{Method: http.MethodPost, Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(path, "diagnostics") {
			raw, _ = json.Marshal(sidecarUIAPIRequest{Method: http.MethodGet, Path: path})
		}
		result := forwardSidecarUIAPI(managementRequest{Body: raw})
		if result.StatusCode != http.StatusBadRequest {
			t.Errorf("unsafe request %q returned %d", path, result.StatusCode)
		}
	}
	if requests != 0 {
		t.Fatalf("%d rejected requests reached the sidecar", requests)
	}
	raw, _ := json.Marshal(sidecarUIAPIRequest{Method: http.MethodPost, Path: "identities/import/batch?preview=%74rue&atomic=false"})
	if result := forwardSidecarUIAPI(managementRequest{Body: raw}); result.StatusCode != http.StatusOK {
		t.Fatalf("canonical request failed: %d %s", result.StatusCode, result.Body)
	}
	if requests != 1 {
		t.Fatalf("canonical request count = %d", requests)
	}
}

func TestSidecarURLRejectsAmbiguousPathsAndDoesNotEchoSecrets(t *testing.T) {
	for _, path := range []string{
		"/agent-identity/../admin/", "/agent-identity/./", "/agent-identity/%2e%2e/admin/",
		"/agent-identity/%252e%252e/admin/", "/agent-identity/%5cadmin/",
		"/agent-identity/%255cadmin/", "/agent-identity%2fadmin/", "/agent-identity//admin/",
		"/agent-identity/%00/", "/agent-identity/%0d%0a/", "/agent-identity/%3fadmin/",
	} {
		t.Run(path, func(t *testing.T) {
			for _, target := range []string{path, "https://sidecar.example.test" + path} {
				if _, _, err := normalizeSidecarURL(target); err == nil {
					t.Errorf("UI accepted %q", target)
				}
			}
			if _, err := normalizeSidecarAPIURL("https://sidecar.example.test" + path); err == nil {
				t.Errorf("API accepted %q", path)
			}
		})
	}
	for _, path := range []string{"/", "/agent-identity/", "/custom-dashboard", "/prefix/agent-identity/"} {
		if _, _, err := normalizeSidecarURL(path); err != nil {
			t.Errorf("valid UI path %q rejected: %v", path, err)
		}
		if _, err := normalizeSidecarAPIURL("https://sidecar.example.test" + path); err != nil {
			t.Errorf("valid API path %q rejected: %v", path, err)
		}
	}
	const sentinel = "do-not-echo-invalid-url-secret"
	for _, target := range []string{
		"https://user:" + sentinel + "@host/%zz",
		"https://host/" + sentinel + "%zz",
	} {
		_, _, err := normalizeSidecarURL(target)
		if err == nil || strings.Contains(err.Error(), sentinel) {
			t.Fatal("invalid URL was accepted or echoed the input")
		}
	}
}

func TestSidecarBridgesOnlyForwardCPAManagementCredentials(t *testing.T) {
	for _, header := range []string{"Authorization", "X-Management-Key"} {
		for _, ui := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ui=%t", header, ui), func(t *testing.T) {
				hits := 0
				sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits++
					if r.Header.Get(header) != "management-fixture" {
						t.Error("CPA management credential was not preserved")
					}
					for _, forbidden := range []string{"Cookie", "Origin", "Referer", "Forwarded", "X-Forwarded-For", "Proxy-Authorization", "X-API-Key", "X-Custom-Secret"} {
						if r.Header.Get(forbidden) != "" {
							t.Errorf("%s crossed the sidecar control-plane boundary", forbidden)
						}
					}
					fmt.Fprint(w, `{}`)
				}))
				defer sidecar.Close()
				configurePluginForTest(t, "sidecar_api_url: "+sidecar.URL)
				headers := http.Header{header: []string{"management-fixture"}}
				for _, forbidden := range []string{"Cookie", "Origin", "Referer", "Forwarded", "X-Forwarded-For", "Proxy-Authorization", "X-API-Key", "X-Custom-Secret"} {
					headers.Set(forbidden, "must-not-forward-fixture")
				}
				request := managementRequest{Headers: headers, Body: []byte(`{}`)}
				var result managementResponse
				if ui {
					request.Body, _ = json.Marshal(sidecarUIAPIRequest{
						Method: http.MethodGet, Path: "diagnostics",
						Headers: map[string]string{"Authorization": "must-not-override-outer-auth"},
					})
					result = forwardSidecarUIAPI(request)
				} else {
					result = forwardSidecarAPICall(request)
				}
				if result.StatusCode != http.StatusOK || hits != 1 {
					t.Fatalf("bridge failed: status=%d hits=%d", result.StatusCode, hits)
				}
			})
		}
	}
}

func TestManagementUIBridgeHandshake(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required for full generated wrapper event regression")
	}
	body := managementHTML(defaultSidecarURL, defaultSidecarEmbedURL)
	start := strings.Index(body, "<script>")
	end := strings.LastIndex(body, "</script>")
	if start < 0 || end <= start {
		t.Fatal("wrapper script missing")
	}
	input, _ := json.Marshal(map[string]string{
		"script":    body[start+len("<script>") : end],
		"readyType": readyMessageType, "authType": managementKeyMessageType,
		"unavailableType": unavailableMessageType,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "testdata/bridge_security.cjs")
	cmd.Stdin = bytes.NewReader(input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated wrapper handshake failed: %v\n%s", err, output)
	}
}
