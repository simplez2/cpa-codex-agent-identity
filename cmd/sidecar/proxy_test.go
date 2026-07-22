package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func forceProxySourceRefresh(source *proxyConfigSource) {
	source.mu.Lock()
	source.nextCheck = time.Time{}
	source.mu.Unlock()
}

func TestProxyConfigSourceFollowsCPAAndClearsToFallback(t *testing.T) {
	var mu sync.Mutex
	proxyValue := ""
	proxyConfigUnavailable := false
	cpa := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		value := proxyValue
		unavailable := proxyConfigUnavailable
		mu.Unlock()
		if unavailable {
			http.Error(writer, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]string{"proxy-url": value})
	}))
	defer cpa.Close()

	source, err := newProxyConfigSource(
		"socks5://fallback.example:1080",
		"TEST_PROXY_VALUE",
		"TEST_PROXY_FILE",
		cpa.URL+"/config",
		"management-key",
		cpa.Client(),
		minProxyConfigPollInterval,
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := source.current(t.Context())
	if err != nil || got != "socks5://fallback.example:1080" {
		t.Fatalf("initial proxy=%q err=%v", got, err)
	}

	mu.Lock()
	proxyValue = "socks5://new.example:1080"
	mu.Unlock()
	forceProxySourceRefresh(source)
	got, err = source.current(t.Context())
	if err != nil || got != "socks5://new.example:1080" {
		t.Fatalf("updated proxy=%q err=%v", got, err)
	}

	mu.Lock()
	proxyConfigUnavailable = true
	mu.Unlock()
	forceProxySourceRefresh(source)
	got, err = source.current(t.Context())
	if err == nil || got != "socks5://new.example:1080" {
		t.Fatalf("proxy during CPA outage=%q err=%v", got, err)
	}

	mu.Lock()
	proxyConfigUnavailable = false
	proxyValue = ""
	mu.Unlock()
	forceProxySourceRefresh(source)
	got, err = source.current(t.Context())
	if err != nil || got != "socks5://fallback.example:1080" {
		t.Fatalf("cleared proxy=%q err=%v", got, err)
	}
}

func TestHotReloadTransportSwapsProxyAndDirectModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy")
	if err := os.WriteFile(path, []byte("socks5://one.example:1080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PROXY_VALUE", "")
	t.Setenv("TEST_PROXY_FILE", path)
	source, err := newProxyConfigSource(
		"socks5://one.example:1080",
		"TEST_PROXY_VALUE",
		"TEST_PROXY_FILE",
		"",
		"",
		nil,
		minProxyConfigPollInterval,
	)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newHotReloadTransport("socks5://one.example:1080", source, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	proxy, err := transport.current.Load().Proxy(request)
	if err != nil || proxy == nil || proxy.Host != "one.example:1080" {
		t.Fatalf("initial proxy=%v err=%v", proxy, err)
	}

	if err := os.WriteFile(path, []byte("socks5://two.example:1080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	forceProxySourceRefresh(source)
	if err := transport.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	proxy, err = transport.current.Load().Proxy(request)
	if err != nil || proxy == nil || proxy.Host != "two.example:1080" {
		t.Fatalf("reloaded proxy=%v err=%v", proxy, err)
	}

	if err := os.WriteFile(path, []byte("direct\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	forceProxySourceRefresh(source)
	if err := transport.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	active := transport.current.Load()
	if active == nil || active.Proxy != nil {
		t.Fatalf("direct transport proxy=%v", active)
	}
}

func TestManagementConfigURL(t *testing.T) {
	got, err := managementConfigURL("http://cpa:8317/v0/management/")
	if err != nil || got != "http://cpa:8317/v0/management/config" {
		t.Fatalf("url=%q err=%v", got, err)
	}
}
