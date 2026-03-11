package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Environment controls which Daraja API base URL the Manager targets.
// Use Sandbox for development and testing, Production for live payments.
type Environment string

const (
	// Sandbox targets https://sandbox.safaricom.co.ke.
	// Use for development and testing with Daraja test credentials.
	Sandbox Environment = "sandbox"

	// Production targets https://api.safaricom.co.ke.
	// Use for live payments with real Daraja credentials.
	Production Environment = "production"
)

// baseURL returns the Daraja API base URL for the given environment.
func (e Environment) baseURL() string {
	if e == Production {
		return "https://api.safaricom.co.ke"
	}
	return "https://sandbox.safaricom.co.ke"
}

// Config holds the credentials and environment the Manager needs to
// authenticate against the Daraja API. Provided by the caller at
// construction time via malipo.New().
type Config struct {
	// ConsumerKey is the API key from the Safaricom developer portal.
	// Used as the username in Daraja OAuth Basic authentication.
	ConsumerKey string

	// ConsumerSecret is the API secret from the Safaricom developer portal.
	// Used as the password in Daraja OAuth Basic authentication.
	ConsumerSecret string

	// Environment controls which Daraja base URL is targeted.
	// Defaults to Sandbox if not set.
	Environment Environment
}

// Manager handles Daraja OAuth token lifecycle for the session package.
// It fetches a token on first use, caches it until near expiry, then
// fetches a fresh one transparently. Safe for concurrent use.
//
// Manager satisfies session.TokenProvider — it is never imported by
// the session package directly. malipo.New() injects it via the interface.
type Manager struct {
	cfg        Config
	httpClient *http.Client

	// mu protects token and expiresAt.
	// RWMutex is used because reads (cache hits) vastly outnumber
	// writes (token fetches) in normal operation.
	mu        sync.RWMutex
	token     string    // cached Bearer token
	expiresAt time.Time // when the cached token expires
}

// NewManager constructs a Manager with the given credentials.
// Environment defaults to Sandbox if not set.
// token and expiresAt are intentionally left at their zero values —
// the cache is populated on the first call to GetAccessToken.
func NewManager(cfg Config) *Manager {
	if cfg.Environment == "" {
		cfg.Environment = Sandbox
	}

	return &Manager{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// GetAccessToken returns a valid Daraja Bearer token.
// It returns the cached token if it exists and is not near expiry.
// If the cache is empty or stale, it fetches a fresh token from Daraja,
// updates the cache, and returns the new token.
// Safe for concurrent use — RWMutex allows parallel cache hits.
func (m *Manager) GetAccessToken(ctx context.Context) (string, error) {
	// Fast path — read lock allows multiple goroutines to read simultaneously.
	// If the cache is valid, return immediately without touching the write lock.
	m.mu.RLock()
	if m.token != "" && time.Now().Before(m.expiresAt) {
		token := m.token
		m.mu.RUnlock()
		return token, nil
	}
	m.mu.RUnlock()

	// Slow path — cache is empty or stale. Upgrade to write lock.
	// RLock must be released before Lock — Go does not support lock upgrading.
	// There is a window between RUnlock and Lock where another goroutine
	// may have already refreshed the token — the second cache check below
	// catches this and avoids a redundant Daraja call.
	m.mu.Lock()
	defer m.mu.Unlock()

	// Second cache check — another goroutine may have fetched a fresh token
	// while this one was waiting for the write lock.
	if m.token != "" && time.Now().Before(m.expiresAt) {
		return m.token, nil
	}

	// Fetch a fresh token from Daraja.
	token, expiresIn, err := m.fetchToken(ctx)
	if err != nil {
		// Cache is left untouched — next call will retry.
		return "", fmt.Errorf("auth: failed to fetch token: %w", err)
	}

	// Parse expires_in — Daraja returns it as a string, not an integer.
	secs, err := strconv.ParseInt(expiresIn, 10, 64)
	if err != nil {
		return "", fmt.Errorf("auth: invalid expires_in value %q: %w", expiresIn, err)
	}

	// Cache the token with a 119 second buffer before actual expiry.
	// This ensures we refresh before Daraja rejects the token mid-flight.
	m.token = token
	m.expiresAt = time.Now().Add(time.Duration(secs-119) * time.Second)

	return m.token, nil
}

func (m *Manager) GeneratePassword(shortcode, passkey string) (password, timestamp string) {
	loc, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		// timezone database unavailable — import time/tzdata for static binaries
		return "", ""
	}

	now := time.Now().In(loc)
	timestamp = now.Format("20060102150405")
	password = base64.StdEncoding.EncodeToString([]byte(shortcode + passkey + timestamp))

	return password, timestamp
}

// fetchToken makes the outbound HTTP call to the Daraja OAuth endpoint.
// Implemented in daraja.go.
func (m *Manager) fetchToken(ctx context.Context) (token, expiresIn string, err error) {
	return "", "", errors.New("fetchToken: not implemented")
}
