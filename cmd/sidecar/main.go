package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/simplez2/cpa-codex-agent-identity/internal/cpa"
	"github.com/simplez2/cpa-codex-agent-identity/internal/identity"
	"github.com/simplez2/cpa-codex-agent-identity/internal/server"
	identitystore "github.com/simplez2/cpa-codex-agent-identity/internal/store"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(); err != nil {
			os.Exit(1)
		}
		return
	}
	logger := log.New(os.Stderr, "codex-agent-identity: ", log.LstdFlags|log.LUTC)
	if err := run(logger); err != nil {
		logger.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(logger *log.Logger) error {
	listenAddress := envOrDefault("LISTEN_ADDR", ":8787")
	dataDirectory := envOrDefault("DATA_DIR", "/data")
	managementKey, err := readSecret("MANAGEMENT_KEY", "MANAGEMENT_KEY_FILE")
	if err != nil {
		return err
	}
	if len(managementKey) < 24 {
		return errors.New("management key must contain at least 24 characters")
	}
	upstreamOrigin, err := url.Parse(envOrDefault("UPSTREAM_ORIGIN", "https://chatgpt.com"))
	if err != nil || upstreamOrigin.Scheme == "" || upstreamOrigin.Host == "" {
		return errors.New("UPSTREAM_ORIGIN is invalid")
	}
	allowInsecureUpstream, err := boolEnv("ALLOW_INSECURE_UPSTREAM", false)
	if err != nil {
		return err
	}
	if upstreamOrigin.User != nil || upstreamOrigin.RawQuery != "" || upstreamOrigin.Fragment != "" ||
		(upstreamOrigin.Scheme != "https" && !(allowInsecureUpstream && upstreamOrigin.Scheme == "http")) {
		return errors.New("UPSTREAM_ORIGIN must be an HTTPS origin without credentials, query, or fragment")
	}

	cpaManagementURL := strings.TrimSpace(os.Getenv("CPA_MANAGEMENT_URL"))
	cpaManagementKey := managementKey
	var cpaClient *http.Client
	var cpaProxyConfigURL string
	if cpaManagementURL != "" {
		if os.Getenv("CPA_MANAGEMENT_KEY") != "" || os.Getenv("CPA_MANAGEMENT_KEY_FILE") != "" {
			cpaManagementKey, err = readSecret("CPA_MANAGEMENT_KEY", "CPA_MANAGEMENT_KEY_FILE")
			if err != nil {
				return err
			}
		}
		cpaProxyConfigURL, err = managementConfigURL(cpaManagementURL)
		if err != nil {
			return err
		}
		cpaClient = &http.Client{Transport: directHTTPTransport(), Timeout: 2 * time.Second}
	}
	outboundProxy, _, err := readOptionalSecret("OUTBOUND_PROXY", "OUTBOUND_PROXY_FILE")
	if err != nil {
		return err
	}
	pollInterval, err := proxyPollInterval()
	if err != nil {
		return err
	}
	proxySource, err := newProxyConfigSource(
		outboundProxy,
		"OUTBOUND_PROXY",
		"OUTBOUND_PROXY_FILE",
		cpaProxyConfigURL,
		cpaManagementKey,
		cpaClient,
		pollInterval,
	)
	if err != nil {
		return err
	}
	transport, err := newHotReloadTransport(outboundProxy, proxySource, logger)
	if err != nil {
		return err
	}
	// Load CPA's current proxy before identity inspection. Subsequent refreshes
	// stay on the background watcher and never delay model traffic.
	_ = transport.refresh(context.Background())
	identityClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	manager := identity.NewManagerWithPersonalAccessTokenAPI(
		envOrDefault("JWKS_URL", identity.DefaultJWKSURL),
		envOrDefault("AUTH_API_BASE_URL", identity.DefaultAuthAPIURL),
		envOrDefault("PERSONAL_ACCESS_TOKEN_AUTH_API_BASE_URL", identity.DefaultPersonalAccessTokenAuthAPIURL),
		identityClient,
	)
	var storeOptions []identitystore.Option
	encryptionKeyValue, encryptionConfigured, err := readOptionalSecret("DATA_ENCRYPTION_KEY", "DATA_ENCRYPTION_KEY_FILE")
	if err != nil {
		return err
	}
	var encryptionKey []byte
	if encryptionConfigured {
		encryptionKey, err = identitystore.ParseEncryptionKey(encryptionKeyValue)
		if err != nil {
			return err
		}
		storeOptions = append(storeOptions, identitystore.WithEncryptionKey(encryptionKey))
	} else {
		allowPlaintext, parseErr := boolEnv("ALLOW_PLAINTEXT_STORE", false)
		if parseErr != nil {
			return parseErr
		}
		if !allowPlaintext {
			return errors.New("DATA_ENCRYPTION_KEY_FILE is required unless ALLOW_PLAINTEXT_STORE=true")
		}
		logger.Printf("warning: identity store encryption is disabled")
	}
	credentialStore, err := identitystore.Open(dataDirectory, storeOptions...)
	for index := range encryptionKey {
		encryptionKey[index] = 0
	}
	if err != nil {
		return err
	}
	maxReplayBytes, err := int64Env("MAX_REPLAY_BODY_BYTES", 16<<20)
	if err != nil {
		return err
	}
	publicCPABaseURL := envOrDefault("PUBLIC_CPA_BASE_URL", "http://codex-agent-identity-sidecar:8787/backend-api/codex")
	var channelManager *cpa.Manager
	if cpaManagementURL != "" {
		channelManager, err = cpa.NewManager(
			cpaManagementURL,
			cpaManagementKey,
			publicCPABaseURL,
			&http.Client{Transport: directHTTPTransport(), Timeout: 12 * time.Second},
		)
		if err != nil {
			return err
		}
		reconcileStoredCredentials(logger, credentialStore, manager, channelManager)
	}
	handler, err := server.New(server.Config{
		UpstreamOrigin:      upstreamOrigin,
		PublicCPABaseURL:    publicCPABaseURL,
		ManagementKey:       managementKey,
		MaxReplayBodyBytes:  maxReplayBytes,
		OutboundTransport:   transport,
		Logger:              logger,
		CPAChannels:         channelManager,
		EmbedAllowedOrigins: strings.Split(os.Getenv("EMBED_ALLOWED_ORIGINS"), ","),
	}, credentialStore, manager)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 << 10,
		BaseContext: func(net.Listener) context.Context {
			return context.Background()
		},
	}
	shutdownContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go transport.Watch(shutdownContext)
	defer transport.CloseIdleConnections()
	go func() {
		<-shutdownContext.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	logger.Printf("listening addr=%s identities=%d upstream=%s", listenAddress, len(credentialStore.List()), upstreamOrigin.Host)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func runHealthcheck() error {
	endpoint := envOrDefault("HEALTHCHECK_URL", "http://127.0.0.1:8787/healthz")
	client := &http.Client{Timeout: 3 * time.Second}
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("healthcheck returned a non-200 status")
	}
	return nil
}

func reconcileStoredCredentials(logger *log.Logger, store *identitystore.Store, manager *identity.Manager, channels *cpa.Manager) {
	if store == nil || manager == nil || channels == nil {
		return
	}
	for _, stored := range store.ListForSync() {
		if strings.TrimSpace(stored.ClientKey) == "" {
			logger.Printf("CPA credential reconciliation skipped legacy identity=%s", stored.ID)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		credential, err := manager.Inspect(ctx, stored.Token)
		if err == nil {
			_ = store.UpdateMetadata(stored.ID, identitystore.CredentialMetadata{
				Kind:      string(credential.Kind),
				Email:     credential.Email,
				PlanType:  credential.PlanType,
				ExpiresAt: credential.ExpiresAt,
				FedRAMP:   credential.FedRAMP,
			})
			err = channels.UpsertIdentity(ctx, cpa.Credential{
				IdentityID: stored.ID,
				ClientKey:  stored.ClientKey,
				Kind:       string(credential.Kind),
				AccountID:  credential.AccountID,
				UserID:     credential.UserID,
				Email:      credential.Email,
				PlanType:   credential.PlanType,
				ExpiresAt:  credential.ExpiresAt,
				FedRAMP:    credential.FedRAMP,
			})
		}
		cancel()
		if err != nil {
			logger.Printf("CPA credential reconciliation failed identity=%s", stored.ID)
		}
	}
}

func outboundTransport(rawProxyURL string) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	rawProxyURL = strings.TrimSpace(rawProxyURL)
	if rawProxyURL == "" {
		transport.Proxy = http.ProxyFromEnvironment
		return transport, nil
	}
	if strings.EqualFold(rawProxyURL, "direct") || strings.EqualFold(rawProxyURL, "none") {
		transport.Proxy = nil
		return transport, nil
	}
	proxyURL, err := url.Parse(rawProxyURL)
	if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
		return nil, errors.New("OUTBOUND_PROXY is invalid")
	}
	transport.Proxy = http.ProxyURL(proxyURL)
	return transport, nil
}

func readSecret(valueEnvironment, fileEnvironment string) (string, error) {
	if path := strings.TrimSpace(os.Getenv(fileEnvironment)); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", errors.New("failed to read management key file")
		}
		if secret := strings.TrimSpace(string(data)); secret != "" {
			return secret, nil
		}
		return "", errors.New("management key file is empty")
	}
	if secret := strings.TrimSpace(os.Getenv(valueEnvironment)); secret != "" {
		return secret, nil
	}
	return "", errors.New("management key is required")
}

func readOptionalSecret(valueEnvironment, fileEnvironment string) (string, bool, error) {
	if path := strings.TrimSpace(os.Getenv(fileEnvironment)); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false, errors.New("failed to read secret file")
		}
		if secret := strings.TrimSpace(string(data)); secret != "" {
			return secret, true, nil
		}
		return "", false, errors.New("secret file is empty")
	}
	if secret := strings.TrimSpace(os.Getenv(valueEnvironment)); secret != "" {
		return secret, true, nil
	}
	return "", false, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func int64Env(name string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New(name + " must be a positive integer")
	}
	return value, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New(name + " must be a boolean")
	}
	return value, nil
}
