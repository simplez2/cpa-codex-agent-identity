package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultProxyConfigPollInterval = time.Second
	minProxyConfigPollInterval     = 100 * time.Millisecond
	maxProxyConfigPollInterval     = 5 * time.Minute
	maxCPAConfigResponseBytes      = 1 << 20
)

// proxyConfigSource reads the current outbound proxy from CPA's management
// config. The file/environment value remains the durable fallback so an
// already-working deployment stays reachable while CPA is starting and when
// CPA's global proxy-url is empty.
type proxyConfigSource struct {
	valueEnvironment string
	fileEnvironment  string
	fallback         string
	cpaConfigURL     string
	cpaManagementKey string
	cpaClient        *http.Client
	pollInterval     time.Duration

	mu        sync.Mutex
	refreshMu sync.Mutex
	nextCheck time.Time
	effective string
}

func newProxyConfigSource(initial, valueEnvironment, fileEnvironment, cpaConfigURL, cpaManagementKey string, cpaClient *http.Client, pollInterval time.Duration) (*proxyConfigSource, error) {
	if pollInterval <= 0 {
		pollInterval = defaultProxyConfigPollInterval
	}
	if pollInterval < minProxyConfigPollInterval || pollInterval > maxProxyConfigPollInterval {
		return nil, errors.New("CPA_PROXY_CONFIG_POLL_INTERVAL is outside the supported range")
	}
	if cpaConfigURL != "" {
		parsed, err := url.Parse(cpaConfigURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, errors.New("CPA proxy config URL is invalid")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, errors.New("CPA proxy config URL must use HTTP or HTTPS")
		}
		if cpaClient == nil || strings.TrimSpace(cpaManagementKey) == "" {
			return nil, errors.New("CPA proxy config client and key are required")
		}
	}
	return &proxyConfigSource{
		valueEnvironment: strings.TrimSpace(valueEnvironment),
		fileEnvironment:  strings.TrimSpace(fileEnvironment),
		fallback:         strings.TrimSpace(initial),
		cpaConfigURL:     strings.TrimRight(strings.TrimSpace(cpaConfigURL), "/"),
		cpaManagementKey: strings.TrimSpace(cpaManagementKey),
		cpaClient:        cpaClient,
		pollInterval:     pollInterval,
		effective:        strings.TrimSpace(initial),
	}, nil
}

func (s *proxyConfigSource) current(ctx context.Context) (string, error) {
	now := time.Now()
	s.mu.Lock()
	if now.Before(s.nextCheck) {
		raw := s.effective
		s.mu.Unlock()
		return raw, nil
	}
	s.mu.Unlock()

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	now = time.Now()
	if now.Before(s.nextCheck) {
		raw := s.effective
		s.mu.Unlock()
		return raw, nil
	}
	s.nextCheck = now.Add(s.pollInterval)
	effective := s.effective
	s.mu.Unlock()

	if s.cpaConfigURL == "" {
		raw, configured, err := readOptionalSecret(s.valueEnvironment, s.fileEnvironment)
		if err != nil {
			return effective, err
		}
		if !configured {
			raw = s.fallback
		}
		return s.updateEffective(raw), nil
	}

	raw, err := s.fetchCPAProxy(ctx)
	if err != nil {
		// A control-plane outage must not move live traffic to a different
		// route. The constructor seeds effective with the bootstrap fallback,
		// so this is safe both during startup and after a CPA override.
		return effective, err
	}

	if raw == "" {
		fallback, configured, fallbackErr := readOptionalSecret(s.valueEnvironment, s.fileEnvironment)
		if fallbackErr != nil {
			return effective, fallbackErr
		}
		if !configured {
			fallback = s.fallback
		}
		raw = fallback
	}
	return s.updateEffective(raw), nil
}

func (s *proxyConfigSource) updateEffective(raw string) string {
	raw = strings.TrimSpace(raw)
	s.mu.Lock()
	s.effective = raw
	s.mu.Unlock()
	return raw
}

func (s *proxyConfigSource) fetchCPAProxy(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cpaConfigURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+s.cpaManagementKey)
	request.Header.Set("Accept", "application/json")
	response, err := s.cpaClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CPA proxy config returned HTTP %d", response.StatusCode)
	}
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(response.Body, maxCPAConfigResponseBytes)).Decode(&payload); err != nil {
		return "", errors.New("CPA proxy config response is invalid")
	}
	rawValue := payload["proxy-url"]
	if len(rawValue) == 0 {
		return "", nil
	}
	var proxyValue string
	if err := json.Unmarshal(rawValue, &proxyValue); err != nil {
		return "", errors.New("CPA proxy-url value is invalid")
	}
	return strings.TrimSpace(proxyValue), nil
}

// hotReloadTransport swaps complete http.Transport instances atomically. A
// transport's Proxy field is not safe to mutate after first use, so replacing
// the instance also lets us close idle connections from the previous route.
type hotReloadTransport struct {
	source  *proxyConfigSource
	current atomic.Pointer[http.Transport]

	activeMu  sync.RWMutex
	activeRaw string
	refreshMu sync.Mutex
	logger    *log.Logger

	logMu              sync.Mutex
	lastRefreshError   string
	lastRefreshLogTime time.Time
}

func newHotReloadTransport(initial string, source *proxyConfigSource, logger *log.Logger) (*hotReloadTransport, error) {
	transport, err := outboundTransport(initial)
	if err != nil {
		return nil, err
	}
	result := &hotReloadTransport{source: source, activeRaw: strings.TrimSpace(initial), logger: logger}
	result.current.Store(transport)
	return result, nil
}

func (t *hotReloadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("nil HTTP request")
	}
	transport := t.current.Load()
	if transport == nil {
		return nil, errors.New("outbound transport is unavailable")
	}
	return transport.RoundTrip(request)
}

func (t *hotReloadTransport) refresh(ctx context.Context) error {
	t.refreshMu.Lock()
	defer t.refreshMu.Unlock()
	raw, sourceErr := t.source.current(ctx)
	if sourceErr != nil {
		t.logRefreshError(sourceErr)
	}
	t.activeMu.RLock()
	unchanged := raw == t.activeRaw
	t.activeMu.RUnlock()
	if unchanged {
		return sourceErr
	}
	next, err := outboundTransport(raw)
	if err != nil {
		t.logRefreshError(err)
		return errors.Join(sourceErr, err)
	}
	t.activeMu.Lock()
	if raw == t.activeRaw {
		t.activeMu.Unlock()
		next.CloseIdleConnections()
		return nil
	}
	old := t.current.Swap(next)
	t.activeRaw = raw
	t.activeMu.Unlock()
	if old != nil {
		old.CloseIdleConnections()
	}
	if t.logger != nil {
		t.logger.Printf("outbound proxy reloaded mode=%s", proxyMode(raw))
	}
	return sourceErr
}

func (t *hotReloadTransport) Watch(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	_ = t.refresh(ctx)
	ticker := time.NewTicker(t.source.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = t.refresh(ctx)
		}
	}
}

func (t *hotReloadTransport) CloseIdleConnections() {
	if transport := t.current.Load(); transport != nil {
		transport.CloseIdleConnections()
	}
}

func (t *hotReloadTransport) logRefreshError(err error) {
	if t.logger == nil || err == nil {
		return
	}
	message := err.Error()
	now := time.Now()
	t.logMu.Lock()
	defer t.logMu.Unlock()
	if message == t.lastRefreshError && now.Sub(t.lastRefreshLogTime) < 30*time.Second {
		return
	}
	t.lastRefreshError = message
	t.lastRefreshLogTime = now
	t.logger.Printf("outbound proxy reload failed: %s", message)
}

func proxyMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "direct", "none":
		return "direct"
	default:
		if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
			return parsed.Scheme
		}
		return "invalid"
	}
}

func directHTTPTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ForceAttemptHTTP2 = true
	return transport
}

func managementConfigURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("CPA management URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("CPA management URL must use HTTP or HTTPS")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/config"
	return parsed.String(), nil
}

func proxyPollInterval() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("CPA_PROXY_CONFIG_POLL_INTERVAL"))
	if raw == "" {
		return defaultProxyConfigPollInterval, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < minProxyConfigPollInterval || value > maxProxyConfigPollInterval {
		return 0, errors.New("CPA_PROXY_CONFIG_POLL_INTERVAL is outside the supported range")
	}
	return value, nil
}
