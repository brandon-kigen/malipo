//go:build integration

package malipo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brandon-kigen/malipo/callback"
	"github.com/brandon-kigen/malipo/session"
	"github.com/brandon-kigen/malipo/store"
	"github.com/brandon-kigen/malipo/store/sqlite"
	"github.com/brandon-kigen/malipo/x402"
)

// ── Mock ──────────────────────────────────────────────────────────────────────
//
// integrationAuth is the only non-real component in the integration suite.
// It satisfies session.TokenProvider without touching Daraja. checkoutID is
// fixed at construction so tests can craft matching callback payloads.

type integrationAuth struct {
	checkoutID string
	merchantID string
}

func (m *integrationAuth) GetAccessToken(_ context.Context) (string, error) {
	return "test-bearer-token", nil
}

func (m *integrationAuth) GeneratePassword(_, _ string) (string, string) {
	return "dGVzdHBhc3N3b3Jk", "20260101000000"
}

func (m *integrationAuth) SendSTKPush(_ context.Context, _ store.STKPushRequest) (string, string, error) {
	return m.checkoutID, m.merchantID, nil
}

func (m *integrationAuth) QuerySTKStatus(_ context.Context, _, _, _ string) (string, string, error) {
	// Return "still processing" — recovery loop leaves the session alone
	// during tests; callback handler is the fast path under test here.
	return "500.001.1001", "Request is being processed", nil
}

// ── Test environment ──────────────────────────────────────────────────────────

// integrationEnv holds the fully wired stack for one integration test:
// real SQLite (:memory:) → real session.Manager → real httptest.Server
// with the callback handler and a gated /api/data route mounted.
type integrationEnv struct {
	manager *session.Manager
	srv     *httptest.Server
	auth    *integrationAuth
}

// newIntegrationEnv constructs the full stack.
//
// The CallbackURL in session.Config points to the test server's own address.
// Tests simulate Safaricom by posting callback payloads to that URL directly.
//
// ttl controls session expiry. Pass 0 to use the session package default (90s).
func newIntegrationEnv(t *testing.T, ttl time.Duration) *integrationEnv {
	t.Helper()
	ctx := context.Background()

	if ttl == 0 {
		ttl = 90 * time.Second
	}

	auth := &integrationAuth{
		checkoutID: "ws_CO_INT_001",
		merchantID: "MR_INT_001",
	}

	adapter, err := sqlite.NewSQLiteAdapter(ctx, ":memory:")
	if err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	// Build the mux before starting the server — routes are registered on
	// the same mux pointer the server holds, so they are live immediately.
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	manager := session.NewManager(auth, adapter, session.Config{
		Shortcode:        "174379",
		Passkey:          "testpasskey",
		CallbackURL:      srv.URL + "/mpesa/callback",
		AccountReference: "TestRef",
		TransactionDesc:  "Integration test payment",
		TTL:              ttl,
	})

	mux.Handle("/mpesa/callback", callback.NewHandler(callback.HandlerConfig{
		Manager: manager,
	}))

	gate := x402.Gate(x402.GateOptions{
		Amount:      100,
		Currency:    "KES",
		Description: "Integration test payment",
		Shortcode:   "174379",
		Manager:     manager,
		PhoneExtractor: func(r *http.Request) (string, error) {
			return r.Header.Get("X-Phone"), nil
		},
	})

	mux.Handle("/api/data", gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":"ok"}`))
	})))

	// Stop Manager before closing the DB — same ordering as Shutdown().
	// Closing the DB first risks the cleanup ticker firing between the two calls.
	t.Cleanup(func() {
		manager.Stop()
		adapter.Close()
		srv.Close()
	})

	return &integrationEnv{
		manager: manager,
		srv:     srv,
		auth:    auth,
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// requestData makes a GET /api/data request.
// If sessionID is non-empty it is set as X-PAYMENT. X-Phone is always set.
func (e *integrationEnv) requestData(t *testing.T, sessionID string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, e.srv.URL+"/api/data", nil)
	if err != nil {
		t.Fatalf("request build failed: %v", err)
	}
	req.Header.Set("X-Phone", "+254712345678")
	if sessionID != "" {
		req.Header.Set("X-PAYMENT", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

// postCallback posts a Safaricom-shaped callback payload to the test server.
// resultCode 0 produces a success payload with metadata; any other value
// produces a failure payload without metadata.
func (e *integrationEnv) postCallback(t *testing.T, checkoutID string, resultCode int) *http.Response {
	t.Helper()

	var body map[string]any
	if resultCode == 0 {
		body = map[string]any{
			"Body": map[string]any{
				"stkCallback": map[string]any{
					"MerchantRequestID": e.auth.merchantID,
					"CheckoutRequestID": checkoutID,
					"ResultCode":        0,
					"ResultDesc":        "The service request is processed successfully.",
					"CallbackMetadata": map[string]any{
						"Item": []map[string]any{
							{"Name": "Amount", "Value": 100},
							{"Name": "MpesaReceiptNumber", "Value": "NLJ7RT61SV"},
							{"Name": "TransactionDate", "Value": 20260315143022},
							{"Name": "PhoneNumber", "Value": 254712345678},
						},
					},
				},
			},
		}
	} else {
		body = map[string]any{
			"Body": map[string]any{
				"stkCallback": map[string]any{
					"MerchantRequestID": e.auth.merchantID,
					"CheckoutRequestID": checkoutID,
					"ResultCode":        resultCode,
					"ResultDesc":        "Request cancelled by user",
				},
			},
		}
	}

	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/mpesa/callback", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("callback request build failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	return resp
}

// extractSessionID decodes the x402 Response402 body and returns the sessionId.
// Closes resp.Body.
func extractSessionID(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()

	var body x402.Response402
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode 402 body failed: %v", err)
	}
	if len(body.Accepts) == 0 {
		t.Fatal("402 body has no accepts entries")
	}
	id := body.Accepts[0].SessionID
	if id == "" {
		t.Fatal("sessionId is empty in 402 body")
	}
	return id
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestIntegration_HappyPath exercises the complete payment lifecycle:
//
//	GET /api/data (no token)          → 402, session created in SQLite
//	POST /mpesa/callback (success)    → session transitions to CONFIRMED
//	GET /api/data (X-PAYMENT: id)     → 200, session transitions to CONSUMED
func TestIntegration_HappyPath(t *testing.T) {
	env := newIntegrationEnv(t, 0)
	ctx := context.Background()

	// Step 1 — unauthenticated request
	resp1 := env.requestData(t, "")
	if resp1.StatusCode != http.StatusPaymentRequired {
		resp1.Body.Close()
		t.Fatalf("step 1: got status %d want 402", resp1.StatusCode)
	}
	sessionID := extractSessionID(t, resp1) // closes resp1.Body

	// Step 2 — fake Safaricom callback with the checkout ID mockAuth assigned
	cbResp := env.postCallback(t, env.auth.checkoutID, 0)
	cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusOK {
		t.Fatalf("step 2: callback got status %d want 200", cbResp.StatusCode)
	}

	// Step 3 — authenticated retry
	resp3 := env.requestData(t, sessionID)
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("step 3: got status %d want 200", resp3.StatusCode)
	}

	// Step 4 — verify final state in storage
	state, _, err := env.manager.GetStatus(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if state != "CONSUMED" {
		t.Errorf("got state %q want CONSUMED", state)
	}
}

// TestIntegration_DoubleSpend verifies that a consumed session cannot unlock
// a resource a second time.
func TestIntegration_DoubleSpend(t *testing.T) {
	env := newIntegrationEnv(t, 0)
	ctx := context.Background()

	// Drive session all the way to CONSUMED
	resp1 := env.requestData(t, "")
	if resp1.StatusCode != http.StatusPaymentRequired {
		resp1.Body.Close()
		t.Fatalf("setup: got status %d want 402", resp1.StatusCode)
	}
	sessionID := extractSessionID(t, resp1)

	cbResp := env.postCallback(t, env.auth.checkoutID, 0)
	cbResp.Body.Close()

	resp2 := env.requestData(t, sessionID)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("first gate pass: got status %d want 200", resp2.StatusCode)
	}

	// Second attempt with the same session ID — must be rejected
	resp3 := env.requestData(t, sessionID)
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusPaymentRequired {
		t.Errorf("double-spend: got status %d want 402", resp3.StatusCode)
	}

	// Session must remain CONSUMED — the rejected attempt must not mutate it
	state, _, err := env.manager.GetStatus(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if state != "CONSUMED" {
		t.Errorf("got state %q want CONSUMED after double-spend attempt", state)
	}
}

// TestIntegration_TTLExpiry verifies that a session whose TTL elapses before
// the user enters their PIN cannot be used to unlock a resource.
func TestIntegration_TTLExpiry(t *testing.T) {
	// 100ms TTL — expireAfter goroutine fires well within the 300ms sleep below
	env := newIntegrationEnv(t, 100*time.Millisecond)
	ctx := context.Background()

	// Step 1 — request without payment, session created
	resp1 := env.requestData(t, "")
	if resp1.StatusCode != http.StatusPaymentRequired {
		resp1.Body.Close()
		t.Fatalf("step 1: got status %d want 402", resp1.StatusCode)
	}
	sessionID := extractSessionID(t, resp1)

	// Step 2 — wait for expireAfter goroutine to fire (TTL = 100ms, sleep = 3×)
	time.Sleep(300 * time.Millisecond)

	// Step 3 — retry with expired session — Gate reads TIMEOUT, returns 402
	resp3 := env.requestData(t, sessionID)
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusPaymentRequired {
		t.Errorf("step 3: got status %d want 402 — session should have expired", resp3.StatusCode)
	}

	// Step 4 — verify final state
	state, _, err := env.manager.GetStatus(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if state != "TIMEOUT" {
		t.Errorf("got state %q want TIMEOUT", state)
	}
}
