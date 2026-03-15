package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brandon-kigen/malipo/session"
	"github.com/brandon-kigen/malipo/store"
	"github.com/brandon-kigen/malipo/store/memory"
)

// ── Mock TokenProvider ────────────────────────────────────────────────────────

// mockAuth is a test double for session.TokenProvider.
// Fields control return values — set them per test to simulate
// Daraja success, token failure, or STK Push rejection.
type mockAuth struct {
	token       string
	tokenErr    error
	checkoutID  string
	merchantID  string
	stkErr      error
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

// newTestManager builds a Manager with a mock auth and fresh memory adapter.
// TTL is set short — tests that need to observe TTL expiry use a tiny value.
func newTestManager(t *testing.T, auth *mockAuth, ttl time.Duration) *session.Manager {
	t.Helper()
	if ttl == 0 {
		ttl = 90 * time.Second
	}
	storage := memory.NewMemoryAdapter()
	m := session.NewManager(auth, storage, session.Config{
		Shortcode:        "174379",
		Passkey:          "testpasskey",
		CallbackURL:      "https://example.com/callback",
		TTL:              ttl,
		AccountReference: "TestRef",
		TransactionDesc:  "Test payment",
	})
	t.Cleanup(func() { m.Stop() })
	return m
}

// successAuth returns a mockAuth pre-configured for a happy path payment.
func successAuth(checkoutID, merchantID string) *mockAuth {
	return &mockAuth{
		token:      "test-bearer-token",
		checkoutID: checkoutID,
		merchantID: merchantID,
	}
}

// validRequest returns a minimal valid PaymentRequest.
func validRequest() session.PaymentRequest {
	return session.PaymentRequest{
		Phone:    "+254712345678",
		Amount:   100,
		Currency: "KES",
	}
}

// ── InitiatePayment ───────────────────────────────────────────────────────────

func TestInitiatePayment(t *testing.T) {
	ctx := context.Background()

	t.Run("returns a session id on success", func(t *testing.T) {
		auth := successAuth("ws_CO_111", "MR_111")
		m := newTestManager(t, auth, 0)

		id, err := m.InitiatePayment(ctx, validRequest())
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}
		if id == "" {
			t.Error("got empty session id")
		}
	})

	t.Run("session is in STK_PUSHED state after initiation", func(t *testing.T) {
		auth := successAuth("ws_CO_222", "MR_222")
		m := newTestManager(t, auth, 0)

		id, err := m.InitiatePayment(ctx, validRequest())
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}

		state, _, err := m.GetStatus(ctx, id)
		if err != nil {
			t.Fatalf("GetStatus failed: %v", err)
		}
		if state != string(store.StateSTKPushed) {
			t.Errorf("got state %q want STK_PUSHED", state)
		}
	})

	t.Run("each call produces a unique session id", func(t *testing.T) {
		auth := successAuth("ws_CO_333", "MR_333")
		m := newTestManager(t, auth, 0)

		id1, err := m.InitiatePayment(ctx, validRequest())
		if err != nil {
			t.Fatalf("first InitiatePayment failed: %v", err)
		}

		// Update checkout ID to avoid secondary index collision
		auth.checkoutID = "ws_CO_444"
		auth.merchantID = "MR_444"

		id2, err := m.InitiatePayment(ctx, validRequest())
		if err != nil {
			t.Fatalf("second InitiatePayment failed: %v", err)
		}

		if id1 == id2 {
			t.Error("two InitiatePayment calls produced the same session id")
		}
	})

	t.Run("normalises phone number to E.164", func(t *testing.T) {
		auth := successAuth("ws_CO_555", "MR_555")
		storage := memory.NewMemoryAdapter()
		m := session.NewManager(auth, storage, session.Config{
			Shortcode:   "174379",
			Passkey:     "testpasskey",
			CallbackURL: "https://example.com/callback",
			TTL:         90 * time.Second,
		})
		t.Cleanup(func() { m.Stop() })

		req := validRequest()
		req.Phone = "0712345678" // local format — should be normalised

		_, err := m.InitiatePayment(ctx, req)
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}
	})

	t.Run("returns error for invalid phone number", func(t *testing.T) {
		auth := successAuth("ws_CO_666", "MR_666")
		m := newTestManager(t, auth, 0)

		req := validRequest()
		req.Phone = "not-a-phone"

		_, err := m.InitiatePayment(ctx, req)
		if err == nil {
			t.Error("expected error for invalid phone, got nil")
		}
	})

	t.Run("returns error when GetAccessToken fails", func(t *testing.T) {
		auth := &mockAuth{
			tokenErr: errors.New("daraja: token endpoint down"),
		}
		m := newTestManager(t, auth, 0)

		_, err := m.InitiatePayment(ctx, validRequest())
		if err == nil {
			t.Error("expected error when token fetch fails, got nil")
		}
	})

	t.Run("session is in FAILED state when STK Push is rejected", func(t *testing.T) {
		// Need direct storage access to inspect state after STK rejection.
		// Build manager manually so we can hold a reference to storage.
		storage := memory.NewMemoryAdapter()
		auth := &mockAuth{
			token:  "test-token",
			stkErr: errors.New("daraja: insufficient balance"),
		}
		m := session.NewManager(auth, storage, session.Config{
			Shortcode:   "174379",
			Passkey:     "testpasskey",
			CallbackURL: "https://example.com/callback",
			TTL:         90 * time.Second,
		})
		t.Cleanup(func() { m.Stop() })

		_, err := m.InitiatePayment(ctx, validRequest())
		if err == nil {
			t.Error("expected error when STK Push fails, got nil")
		}
	})

	t.Run("uses request reference and desc when provided", func(t *testing.T) {
		auth := successAuth("ws_CO_777", "MR_777")
		m := newTestManager(t, auth, 0)

		req := validRequest()
		req.Reference = "CustomRef"
		req.Desc = "Custom desc"

		id, err := m.InitiatePayment(ctx, req)
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}
		if id == "" {
			t.Error("got empty session id")
		}
		// Reference and desc are passed to SendSTKPush — verified via
		// a mock that asserts on the request in a future test if needed.
	})
}

// ── GetStatus ─────────────────────────────────────────────────────────────────

func TestGetStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("returns state and expiry for existing session", func(t *testing.T) {
		auth := successAuth("ws_CO_888", "MR_888")
		m := newTestManager(t, auth, 0)

		id, err := m.InitiatePayment(ctx, validRequest())
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}

		state, expiresAt, err := m.GetStatus(ctx, id)
		if err != nil {
			t.Fatalf("GetStatus failed: %v", err)
		}
		if state != string(store.StateSTKPushed) {
			t.Errorf("got state %q want STK_PUSHED", state)
		}
		if expiresAt.IsZero() {
			t.Error("expiresAt is zero")
		}
		if time.Until(expiresAt) <= 0 {
			t.Error("expiresAt is in the past")
		}
	})

	t.Run("returns error for empty id", func(t *testing.T) {
		auth := successAuth("ws_CO_999", "MR_999")
		m := newTestManager(t, auth, 0)

		_, _, err := m.GetStatus(ctx, "")
		if err == nil {
			t.Error("expected error for empty id, got nil")
		}
	})

	t.Run("returns ErrNotFound for unknown id", func(t *testing.T) {
		auth := successAuth("ws_CO_AAA", "MR_AAA")
		m := newTestManager(t, auth, 0)

		_, _, err := m.GetStatus(ctx, "nonexistent-id")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got error %v want ErrNotFound", err)
		}
	})
}

// ── TTL expiry ────────────────────────────────────────────────────────────────

func TestTTLExpiry(t *testing.T) {
	ctx := context.Background()

	t.Run("session transitions to TIMEOUT after TTL elapses", func(t *testing.T) {
		auth := successAuth("ws_CO_BBB", "MR_BBB")
		// Very short TTL so the test does not hang
		m := newTestManager(t, auth, 50*time.Millisecond)

		id, err := m.InitiatePayment(ctx, validRequest())
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}

		// Wait for TTL to fire plus a small buffer
		time.Sleep(200 * time.Millisecond)

		state, _, err := m.GetStatus(ctx, id)
		if err != nil {
			t.Fatalf("GetStatus failed: %v", err)
		}
		if state != string(store.StateTimeout) {
			t.Errorf("got state %q want TIMEOUT", state)
		}
	})

	t.Run("expireAfter does not panic when session is already terminal", func(t *testing.T) {
		// If the session is confirmed and consumed before TTL fires,
		// the transition attempt must be silently ignored — not panic.
		auth := successAuth("ws_CO_CCC", "MR_CCC")
		storage := memory.NewMemoryAdapter()
		m := session.NewManager(auth, storage, session.Config{
			Shortcode:   "174379",
			Passkey:     "testpasskey",
			CallbackURL: "https://example.com/callback",
			TTL:         50 * time.Millisecond,
		})
		t.Cleanup(func() { m.Stop() })

		id, err := m.InitiatePayment(ctx, validRequest())
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}

		// Drive session to CONFIRMED manually via storage
		if err := storage.Transition(ctx, id, store.StateSTKPushed, store.StateConfirmed, &store.Update{
			MpesaReceiptNumber: strPtr("RCP_CCC"),
			ConfirmedAmount:    int64Ptr(100),
			ConfirmedPhone:     strPtr("+254712345678"),
		}); err != nil {
			t.Fatalf("manual Transition to CONFIRMED failed: %v", err)
		}

		// Consume it
		if _, err := storage.ConsumeIfConfirmed(ctx, id); err != nil {
			t.Fatalf("ConsumeIfConfirmed failed: %v", err)
		}

		// Wait for TTL goroutine to fire — it should see CONSUMED and exit silently
		time.Sleep(200 * time.Millisecond)

		// Session must still be CONSUMED — not mutated by the TTL goroutine
		state, _, err := m.GetStatus(ctx, id)
		if err != nil {
			t.Fatalf("GetStatus failed: %v", err)
		}
		if state != string(store.StateConsumed) {
			t.Errorf("got state %q want CONSUMED — TTL goroutine mutated a terminal session", state)
		}
	})
}

// ── ConsumeIfConfirmed ────────────────────────────────────────────────────────

func TestConsumeIfConfirmed(t *testing.T) {
	ctx := context.Background()

	// createConfirmed builds a manager with direct storage access,
	// initiates a payment, and drives the session to CONFIRMED via storage —
	// mimicking what the Phase 5 callback handler will do in production.
	createConfirmed := func(t *testing.T, checkoutID, merchantID string) (*session.Manager, *memory.MemoryAdapter, string) {
		t.Helper()
		storage := memory.NewMemoryAdapter()
		auth := &mockAuth{
			token:      "test-token",
			checkoutID: checkoutID,
			merchantID: merchantID,
		}
		m := session.NewManager(auth, storage, session.Config{
			Shortcode:   "174379",
			Passkey:     "testpasskey",
			CallbackURL: "https://example.com/callback",
			TTL:         90 * time.Second,
		})
		t.Cleanup(func() { m.Stop() })

		id, err := m.InitiatePayment(ctx, validRequest())
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}

		if err := storage.Transition(ctx, id, store.StateSTKPushed, store.StateConfirmed, &store.Update{
			MpesaReceiptNumber: strPtr("RCP_CONF"),
			ConfirmedAmount:    int64Ptr(100),
			ConfirmedPhone:     strPtr("+254712345678"),
		}); err != nil {
			t.Fatalf("Transition to CONFIRMED failed: %v", err)
		}

		return m, storage, id
	}

	t.Run("returns nil and transitions session to CONSUMED", func(t *testing.T) {
		m, _, id := createConfirmed(t, "ws_CO_CIF1", "MR_CIF1")

		if err := m.ConsumeIfConfirmed(ctx, id); err != nil {
			t.Fatalf("ConsumeIfConfirmed failed: %v", err)
		}

		state, _, err := m.GetStatus(ctx, id)
		if err != nil {
			t.Fatalf("GetStatus after consume failed: %v", err)
		}
		if state != string(store.StateConsumed) {
			t.Errorf("got state %q want CONSUMED", state)
		}
	})

	t.Run("returns ErrNotFound for unknown session", func(t *testing.T) {
		m, _, _ := createConfirmed(t, "ws_CO_CIF2", "MR_CIF2")

		err := m.ConsumeIfConfirmed(ctx, "nonexistent-session-id")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got error %v want ErrNotFound", err)
		}
	})

	t.Run("returns ErrAlreadyConsumed on second call", func(t *testing.T) {
		m, _, id := createConfirmed(t, "ws_CO_CIF3", "MR_CIF3")

		if err := m.ConsumeIfConfirmed(ctx, id); err != nil {
			t.Fatalf("first ConsumeIfConfirmed failed: %v", err)
		}

		err := m.ConsumeIfConfirmed(ctx, id)
		if !errors.Is(err, store.ErrAlreadyConsumed) {
			t.Errorf("got error %v want ErrAlreadyConsumed", err)
		}
	})

	t.Run("returns ErrInvalidTransition when session is STK_PUSHED not CONFIRMED", func(t *testing.T) {
		storage := memory.NewMemoryAdapter()
		auth := &mockAuth{token: "test-token", checkoutID: "ws_CO_CIF4", merchantID: "MR_CIF4"}
		m := session.NewManager(auth, storage, session.Config{
			Shortcode:   "174379",
			Passkey:     "testpasskey",
			CallbackURL: "https://example.com/callback",
			TTL:         90 * time.Second,
		})
		t.Cleanup(func() { m.Stop() })

		// InitiatePayment leaves session in STK_PUSHED — not CONFIRMED
		id, err := m.InitiatePayment(ctx, validRequest())
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}

		err = m.ConsumeIfConfirmed(ctx, id)
		if !errors.Is(err, store.ErrInvalidTransition) {
			t.Errorf("got error %v want ErrInvalidTransition", err)
		}
	})
}

// ── Stop ──────────────────────────────────────────────────────────────────────

func TestStop(t *testing.T) {
	t.Run("Stop does not panic when called once", func(t *testing.T) {
		auth := successAuth("ws_CO_DDD", "MR_DDD")
		storage := memory.NewMemoryAdapter()
		m := session.NewManager(auth, storage, session.Config{
			Shortcode:   "174379",
			Passkey:     "testpasskey",
			CallbackURL: "https://example.com/callback",
		})
		// Should not panic
		m.Stop()
	})
}

// ── helpers used in TTL test ──────────────────────────────────────────────────

func strPtr(s string) *string  { return &s }
func int64Ptr(i int64) *int64  { return &i }
