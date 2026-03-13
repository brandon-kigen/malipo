package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brandon-kigen/malipo/store"
	"github.com/brandon-kigen/malipo/store/sqlite"
)

// newTestAdapter opens an in-memory SQLite database.
// Each test gets its own adapter — no shared state between tests.
// t.Cleanup registers Close so the connection pool is released
// when the test exits regardless of pass or fail.
func newTestAdapter(t *testing.T) *sqlite.SQLiteAdapter {
	t.Helper()
	a, err := sqlite.NewSQLiteAdapter(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAdapter failed: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// newTestSession returns a minimal valid session for use in tests.
// Timestamps are truncated to second precision — RFC3339 storage in SQLite
// discards sub-second precision, so truncating here avoids false failures
// when comparing stored vs original timestamps.
func newTestSession(id string) *store.Session {
	now := time.Now().UTC().Truncate(time.Second)
	return &store.Session{
		ID:        id,
		State:     store.StateCreated,
		Phone:     "+254712345678",
		Amount:    100,
		Currency:  "KES",
		Shortcode: "174379",
		CreatedAt: now,
		ExpiresAt: now.Add(90 * time.Second),
	}
}

func strPtr(s string) *string { return &s }
func int64Ptr(i int64) *int64 { return &i }

// ── Create ────────────────────────────────────────────────────────────────────

func TestCreate(t *testing.T) {
	ctx := context.Background()

	t.Run("stores a valid session", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000001")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get after Create failed: %v", err)
		}
		if got.ID != s.ID {
			t.Errorf("got ID %q want %q", got.ID, s.ID)
		}
		if got.State != store.StateCreated {
			t.Errorf("got state %q want CREATED", got.State)
		}
		if got.Phone != s.Phone {
			t.Errorf("got phone %q want %q", got.Phone, s.Phone)
		}
		if got.Amount != s.Amount {
			t.Errorf("got amount %d want %d", got.Amount, s.Amount)
		}
		if got.Currency != s.Currency {
			t.Errorf("got currency %q want %q", got.Currency, s.Currency)
		}
		if got.Shortcode != s.Shortcode {
			t.Errorf("got shortcode %q want %q", got.Shortcode, s.Shortcode)
		}
	})

	t.Run("timestamps survive a round-trip through SQLite", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000002")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		// Timestamps are stored as RFC3339 strings — sub-second precision is lost.
		// newTestSession truncates to seconds so these comparisons are exact.
		if !got.CreatedAt.Equal(s.CreatedAt) {
			t.Errorf("CreatedAt mismatch: got %v want %v", got.CreatedAt, s.CreatedAt)
		}
		if !got.ExpiresAt.Equal(s.ExpiresAt) {
			t.Errorf("ExpiresAt mismatch: got %v want %v", got.ExpiresAt, s.ExpiresAt)
		}
	})

	t.Run("nullable pointer fields are nil after create", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000003")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.ConfirmedAmount != nil {
			t.Errorf("ConfirmedAmount should be nil, got %d", *got.ConfirmedAmount)
		}
		if got.ConfirmedPhone != nil {
			t.Errorf("ConfirmedPhone should be nil, got %q", *got.ConfirmedPhone)
		}
		if got.ConsumedAt != nil {
			t.Errorf("ConsumedAt should be nil, got %v", *got.ConsumedAt)
		}
	})

	t.Run("returns ErrSessionExists on duplicate ID", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000004")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("first Create failed: %v", err)
		}

		err := a.Create(ctx, s)
		if !errors.Is(err, store.ErrSessionExists) {
			t.Errorf("got error %v want ErrSessionExists", err)
		}
	})
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestGet(t *testing.T) {
	ctx := context.Background()

	t.Run("returns stored session by id", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000005")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.ID != s.ID {
			t.Errorf("got ID %q want %q", got.ID, s.ID)
		}
		if got.Amount != s.Amount {
			t.Errorf("got amount %d want %d", got.Amount, s.Amount)
		}
	})

	t.Run("returns ErrNotFound for unknown id", func(t *testing.T) {
		a := newTestAdapter(t)

		_, err := a.Get(ctx, "nonexistent-id")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got error %v want ErrNotFound", err)
		}
	})
}

// ── GetByCheckoutID ───────────────────────────────────────────────────────────

func TestGetByCheckoutID(t *testing.T) {
	ctx := context.Background()

	t.Run("returns session after checkout id is set via Transition", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000006")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		checkoutID := "ws_CO_123456789"
		if err := a.Transition(ctx, s.ID, store.StateCreated, store.StateSTKPushed, &store.Update{
			CheckoutRequestID: strPtr(checkoutID),
			MerchantRequestID: strPtr("MR_987654321"),
		}); err != nil {
			t.Fatalf("Transition failed: %v", err)
		}

		got, err := a.GetByCheckoutID(ctx, checkoutID)
		if err != nil {
			t.Fatalf("GetByCheckoutID failed: %v", err)
		}
		if got.ID != s.ID {
			t.Errorf("got ID %q want %q", got.ID, s.ID)
		}
		if got.CheckoutRequestID != checkoutID {
			t.Errorf("got CheckoutRequestID %q want %q", got.CheckoutRequestID, checkoutID)
		}
	})

	t.Run("returns ErrNotFound for unknown checkout id", func(t *testing.T) {
		a := newTestAdapter(t)

		_, err := a.GetByCheckoutID(ctx, "ws_CO_nonexistent")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got error %v want ErrNotFound", err)
		}
	})
}

// ── Transition ────────────────────────────────────────────────────────────────

func TestTransition(t *testing.T) {
	ctx := context.Background()

	t.Run("advances state and writes update fields", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000007")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		checkoutID := "ws_CO_111111111"
		merchantID := "MR_222222222"

		if err := a.Transition(ctx, s.ID, store.StateCreated, store.StateSTKPushed, &store.Update{
			CheckoutRequestID: strPtr(checkoutID),
			MerchantRequestID: strPtr(merchantID),
		}); err != nil {
			t.Fatalf("Transition failed: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.State != store.StateSTKPushed {
			t.Errorf("got state %q want STK_PUSHED", got.State)
		}
		if got.CheckoutRequestID != checkoutID {
			t.Errorf("got CheckoutRequestID %q want %q", got.CheckoutRequestID, checkoutID)
		}
		if got.MerchantRequestID != merchantID {
			t.Errorf("got MerchantRequestID %q want %q", got.MerchantRequestID, merchantID)
		}
	})

	t.Run("COALESCE preserves existing fields when update field is nil", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000008")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		checkoutID := "ws_CO_333"
		if err := a.Transition(ctx, s.ID, store.StateCreated, store.StateSTKPushed, &store.Update{
			CheckoutRequestID: strPtr(checkoutID),
			MerchantRequestID: strPtr("MR_444"),
		}); err != nil {
			t.Fatalf("first Transition failed: %v", err)
		}

		// Second transition sets callback fields only — CheckoutRequestID must
		// be preserved by COALESCE(NULL, checkout_request_id) in the SQL.
		if err := a.Transition(ctx, s.ID, store.StateSTKPushed, store.StateConfirmed, &store.Update{
			MpesaReceiptNumber: strPtr("RCP_555"),
			ConfirmedAmount:    int64Ptr(100),
			ConfirmedPhone:     strPtr("+254712345678"),
		}); err != nil {
			t.Fatalf("second Transition failed: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.State != store.StateConfirmed {
			t.Errorf("got state %q want CONFIRMED", got.State)
		}
		// CheckoutRequestID was set in first transition — must still be present
		if got.CheckoutRequestID != checkoutID {
			t.Errorf("CheckoutRequestID was cleared by COALESCE: got %q want %q", got.CheckoutRequestID, checkoutID)
		}
		if got.MpesaReceiptNumber != "RCP_555" {
			t.Errorf("got MpesaReceiptNumber %q want RCP_555", got.MpesaReceiptNumber)
		}
	})

	t.Run("nil update does not panic", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000009")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		// EventSTKRejected fires with nil update — must not panic
		if err := a.Transition(ctx, s.ID, store.StateCreated, store.StateFailed, nil); err != nil {
			t.Fatalf("Transition with nil update failed: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.State != store.StateFailed {
			t.Errorf("got state %q want FAILED", got.State)
		}
	})

	t.Run("returns ErrInvalidTransition when from-state does not match", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000010")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		// Session is CREATED — claim it is STK_PUSHED in the from argument.
		// WHERE state = 'STK_PUSHED' matches nothing → rows affected = 0.
		err := a.Transition(ctx, s.ID, store.StateSTKPushed, store.StateConfirmed, nil)
		if !errors.Is(err, store.ErrInvalidTransition) {
			t.Errorf("got error %v want ErrInvalidTransition", err)
		}
	})

	t.Run("returns ErrInvalidTransition for unknown session", func(t *testing.T) {
		a := newTestAdapter(t)

		// No row matches WHERE id = ? — rows affected = 0.
		err := a.Transition(ctx, "nonexistent-id", store.StateCreated, store.StateSTKPushed, nil)
		if !errors.Is(err, store.ErrInvalidTransition) {
			t.Errorf("got error %v want ErrInvalidTransition", err)
		}
	})
}

// ── ConsumeIfConfirmed ────────────────────────────────────────────────────────

func TestConsumeIfConfirmed(t *testing.T) {
	ctx := context.Background()

	// createConfirmed drives a fresh session from CREATED to CONFIRMED.
	createConfirmed := func(t *testing.T, a *sqlite.SQLiteAdapter, id string) {
		t.Helper()
		s := newTestSession(id)
		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		if err := a.Transition(ctx, id, store.StateCreated, store.StateSTKPushed, &store.Update{
			CheckoutRequestID: strPtr("ws_CO_" + id),
			MerchantRequestID: strPtr("MR_" + id),
		}); err != nil {
			t.Fatalf("Transition to STK_PUSHED failed: %v", err)
		}
		if err := a.Transition(ctx, id, store.StateSTKPushed, store.StateConfirmed, &store.Update{
			MpesaReceiptNumber: strPtr("RCP_" + id),
			ConfirmedAmount:    int64Ptr(100),
			ConfirmedPhone:     strPtr("+254712345678"),
		}); err != nil {
			t.Fatalf("Transition to CONFIRMED failed: %v", err)
		}
	}

	t.Run("transitions CONFIRMED to CONSUMED and returns session", func(t *testing.T) {
		a := newTestAdapter(t)
		id := "01JMWX0000000000000011"
		createConfirmed(t, a, id)

		got, err := a.ConsumeIfConfirmed(ctx, id)
		if err != nil {
			t.Fatalf("ConsumeIfConfirmed failed: %v", err)
		}
		if got.State != store.StateConsumed {
			t.Errorf("got state %q want CONSUMED", got.State)
		}
		if got.ConsumedAt == nil {
			t.Error("ConsumedAt is nil — expected a timestamp")
		}
	})

	t.Run("consumed_at survives round-trip through SQLite", func(t *testing.T) {
		a := newTestAdapter(t)
		id := "01JMWX0000000000000012"
		createConfirmed(t, a, id)

		if _, err := a.ConsumeIfConfirmed(ctx, id); err != nil {
			t.Fatalf("ConsumeIfConfirmed failed: %v", err)
		}

		// Fetch again — ConsumedAt must have been persisted as RFC3339 and
		// parsed back correctly by scanSession.
		got, err := a.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get after consume failed: %v", err)
		}
		if got.ConsumedAt == nil {
			t.Fatal("ConsumedAt is nil after SQLite round-trip")
		}
		if time.Since(*got.ConsumedAt) > 5*time.Second {
			t.Errorf("ConsumedAt looks wrong — too far in the past: %v", *got.ConsumedAt)
		}
	})

	t.Run("returns ErrNotFound for unknown session", func(t *testing.T) {
		a := newTestAdapter(t)

		_, err := a.ConsumeIfConfirmed(ctx, "nonexistent-id")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got error %v want ErrNotFound", err)
		}
	})

	t.Run("returns ErrAlreadyConsumed on second call", func(t *testing.T) {
		a := newTestAdapter(t)
		id := "01JMWX0000000000000013"
		createConfirmed(t, a, id)

		if _, err := a.ConsumeIfConfirmed(ctx, id); err != nil {
			t.Fatalf("first ConsumeIfConfirmed failed: %v", err)
		}

		_, err := a.ConsumeIfConfirmed(ctx, id)
		if !errors.Is(err, store.ErrAlreadyConsumed) {
			t.Errorf("got error %v want ErrAlreadyConsumed", err)
		}
	})

	t.Run("returns ErrInvalidTransition when state is not CONFIRMED", func(t *testing.T) {
		a := newTestAdapter(t)
		id := "01JMWX0000000000000014"

		if err := a.Create(ctx, newTestSession(id)); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		// Session is CREATED — WHERE state = 'CONFIRMED' matches nothing.
		_, err := a.ConsumeIfConfirmed(ctx, id)
		if !errors.Is(err, store.ErrInvalidTransition) {
			t.Errorf("got error %v want ErrInvalidTransition", err)
		}
	})
}

// ── ExpireStale ───────────────────────────────────────────────────────────────

func TestExpireStale(t *testing.T) {
	ctx := context.Background()

	t.Run("expires non-terminal sessions past the cutoff", func(t *testing.T) {
		a := newTestAdapter(t)
		now := time.Now().UTC().Truncate(time.Second)

		stale := &store.Session{
			ID:        "01JMWX0000000000000015",
			State:     store.StateSTKPushed,
			Phone:     "+254712345678",
			Amount:    100,
			Currency:  "KES",
			Shortcode: "174379",
			CreatedAt: now.Add(-2 * time.Minute),
			ExpiresAt: now.Add(-1 * time.Minute), // already past cutoff
		}
		if err := a.Create(ctx, stale); err != nil {
			t.Fatalf("Create stale session failed: %v", err)
		}

		count, err := a.ExpireStale(ctx, now)
		if err != nil {
			t.Fatalf("ExpireStale failed: %v", err)
		}
		if count != 1 {
			t.Errorf("got count %d want 1", count)
		}

		got, err := a.Get(ctx, stale.ID)
		if err != nil {
			t.Fatalf("Get after ExpireStale failed: %v", err)
		}
		if got.State != store.StateTimeout {
			t.Errorf("got state %q want TIMEOUT", got.State)
		}
	})

	t.Run("does not expire sessions with future expiry", func(t *testing.T) {
		a := newTestAdapter(t)
		s := newTestSession("01JMWX0000000000000016")
		// ExpiresAt is 90s in the future — must not be touched

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		count, err := a.ExpireStale(ctx, time.Now())
		if err != nil {
			t.Fatalf("ExpireStale failed: %v", err)
		}
		if count != 0 {
			t.Errorf("got count %d want 0", count)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.State != store.StateCreated {
			t.Errorf("state changed unexpectedly: got %q want CREATED", got.State)
		}
	})

	t.Run("does not expire any of the four terminal states", func(t *testing.T) {
		a := newTestAdapter(t)
		now := time.Now().UTC().Truncate(time.Second)

		// Explicit unique IDs per state — do NOT derive from state[0] because
		// CONSUMED and CANCELLED both start with 'C', producing duplicate IDs
		// which cause Create to return ErrSessionExists on the second insert.
		terminalSessions := []struct {
			id    string
			state store.State
		}{
			{"01JMWX0000000000000017", store.StateConsumed},
			{"01JMWX0000000000000018", store.StateTimeout},
			{"01JMWX0000000000000019", store.StateCancelled},
			{"01JMWX0000000000000020", store.StateFailed},
		}

		for _, tc := range terminalSessions {
			s := &store.Session{
				ID:        tc.id,
				State:     tc.state,
				Phone:     "+254712345678",
				Amount:    100,
				Currency:  "KES",
				Shortcode: "174379",
				CreatedAt: now.Add(-2 * time.Minute),
				ExpiresAt: now.Add(-1 * time.Minute),
			}
			if err := a.Create(ctx, s); err != nil {
				t.Fatalf("Create %s session failed: %v", tc.state, err)
			}
		}

		// Cutoff far in the future — would catch everything non-terminal
		count, err := a.ExpireStale(ctx, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("ExpireStale failed: %v", err)
		}
		if count != 0 {
			t.Errorf("got count %d want 0 — terminal sessions must never be expired", count)
		}

		// Verify each session is still in its original terminal state
		for _, tc := range terminalSessions {
			got, err := a.Get(ctx, tc.id)
			if err != nil {
				t.Fatalf("Get %s failed: %v", tc.id, err)
			}
			if got.State != tc.state {
				t.Errorf("session %s state changed: got %q want %q", tc.id, got.State, tc.state)
			}
		}
	})

	t.Run("returns zero and no error when nothing is stale", func(t *testing.T) {
		a := newTestAdapter(t)

		count, err := a.ExpireStale(ctx, time.Now())
		if err != nil {
			t.Fatalf("ExpireStale on empty adapter failed: %v", err)
		}
		if count != 0 {
			t.Errorf("got count %d want 0", count)
		}
	})

	t.Run("expires multiple stale sessions in one sweep", func(t *testing.T) {
		a := newTestAdapter(t)
		now := time.Now().UTC().Truncate(time.Second)

		ids := []string{
			"01JMWX0000000000000021",
			"01JMWX0000000000000022",
			"01JMWX0000000000000023",
		}

		for _, id := range ids {
			s := &store.Session{
				ID:        id,
				State:     store.StateSTKPushed,
				Phone:     "+254712345678",
				Amount:    100,
				Currency:  "KES",
				Shortcode: "174379",
				CreatedAt: now.Add(-2 * time.Minute),
				ExpiresAt: now.Add(-1 * time.Minute),
			}
			if err := a.Create(ctx, s); err != nil {
				t.Fatalf("Create %s failed: %v", id, err)
			}
		}

		count, err := a.ExpireStale(ctx, now)
		if err != nil {
			t.Fatalf("ExpireStale failed: %v", err)
		}
		if count != 3 {
			t.Errorf("got count %d want 3", count)
		}

		for _, id := range ids {
			got, err := a.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get %s failed: %v", id, err)
			}
			if got.State != store.StateTimeout {
				t.Errorf("session %s: got state %q want TIMEOUT", id, got.State)
			}
		}
	})
}
