package x402_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brandon-kigen/malipo/session"
	"github.com/brandon-kigen/malipo/store"
	"github.com/brandon-kigen/malipo/store/memory"
	"github.com/brandon-kigen/malipo/x402"
)

// ── Mock TokenProvider ────────────────────────────────────────────────────────

// mockAuth is a test double for session.TokenProvider.
// Fields control return values — set per test to simulate Daraja responses.
type mockAuth struct {
	token      string
	tokenErr   error
	checkoutID string
	merchantID string
	stkErr     error
}

func (m *mockAuth) GetAccessToken(_ context.Context) (string, error) {
	return m.token, m.tokenErr
}

func (m *mockAuth) GeneratePassword(_, _ string) (password, timestamp string) {
	return "dGVzdHBhc3N3b3Jk", "20260101000000"
}

func (m *mockAuth) SendSTKPush(_ context.Context, _ store.STKPushRequest) (string, string, error) {
	if m.stkErr != nil {
		return "", "", m.stkErr
	}
	return m.checkoutID, m.merchantID, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// managedStorage pairs a Manager with its underlying MemoryAdapter.
// Branch 1 tests need direct storage access to drive sessions to CONFIRMED —
// mimicking what the Phase 5 callback handler will do in production.
type managedStorage struct {
	manager *session.Manager
	storage *memory.MemoryAdapter
}

func newTestManager(t *testing.T, auth *mockAuth) managedStorage {
	t.Helper()
	storage := memory.NewMemoryAdapter()
	m := session.NewManager(auth, storage, session.Config{
		Shortcode:        "174379",
		Passkey:          "testpasskey",
		CallbackURL:      "https://example.com/callback",
		TTL:              90 * time.Second,
		AccountReference: "TestRef",
		TransactionDesc:  "Test payment",
	})
	t.Cleanup(func() { m.Stop() })
	return managedStorage{manager: m, storage: storage}
}

func successAuth(checkoutID, merchantID string) *mockAuth {
	return &mockAuth{
		token:      "test-bearer-token",
		checkoutID: checkoutID,
		merchantID: merchantID,
	}
}

// confirmSession drives a session from STK_PUSHED to CONFIRMED via direct
// storage access. Called in Branch 1 tests to simulate a Safaricom callback.
func confirmSession(t *testing.T, storage *memory.MemoryAdapter, sessionID string) {
	t.Helper()
	if err := storage.Transition(
		context.Background(),
		sessionID,
		store.StateSTKPushed,
		store.StateConfirmed,
		&store.Update{
			MpesaReceiptNumber: strPtr("RCP_TEST"),
			ConfirmedAmount:    int64Ptr(100),
			ConfirmedPhone:     strPtr("+254712345678"),
		},
	); err != nil {
		t.Fatalf("confirmSession: %v", err)
	}
}

// defaultOpts returns a minimal valid GateOptions for testing.
func defaultOpts(m *session.Manager) x402.GateOptions {
	return x402.GateOptions{
		Amount:         100,
		Currency:       "KES",
		Description:    "Test payment",
		Shortcode:      "174379",
		Manager:        m,
		PhoneExtractor: func(r *http.Request) (string, error) { return "+254712345678", nil },
	}
}

// nextOK is a handler that records whether it was called.
func nextOK(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

func strPtr(s string) *string { return &s }
func int64Ptr(i int64) *int64 { return &i }

// ── Write402 ──────────────────────────────────────────────────────────────────

func TestWrite402(t *testing.T) {
	reqs := x402.PaymentRequirements{
		Scheme:      "mpesa",
		Network:     "safaricom-ke",
		Amount:      100,
		Currency:    "KES",
		Resource:    "https://example.com/api/data",
		Description: "Test payment",
		PayTo:       "174379",
		SessionID:   "test-session-id",
		RetryAfter:  5,
	}

	t.Run("writes 402 status code", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := x402.Write402(rec, reqs); err != nil {
			t.Fatalf("Write402 returned error: %v", err)
		}
		if rec.Code != http.StatusPaymentRequired {
			t.Errorf("got status %d want 402", rec.Code)
		}
	})

	t.Run("sets Content-Type to application/json", func(t *testing.T) {
		rec := httptest.NewRecorder()
		x402.Write402(rec, reqs)
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("got Content-Type %q want application/json", ct)
		}
	})

	t.Run("body is valid JSON decoding to Response402", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := x402.Write402(rec, reqs); err != nil {
			t.Fatalf("Write402 failed: %v", err)
		}

		var resp x402.Response402
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode failed: %v", err)
		}

		if resp.Error != "Payment required" {
			t.Errorf("got Error %q want \"Payment required\"", resp.Error)
		}
		if len(resp.Accepts) != 1 {
			t.Fatalf("got %d Accepts want 1", len(resp.Accepts))
		}

		got := resp.Accepts[0]
		if got.Scheme != reqs.Scheme {
			t.Errorf("Scheme: got %q want %q", got.Scheme, reqs.Scheme)
		}
		if got.Network != reqs.Network {
			t.Errorf("Network: got %q want %q", got.Network, reqs.Network)
		}
		if got.Amount != reqs.Amount {
			t.Errorf("Amount: got %d want %d", got.Amount, reqs.Amount)
		}
		if got.Currency != reqs.Currency {
			t.Errorf("Currency: got %q want %q", got.Currency, reqs.Currency)
		}
		if got.SessionID != reqs.SessionID {
			t.Errorf("SessionID: got %q want %q", got.SessionID, reqs.SessionID)
		}
		if got.RetryAfter != reqs.RetryAfter {
			t.Errorf("RetryAfter: got %d want %d", got.RetryAfter, reqs.RetryAfter)
		}
	})

	t.Run("returns nil on success", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := x402.Write402(rec, reqs); err != nil {
			t.Errorf("expected nil error, got: %v", err)
		}
	})
}

// ── Gate — startup validation ─────────────────────────────────────────────────

func TestGate_PanicsOnNilManager(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil Manager, got none")
		}
	}()
	x402.Gate(x402.GateOptions{
		PhoneExtractor: func(r *http.Request) (string, error) { return "+254712345678", nil },
	})
}

func TestGate_PanicsOnNilPhoneExtractor(t *testing.T) {
	ms := newTestManager(t, successAuth("ws_CO_P1", "MR_P1"))
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil PhoneExtractor, got none")
		}
	}()
	x402.Gate(x402.GateOptions{
		Manager: ms.manager,
	})
}

// ── Gate — Branch 1: X-PAYMENT header present ────────────────────────────────

func TestGate_Branch1(t *testing.T) {
	ctx := context.Background()

	// initiateAndConfirm starts a payment and drives it to CONFIRMED via direct
	// storage access — bypassing the callback handler not yet built in Phase 5.
	initiateAndConfirm := func(t *testing.T, ms managedStorage) string {
		t.Helper()
		id, err := ms.manager.InitiatePayment(ctx, session.PaymentRequest{
			Phone: "+254712345678", Amount: 100, Currency: "KES",
		})
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}
		confirmSession(t, ms.storage, id)
		return id
	}

	t.Run("CONFIRMED session passes through to next handler", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B1A", "MR_B1A"))
		sessionID := initiateAndConfirm(t, ms)

		called := false
		handler := x402.Gate(defaultOpts(ms.manager))(nextOK(&called))

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set(x402.PaymentHeader, sessionID)
		handler.ServeHTTP(httptest.NewRecorder(), req)

		if !called {
			t.Error("next handler was not called for a valid confirmed session")
		}
	})

	t.Run("session is CONSUMED after successful gate pass", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B1B", "MR_B1B"))
		sessionID := initiateAndConfirm(t, ms)

		handler := x402.Gate(defaultOpts(ms.manager))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set(x402.PaymentHeader, sessionID)
		handler.ServeHTTP(httptest.NewRecorder(), req)

		state, _, err := ms.manager.GetStatus(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetStatus failed: %v", err)
		}
		if state != string(store.StateConsumed) {
			t.Errorf("got state %q want CONSUMED — ConsumeIfConfirmed was not called", state)
		}
	})

	t.Run("double-spend attempt returns 402", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B1C", "MR_B1C"))
		sessionID := initiateAndConfirm(t, ms)

		handler := x402.Gate(defaultOpts(ms.manager))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

		// First request — valid, consumes the session
		req1 := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req1.Header.Set(x402.PaymentHeader, sessionID)
		handler.ServeHTTP(httptest.NewRecorder(), req1)

		// Second request with the same session ID — double-spend attempt
		req2 := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req2.Header.Set(x402.PaymentHeader, sessionID)
		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req2)

		if rec2.Code != http.StatusPaymentRequired {
			t.Errorf("got status %d want 402 on double-spend attempt", rec2.Code)
		}
	})

	t.Run("non-existent session ID returns 402", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B1D", "MR_B1D"))
		handler := x402.Gate(defaultOpts(ms.manager))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set(x402.PaymentHeader, "completely-nonexistent-session-id")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusPaymentRequired {
			t.Errorf("got status %d want 402 for unknown session", rec.Code)
		}
	})

	t.Run("STK_PUSHED session (not yet confirmed) returns 402", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B1E", "MR_B1E"))

		// InitiatePayment leaves session in STK_PUSHED — no confirmSession call
		id, err := ms.manager.InitiatePayment(ctx, session.PaymentRequest{
			Phone: "+254712345678", Amount: 100, Currency: "KES",
		})
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}

		handler := x402.Gate(defaultOpts(ms.manager))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set(x402.PaymentHeader, id)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusPaymentRequired {
			t.Errorf("got status %d want 402 for unconfirmed session", rec.Code)
		}
	})
}

// ── Gate — Branch 2: phone extraction fails ───────────────────────────────────

func TestGate_Branch2(t *testing.T) {
	t.Run("PhoneExtractor error returns 400", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B2A", "MR_B2A"))

		opts := defaultOpts(ms.manager)
		opts.PhoneExtractor = func(r *http.Request) (string, error) {
			return "", errors.New("phone not available")
		}

		handler := x402.Gate(opts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		// No X-PAYMENT header — falls through to PhoneExtractor
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("got status %d want 400", rec.Code)
		}
	})

	t.Run("400 body contains the extractor error message", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B2B", "MR_B2B"))

		const errMsg = "phone number required in X-Phone header"
		opts := defaultOpts(ms.manager)
		opts.PhoneExtractor = func(r *http.Request) (string, error) {
			return "", errors.New(errMsg)
		}

		handler := x402.Gate(opts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if !strings.Contains(rec.Body.String(), errMsg) {
			t.Errorf("response body %q does not contain error message %q", rec.Body.String(), errMsg)
		}
	})
}

// ── Gate — Branch 3: initiate payment ────────────────────────────────────────

func TestGate_Branch3(t *testing.T) {
	t.Run("returns 402 with x402-compliant body", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B3A", "MR_B3A"))

		handler := x402.Gate(defaultOpts(ms.manager))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusPaymentRequired {
			t.Fatalf("got status %d want 402", rec.Code)
		}

		var resp x402.Response402
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode failed: %v", err)
		}

		if len(resp.Accepts) != 1 {
			t.Fatalf("got %d Accepts want 1", len(resp.Accepts))
		}
		got := resp.Accepts[0]

		if got.Scheme != x402.SchemeName {
			t.Errorf("Scheme: got %q want %q", got.Scheme, x402.SchemeName)
		}
		if got.Network != x402.Network {
			t.Errorf("Network: got %q want %q", got.Network, x402.Network)
		}
		if got.Currency != "KES" {
			t.Errorf("Currency: got %q want KES", got.Currency)
		}
		if got.Amount != 100 {
			t.Errorf("Amount: got %d want 100", got.Amount)
		}
		if got.PayTo != "174379" {
			t.Errorf("PayTo: got %q want 174379", got.PayTo)
		}
		if got.SessionID == "" {
			t.Error("SessionID is empty — InitiatePayment session ID must appear in body")
		}
		if got.RetryAfter != 5 {
			t.Errorf("RetryAfter: got %d want 5", got.RetryAfter)
		}
		if resp.Error != "Payment required" {
			t.Errorf("Error: got %q want \"Payment required\"", resp.Error)
		}
	})

	t.Run("defaults Currency to KES when GateOptions.Currency is empty", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B3B", "MR_B3B"))

		opts := defaultOpts(ms.manager)
		opts.Currency = "" // deliberately empty — Gate should default to KES

		handler := x402.Gate(opts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		var resp x402.Response402
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if len(resp.Accepts) == 0 {
			t.Fatal("empty Accepts")
		}
		if resp.Accepts[0].Currency != "KES" {
			t.Errorf("Currency: got %q want KES", resp.Accepts[0].Currency)
		}
	})

	t.Run("resource URL in body reflects the actual request URI", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B3C", "MR_B3C"))

		handler := x402.Gate(defaultOpts(ms.manager))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/data?format=json", nil)
		req.Host = "example.com"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		var resp x402.Response402
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if len(resp.Accepts) == 0 {
			t.Fatal("empty Accepts")
		}
		resource := resp.Accepts[0].Resource
		if !strings.Contains(resource, "/api/v1/data?format=json") {
			t.Errorf("Resource %q does not contain the request URI", resource)
		}
	})

	t.Run("returns 500 when InitiatePayment fails", func(t *testing.T) {
		auth := &mockAuth{
			tokenErr: errors.New("daraja: auth service unavailable"),
		}
		ms := newTestManager(t, auth)

		handler := x402.Gate(defaultOpts(ms.manager))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("got status %d want 500", rec.Code)
		}
	})

	t.Run("next handler is never called in branch 3", func(t *testing.T) {
		ms := newTestManager(t, successAuth("ws_CO_B3D", "MR_B3D"))

		called := false
		handler := x402.Gate(defaultOpts(ms.manager))(nextOK(&called))

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)

		if called {
			t.Error("next handler was called — Gate must not pass through without payment")
		}
	})
}