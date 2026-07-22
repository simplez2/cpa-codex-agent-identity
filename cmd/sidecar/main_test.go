package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOptionalSecretPrefersFile(t *testing.T) {
	t.Setenv("TEST_SECRET_VALUE", "environment-value")
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SECRET_FILE", path)
	value, configured, err := readOptionalSecret("TEST_SECRET_VALUE", "TEST_SECRET_FILE")
	if err != nil || !configured || value != "file-value" {
		t.Fatalf("value=%q configured=%v err=%v", value, configured, err)
	}
}

func TestOutboundTransportSupportsSOCKS5Proxy(t *testing.T) {
	transport, err := outboundTransport("socks5://user:password@127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	proxyURL, err := transport.Proxy(request)
	if err != nil || proxyURL == nil || proxyURL.Scheme != "socks5" || proxyURL.Host != "127.0.0.1:1080" {
		t.Fatalf("proxy=%v err=%v", proxyURL, err)
	}
}
