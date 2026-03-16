package malipo_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/brandon-kigen/malipo"
)

// minimalConfig returns a valid Config with :memory: storage.
// All required fields populated, all optional fields at zero value.
// Use this as the base for every test — mutate a copy when you need a
// specific field missing or changed.
func minimalConfig() malipo.Config {
	return malipo.Config{
		ConsumerKey:    "test-consumer-key",
		ConsumerSecret: "test-consumer-secret",
		Shortcode:      "174379",
		Passkey:        "test-passkey",
		CallbackURL:    "https://example.com/mpesa/callback",
		DBPath:         ":memory:",
	}
}

// ── New — required field validation ──────────────────────────────────────────

func TestNew_RequiredFields(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name   string
		mutate func(*malipo.Config)
		want   string
	}{
		{
			name:   "missing ConsumerKey",
			mutate: func(c *malipo.Config) { c.ConsumerKey = "" },
			want:   "malipo: ConsumerKey is required",
		},
		{
			name:   "missing ConsumerSecret",
			mutate: func(c *malipo.Config) { c.ConsumerSecret = "" },
			want:   "malipo: ConsumerSecret is required",
		},
		{
			name:   "missing Shortcode",
			mutate: func(c *malipo.Config) { c.Shortcode = "" },
			want:   "malipo: Shortcode is required",
		},
		{
			name:   "missing Passkey",
			mutate: func(c *malipo.Config) { c.Passkey = "" },
			want:   "malipo: Passkey is required",
		},
		{
			name:   "missing CallbackURL",
			mutate: func(c *malipo.Config) { c.CallbackURL = "" },
			want:   "malipo: CallbackURL is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := minimalConfig()
			tc.mutate(&cfg)

			_, err := malipo.New(ctx, cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if err.Error() != tc.want {
				t.Errorf("got error %q want %q", err.Error(), tc.want)
			}
		})
	}
}

// ── New — success paths ───────────────────────────────────────────────────────

func TestNew_SucceedsWithMinimalConfig(t *testing.T) {
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() { m.Shutdown() })
}

func TestNew_AcceptsFileDBPath(t *testing.T) {
	ctx := context.Background()

	cfg := minimalConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "malipo.db")

	m, err := malipo.New(ctx, cfg)
	if err != nil {
		t.Fatalf("New with file DB path failed: %v", err)
	}
	t.Cleanup(func() { m.Shutdown() })
}

// ── Manager ───────────────────────────────────────────────────────────────────

func TestMalipo_ManagerIsNotNil(t *testing.T) {
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() { m.Shutdown() })

	if m.Manager == nil {
		t.Error("Manager is nil")
	}
}

func TestMalipo_ManagerCanGetStatus(t *testing.T) {
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() { m.Shutdown() })

	// GetStatus on a non-existent ID must return an error — confirms the
	// Manager is fully wired to the storage adapter and not a stub.
	// The specific error (ErrNotFound) is owned by the session package;
	// here we only assert the wiring is live.
	_, _, err = m.Manager.GetStatus(ctx, "nonexistent-session-id")
	if err == nil {
		t.Error("expected error for unknown session, got nil — Manager may not be wired to storage")
	}
}

// ── CallbackHandler ───────────────────────────────────────────────────────────

func TestMalipo_CallbackHandlerIsNotNil(t *testing.T) {
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() { m.Shutdown() })

	if m.CallbackHandler() == nil {
		t.Error("CallbackHandler returned nil")
	}
}

func TestMalipo_CallbackHandlerRejectsGET(t *testing.T) {
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() { m.Shutdown() })

	req := httptest.NewRequest(http.MethodGet, "/mpesa/callback", nil)
	rec := httptest.NewRecorder()
	m.CallbackHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("got status %d want 405", rec.Code)
	}
}

func TestMalipo_CallbackHandlerCanBeCalledMultipleTimes(t *testing.T) {
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() { m.Shutdown() })

	h1 := m.CallbackHandler()
	h2 := m.CallbackHandler()

	if h1 == nil || h2 == nil {
		t.Fatal("CallbackHandler returned nil on one of two calls")
	}

	// Both handlers must be independently functional.
	// The method guard (405 on GET) exercises the full handler pipeline
	// without requiring a real Safaricom payload.
	req := httptest.NewRequest(http.MethodGet, "/mpesa/callback", nil)

	rec1 := httptest.NewRecorder()
	h1.ServeHTTP(rec1, req)
	if rec1.Code != http.StatusMethodNotAllowed {
		t.Errorf("h1: got status %d want 405", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Errorf("h2: got status %d want 405", rec2.Code)
	}
}

// ── Gate ──────────────────────────────────────────────────────────────────────
//
// Gate tests are constrained by the lack of a mockable TokenProvider at this
// level — malipo.New constructs auth.Manager internally. Tests that require a
// successful InitiatePayment (and therefore a 402 body to inspect) are
// covered in x402/x402_test.go with full mock control. Here we test:
//   - construction-time panic on nil PhoneExtractor (Branch 0)
//   - 400 on extractor failure (Branch 2, no Daraja call involved)
//   - 500 when Daraja is unreachable (Branch 3, error propagation chain)

func TestMalipo_Gate_PanicsOnNilPhoneExtractor(t *testing.T) {
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() { m.Shutdown() })

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil PhoneExtractor, got none")
		}
	}()

	// x402.Gate panics immediately at construction time — not at request time.
	m.Gate(malipo.GateOptions{
		Amount:         100,
		PhoneExtractor: nil,
	})
}

func TestMalipo_Gate_Returns400WhenPhoneExtractorFails(t *testing.T) {
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() { m.Shutdown() })

	gate := m.Gate(malipo.GateOptions{
		Amount: 100,
		PhoneExtractor: func(r *http.Request) (string, error) {
			return "", errors.New("phone not available")
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d want 400", rec.Code)
	}
}

func TestMalipo_Gate_Returns500WhenDarajaUnreachable(t *testing.T) {
	// PhoneExtractor succeeds — Gate proceeds to Branch 3 and calls
	// InitiatePayment. GetAccessToken fails (sandbox.safaricom.co.ke is
	// unreachable in test environments). x402.Gate maps that error to 500.
	// This test verifies the full error propagation chain:
	//   Gate → InitiatePayment → GetAccessToken → fetchToken → HTTP error → 500
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() { m.Shutdown() })

	gate := m.Gate(malipo.GateOptions{
		Amount: 100,
		PhoneExtractor: func(r *http.Request) (string, error) {
			return "+254712345678", nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("got status %d want 500", rec.Code)
	}
}

// ── Shutdown ──────────────────────────────────────────────────────────────────

func TestMalipo_ShutdownDoesNotPanic(t *testing.T) {
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if err := m.Shutdown(); err != nil {
		t.Errorf("Shutdown returned unexpected error: %v", err)
	}
}

func TestMalipo_ShutdownStopsBackgroundWork(t *testing.T) {
	ctx := context.Background()

	m, err := malipo.New(ctx, minimalConfig())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if err := m.Shutdown(); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// After Shutdown, db.Close() has been called. Any storage operation
	// through Manager must return an error — the DB connection is gone.
	// This confirms both Manager.Stop() and db.Close() were called, and
	// that Stop came first (if Close happened first and a ticker fired
	// between the two calls, the test run with -race would surface it).
	_, _, err = m.Manager.GetStatus(ctx, "any-id")
	if err == nil {
		t.Error("expected error after Shutdown — storage should be closed")
	}
}