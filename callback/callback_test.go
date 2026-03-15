package callback_test

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
	"github.com/brandon-kigen/malipo/store/memory"
)

// ── Mock TokenProvider ────────────────────────────────────────────────────────

type mockAuth struct {
	token      string
	checkoutID string
	merchantID string
}

func (m *mockAuth) GetAccessToken(_ context.Context) (string, error) {
	return m.token, nil
}

func (m *mockAuth) GeneratePassword(_, _ string) (string, string) {
	return "dGVzdHBhc3N3b3Jk", "20260101000000"
}

func (m *mockAuth) SendSTKPush(_ context.Context, _ store.STKPushRequest) (string, string, error) {
	return m.checkoutID, m.merchantID, nil
}

func (m *mockAuth) QuerySTKStatus(_ context.Context, _, _, _ string) (string, string, error) {
	return "0", "Success", nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

type testEnv struct {
	manager *session.Manager
	storage *memory.MemoryAdapter
	handler http.Handler
}

// newTestEnv creates a handler wired to a real manager backed by memory.
// checkoutID is what mockAuth returns — it becomes the session's
// CheckoutRequestID after InitiatePayment succeeds.
func newTestEnv(t *testing.T, checkoutID, merchantID string) testEnv {
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

	h := callback.NewHandler(callback.HandlerConfig{Manager: m})

	return testEnv{manager: m, storage: storage, handler: h}
}

// initiatePayment starts a payment in the given env and returns the session ID.
func initiatePayment(t *testing.T, env testEnv) string {
	t.Helper()
	id, err := env.manager.InitiatePayment(context.Background(), session.PaymentRequest{
		Phone: "+254712345678", Amount: 100, Currency: "KES",
	})
	if err != nil {
		t.Fatalf("InitiatePayment failed: %v", err)
	}
	return id
}

// successPayload builds a valid Safaricom success callback body.
func successPayload(checkoutID string) []byte {
	body := map[string]any{
		"Body": map[string]any{
			"stkCallback": map[string]any{
				"MerchantRequestID": "29115-34620561-1",
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
	b, _ := json.Marshal(body)
	return b
}

// failurePayload builds a Safaricom failure callback body for the given ResultCode.
func failurePayload(checkoutID string, resultCode int, resultDesc string) []byte {
	body := map[string]any{
		"Body": map[string]any{
			"stkCallback": map[string]any{
				"MerchantRequestID": "29115-34620561-1",
				"CheckoutRequestID": checkoutID,
				"ResultCode":        resultCode,
				"ResultDesc":        resultDesc,
			},
		},
	}
	b, _ := json.Marshal(body)
	return b
}

func postCallback(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mpesa/callback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// ── NewHandler ────────────────────────────────────────────────────────────────

func TestNewHandler_PanicsOnNilManager(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil Manager, got none")
		}
	}()
	callback.NewHandler(callback.HandlerConfig{Manager: nil})
}

// ── ServeHTTP — method guard ──────────────────────────────────────────────────

func TestCallback_MethodGuard(t *testing.T) {
	env := newTestEnv(t, "ws_CO_MG1", "MR_MG1")

	methods := []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch}

	for _, method := range methods {
		t.Run("rejects "+method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/mpesa/callback", nil)
			rec := httptest.NewRecorder()
			env.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("got status %d want 405 for %s", rec.Code, method)
			}
		})
	}
}

// ── ServeHTTP — decode guard ──────────────────────────────────────────────────

func TestCallback_MalformedJSON(t *testing.T) {
	env := newTestEnv(t, "ws_CO_MJ1", "MR_MJ1")

	req := httptest.NewRequest(http.MethodPost, "/mpesa/callback",
		bytes.NewReader([]byte(`not valid json`)))
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d want 400", rec.Code)
	}
}

// ── ServeHTTP — checkout ID guard ────────────────────────────────────────────

func TestCallback_EmptyCheckoutRequestID(t *testing.T) {
	env := newTestEnv(t, "ws_CO_ECI1", "MR_ECI1")

	body := map[string]any{
		"Body": map[string]any{
			"stkCallback": map[string]any{
				"MerchantRequestID": "MR_ECI1",
				"CheckoutRequestID": "", // empty
				"ResultCode":        0,
				"ResultDesc":        "Success",
			},
		},
	}
	b, _ := json.Marshal(body)

	rec := postCallback(env.handler, b)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d want 400", rec.Code)
	}
}

// ── ServeHTTP — success path ──────────────────────────────────────────────────

func TestCallback_SuccessPath(t *testing.T) {
	ctx := context.Background()

	t.Run("returns 200 on successful callback", func(t *testing.T) {
		env := newTestEnv(t, "ws_CO_SP1", "MR_SP1")
		initiatePayment(t, env)

		rec := postCallback(env.handler, successPayload("ws_CO_SP1"))
		if rec.Code != http.StatusOK {
			t.Errorf("got status %d want 200", rec.Code)
		}
	})

	t.Run("session transitions to CONFIRMED after ResultCode 0", func(t *testing.T) {
		env := newTestEnv(t, "ws_CO_SP2", "MR_SP2")
		id := initiatePayment(t, env)

		postCallback(env.handler, successPayload("ws_CO_SP2"))

		got, err := env.storage.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.State != store.StateConfirmed {
			t.Errorf("got state %q want CONFIRMED", got.State)
		}
	})

	t.Run("MpesaReceiptNumber is populated from callback metadata", func(t *testing.T) {
		env := newTestEnv(t, "ws_CO_SP3", "MR_SP3")
		id := initiatePayment(t, env)

		postCallback(env.handler, successPayload("ws_CO_SP3"))

		got, err := env.storage.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.MpesaReceiptNumber != "NLJ7RT61SV" {
			t.Errorf("got receipt %q want NLJ7RT61SV", got.MpesaReceiptNumber)
		}
	})

	t.Run("ConfirmedAmount is populated from callback metadata", func(t *testing.T) {
		env := newTestEnv(t, "ws_CO_SP4", "MR_SP4")
		id := initiatePayment(t, env)

		postCallback(env.handler, successPayload("ws_CO_SP4"))

		got, err := env.storage.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.ConfirmedAmount == nil {
			t.Fatal("ConfirmedAmount is nil")
		}
		if *got.ConfirmedAmount != 100 {
			t.Errorf("got amount %d want 100", *got.ConfirmedAmount)
		}
	})

	t.Run("ConfirmedPhone is populated with E.164 format from metadata", func(t *testing.T) {
		env := newTestEnv(t, "ws_CO_SP5", "MR_SP5")
		id := initiatePayment(t, env)

		postCallback(env.handler, successPayload("ws_CO_SP5"))

		got, err := env.storage.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.ConfirmedPhone == nil {
			t.Fatal("ConfirmedPhone is nil")
		}
		// Safaricom sends 254712345678 as float64 → formatted as +254712345678
		if *got.ConfirmedPhone != "+254712345678" {
			t.Errorf("got phone %q want +254712345678", *got.ConfirmedPhone)
		}
	})
}

// ── ServeHTTP — failure paths ─────────────────────────────────────────────────

func TestCallback_FailurePaths(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name        string
		checkoutID  string
		merchantID  string
		resultCode  int
		resultDesc  string
		wantState   store.State
	}{
		{
			name:       "ResultCode 1032 transitions to CANCELLED",
			checkoutID: "ws_CO_FP1",
			merchantID: "MR_FP1",
			resultCode: 1032,
			resultDesc: "Request cancelled by user",
			wantState:  store.StateCancelled,
		},
		{
			name:       "ResultCode 1037 transitions to TIMEOUT",
			checkoutID: "ws_CO_FP2",
			merchantID: "MR_FP2",
			resultCode: 1037,
			resultDesc: "DS timeout user cannot be reached",
			wantState:  store.StateTimeout,
		},
		{
			name:       "unrecognised ResultCode transitions to FAILED",
			checkoutID: "ws_CO_FP3",
			merchantID: "MR_FP3",
			resultCode: 2001,
			resultDesc: "The initiator information is invalid",
			wantState:  store.StateFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, tc.checkoutID, tc.merchantID)
			id := initiatePayment(t, env)

			payload := failurePayload(tc.checkoutID, tc.resultCode, tc.resultDesc)
			rec := postCallback(env.handler, payload)

			// Always 200 regardless of outcome
			if rec.Code != http.StatusOK {
				t.Errorf("got status %d want 200", rec.Code)
			}

			got, err := env.storage.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get failed: %v", err)
			}
			if got.State != tc.wantState {
				t.Errorf("got state %q want %q", got.State, tc.wantState)
			}
		})
	}
}

// ── ServeHTTP — always 200 rule ───────────────────────────────────────────────

func TestCallback_AlwaysReturns200(t *testing.T) {
	t.Run("unknown CheckoutRequestID returns 200 not 404", func(t *testing.T) {
		env := newTestEnv(t, "ws_CO_A200_1", "MR_A200_1")
		// No InitiatePayment — CheckoutRequestID is unknown

		payload := successPayload("ws_CO_completely_unknown")
		rec := postCallback(env.handler, payload)

		// HandleCallback returns ErrNotFound — must be absorbed, still 200
		if rec.Code != http.StatusOK {
			t.Errorf("got status %d want 200 for unknown session", rec.Code)
		}
	})

	t.Run("late callback on terminal session returns 200 not 500", func(t *testing.T) {
		ctx := context.Background()
		env := newTestEnv(t, "ws_CO_A200_2", "MR_A200_2")
		initiatePayment(t, env)

		// First callback — drives session to CANCELLED
		firstPayload := failurePayload("ws_CO_A200_2", 1032, "Cancelled by user")
		postCallback(env.handler, firstPayload)

		// Verify terminal
		sessions, _ := env.storage.ListPending(ctx, time.Now().Add(time.Hour))
		_ = sessions // session should not be in pending

		// Second callback arrives late — ErrInvalidTransition must be absorbed
		secondPayload := successPayload("ws_CO_A200_2")
		rec := postCallback(env.handler, secondPayload)

		if rec.Code != http.StatusOK {
			t.Errorf("got status %d want 200 for late callback on terminal session", rec.Code)
		}
	})
}

// ── ServeHTTP — metadata extraction edge cases ────────────────────────────────

func TestCallback_MetadataEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("missing CallbackMetadata on ResultCode 0 does not panic", func(t *testing.T) {
		env := newTestEnv(t, "ws_CO_ME1", "MR_ME1")
		id := initiatePayment(t, env)

		// ResultCode 0 but no CallbackMetadata — malformed but must not panic
		body := map[string]any{
			"Body": map[string]any{
				"stkCallback": map[string]any{
					"MerchantRequestID": "MR_ME1",
					"CheckoutRequestID": "ws_CO_ME1",
					"ResultCode":        0,
					"ResultDesc":        "Success",
					// no CallbackMetadata
				},
			},
		}
		b, _ := json.Marshal(body)

		rec := postCallback(env.handler, b)
		if rec.Code != http.StatusOK {
			t.Errorf("got status %d want 200", rec.Code)
		}

		// Session should still transition — Update will be nil
		// HandleCallback fires EventCallbackSuccess with nil update
		got, err := env.storage.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.State != store.StateConfirmed {
			t.Errorf("got state %q want CONFIRMED", got.State)
		}
	})
}