package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/simplez2/cpa-codex-agent-identity/internal/egress"
)

func TestCredentialProxyNeverFallsBackToDirect(t *testing.T) {
	var reached atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); w.WriteHeader(200) }))
	defer origin.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	transport, err := newHotReloadTransport("direct", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, proxy := range []string{"http://" + address, "socks5://" + address, "bad://invalid", ":invalid"} {
		r, _ := http.NewRequestWithContext(egress.WithProxy(t.Context(), proxy), "GET", origin.URL, nil)
		response, err := transport.RoundTrip(r)
		if response != nil {
			response.Body.Close()
		}
		if err == nil {
			t.Fatal("broken proxy unexpectedly succeeded")
		}
	}
	if reached.Load() != 0 {
		t.Fatal("origin reached despite broken proxy")
	}
	r, _ := http.NewRequestWithContext(egress.WithProxy(t.Context(), "direct"), "GET", origin.URL, nil)
	response, err := transport.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if reached.Load() != 1 {
		t.Fatal("explicit direct recovery failed")
	}
}

func TestGlobalProxyFailureRemainsClosedThroughBackoff(t *testing.T) {
	config := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer config.Close()
	source, err := newProxyConfigSource("direct", "", "", config.URL, "test-key", config.Client(), minProxyConfigPollInterval)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newHotReloadTransport("direct", source, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if transport.refresh(t.Context()) == nil {
			t.Fatal("failed config became ready")
		}
		r, _ := http.NewRequestWithContext(t.Context(), "GET", config.URL, nil)
		if _, err := transport.RoundTrip(r); err == nil {
			t.Fatal("request escaped failed config")
		}
	}
}
