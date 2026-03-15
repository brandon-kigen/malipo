package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brandon-kigen/malipo/store"
	"github.com/brandon-kigen/malipo/store/memory"
)

// newTestSession returns a minimal valid session for use in tests.
// All required fields are populated. Pointer fields are nil — same
// state as a real session immediately after InitiatePayment calls Create.
func newTestSession(id string) *store.Session {
	now := time.Now()
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

// ptr helpers — avoid &literal which Go does not allow for basic types
func strPtr(s string) *string   { return &s }
func int64Ptr(i int64) *int64   { return &i }
func timePtr(t time.Time) *time.Time { return &t }

// ── Create ────────────────────────────────────────────────────────────────────

func TestCreate(t *testing.T) {
	ctx := context.Background()

	t.Run("stores a valid session", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000001")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get after Create failed: %v", err)
		}
		if got.ID != s.ID {
			t.Errorf("got ID %q want %q", got.ID, s.ID)
		}
		if got.State != store.StateCreated {
			t.Errorf("got state %q want %q", got.State, store.StateCreated)
		}
		if got.Phone != s.Phone {
			t.Errorf("got phone %q want %q", got.Phone, s.Phone)
		}
		if got.Amount != s.Amount {
			t.Errorf("got amount %d want %d", got.Amount, s.Amount)
		}
	})

	t.Run("returns ErrSessionExists on duplicate ID", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000002")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("first Create failed: %v", err)
		}

		err := a.Create(ctx, s)
		if !errors.Is(err, store.ErrSessionExists) {
			t.Errorf("got error %v want ErrSessionExists", err)
		}
	})

	t.Run("stored copy is independent of caller pointer", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000003")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		// Mutate original — must not corrupt stored record
		s.State = store.StateConsumed
		s.Phone = "+254999999999"

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.State != store.StateCreated {
			t.Errorf("stored state was mutated: got %q want %q", got.State, store.StateCreated)
		}
		if got.Phone != "+254712345678" {
			t.Errorf("stored phone was mutated: got %q want %q", got.Phone, "+254712345678")
		}
	})
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestGet(t *testing.T) {
	ctx := context.Background()

	t.Run("returns stored session by id", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000004")

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
		if got.State != store.StateCreated {
			t.Errorf("got state %q want %q", got.State, store.StateCreated)
		}
		if got.Amount != s.Amount {
			t.Errorf("got amount %d want %d", got.Amount, s.Amount)
		}
	})

	t.Run("returns ErrNotFound for unknown id", func(t *testing.T) {
		a := memory.NewMemoryAdapter()

		_, err := a.Get(ctx, "nonexistent-id")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got error %v want ErrNotFound", err)
		}
	})

	t.Run("returned copy is independent of stored record", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000005")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		// Mutate returned copy — must not affect stored record
		got.State = store.StateConsumed
		got.Phone = "+254999999999"

		got2, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("second Get failed: %v", err)
		}
		if got2.State != store.StateCreated {
			t.Errorf("stored state was mutated: got %q want %q", got2.State, store.StateCreated)
		}
		if got2.Phone != "+254712345678" {
			t.Errorf("stored phone was mutated: got %q want %q", got2.Phone, "+254712345678")
		}
	})
}

// ── GetByCheckoutID ───────────────────────────────────────────────────────────

func TestGetByCheckoutID(t *testing.T) {
	ctx := context.Background()

	t.Run("returns session after checkout id is set via Transition", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000006")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		checkoutID := "ws_CO_123456789"
		merchantID := "MR_987654321"
		u := &store.Update{
			CheckoutRequestID: strPtr(checkoutID),
			MerchantRequestID: strPtr(merchantID),
		}

		if err := a.Transition(ctx, s.ID, store.StateCreated, store.StateSTKPushed, u); err != nil {
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
		a := memory.NewMemoryAdapter()

		_, err := a.GetByCheckoutID(ctx, "nonexistent-checkout-id")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got error %v want ErrNotFound", err)
		}
	})

	t.Run("returns ErrNotFound before checkout id is set", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000007")

		// Session exists but Transition has not yet set CheckoutRequestID
		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		_, err := a.GetByCheckoutID(ctx, "ws_CO_not_yet_set")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got error %v want ErrNotFound", err)
		}
	})
}

// ── Transition ────────────────────────────────────────────────────────────────

func TestTransition(t *testing.T) {
	ctx := context.Background()

	t.Run("advances state from CREATED to STK_PUSHED with update fields", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000008")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		checkoutID := "ws_CO_111111111"
		merchantID := "MR_222222222"
		u := &store.Update{
			CheckoutRequestID: strPtr(checkoutID),
			MerchantRequestID: strPtr(merchantID),
		}

		if err := a.Transition(ctx, s.ID, store.StateCreated, store.StateSTKPushed, u); err != nil {
			t.Fatalf("Transition failed: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get after Transition failed: %v", err)
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

	t.Run("advances state from STK_PUSHED to CONFIRMED with callback fields", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000009")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		if err := a.Transition(ctx, s.ID, store.StateCreated, store.StateSTKPushed, &store.Update{
			CheckoutRequestID: strPtr("ws_CO_333333333"),
			MerchantRequestID: strPtr("MR_444444444"),
		}); err != nil {
			t.Fatalf("first Transition failed: %v", err)
		}

		receipt := "QHX3Y4Z5W6"
		amount := int64(100)
		phone := "+254712345678"
		u := &store.Update{
			MpesaReceiptNumber: strPtr(receipt),
			ConfirmedAmount:    int64Ptr(amount),
			ConfirmedPhone:     strPtr(phone),
		}

		if err := a.Transition(ctx, s.ID, store.StateSTKPushed, store.StateConfirmed, u); err != nil {
			t.Fatalf("second Transition failed: %v", err)
		}

		got, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.State != store.StateConfirmed {
			t.Errorf("got state %q want CONFIRMED", got.State)
		}
		if got.MpesaReceiptNumber != receipt {
			t.Errorf("got receipt %q want %q", got.MpesaReceiptNumber, receipt)
		}
		if got.ConfirmedAmount == nil || *got.ConfirmedAmount != amount {
			t.Errorf("got confirmed amount %v want %d", got.ConfirmedAmount, amount)
		}
	})

	t.Run("nil update does not panic", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000010")

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

	t.Run("returns ErrNotFound for unknown session", func(t *testing.T) {
		a := memory.NewMemoryAdapter()

		err := a.Transition(ctx, "nonexistent-id", store.StateCreated, store.StateSTKPushed, nil)
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got error %v want ErrNotFound", err)
		}
	})

	t.Run("returns ErrInvalidTransition when from-state does not match", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000011")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		// Session is CREATED — claim it is STK_PUSHED
		err := a.Transition(ctx, s.ID, store.StateSTKPushed, store.StateConfirmed, nil)
		if !errors.Is(err, store.ErrInvalidTransition) {
			t.Errorf("got error %v want ErrInvalidTransition", err)
		}
	})

	t.Run("returns ErrInvalidTransition when session is terminal", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000012")

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		// Drive to terminal state
		if err := a.Transition(ctx, s.ID, store.StateCreated, store.StateFailed, nil); err != nil {
			t.Fatalf("Transition to FAILED: %v", err)
		}

		// Any further transition must be rejected
		err := a.Transition(ctx, s.ID, store.StateFailed, store.StateCreated, nil)
		if !errors.Is(err, store.ErrInvalidTransition) {
			t.Errorf("got error %v want ErrInvalidTransition", err)
		}
	})
}

// ── ConsumeIfConfirmed ────────────────────────────────────────────────────────

func TestConsumeIfConfirmed(t *testing.T) {
	ctx := context.Background()

	// helper — drives a fresh session to CONFIRMED state
	createConfirmed := func(t *testing.T, a *memory.MemoryAdapter, id string) {
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
		a := memory.NewMemoryAdapter()
		id := "01JMWX0000000000000013"
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

	t.Run("returns ErrNotFound for unknown session", func(t *testing.T) {
		a := memory.NewMemoryAdapter()

		_, err := a.ConsumeIfConfirmed(ctx, "nonexistent-id")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got error %v want ErrNotFound", err)
		}
	})

	t.Run("returns ErrAlreadyConsumed on second call", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		id := "01JMWX0000000000000014"
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
		a := memory.NewMemoryAdapter()
		id := "01JMWX0000000000000015"
		s := newTestSession(id)

		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		// Session is CREATED — not CONFIRMED
		_, err := a.ConsumeIfConfirmed(ctx, id)
		if !errors.Is(err, store.ErrInvalidTransition) {
			t.Errorf("got error %v want ErrInvalidTransition", err)
		}
	})

	t.Run("double-spend — concurrent callers only one succeeds", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		id := "01JMWX0000000000000016"
		createConfirmed(t, a, id)

		type result struct {
			err error
		}

		results := make(chan result, 10)

		// Fire 10 concurrent ConsumeIfConfirmed calls
		for i := 0; i < 10; i++ {
			go func() {
				_, err := a.ConsumeIfConfirmed(ctx, id)
				results <- result{err: err}
			}()
		}

		var successes int
		var alreadyConsumed int

		for i := 0; i < 10; i++ {
			r := <-results
			if r.err == nil {
				successes++
			} else if errors.Is(r.err, store.ErrAlreadyConsumed) {
				alreadyConsumed++
			} else {
				t.Errorf("unexpected error: %v", r.err)
			}
		}

		if successes != 1 {
			t.Errorf("got %d successes want exactly 1", successes)
		}
		if alreadyConsumed != 9 {
			t.Errorf("got %d ErrAlreadyConsumed want 9", alreadyConsumed)
		}
	})
}

// ── ExpireStale ───────────────────────────────────────────────────────────────

func TestExpireStale(t *testing.T) {
	ctx := context.Background()

	t.Run("expires non-terminal sessions past the cutoff", func(t *testing.T) {
		a := memory.NewMemoryAdapter()

		// Session with expires_at in the past
		now := time.Now()
		stale := &store.Session{
			ID:        "01JMWX0000000000000017",
			State:     store.StateSTKPushed,
			Phone:     "+254712345678",
			Amount:    100,
			Currency:  "KES",
			Shortcode: "174379",
			CreatedAt: now.Add(-2 * time.Minute),
			ExpiresAt: now.Add(-1 * time.Minute), // already expired
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
		a := memory.NewMemoryAdapter()
		s := newTestSession("01JMWX0000000000000018")
		// ExpiresAt is 90s from now — well in the future

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

	t.Run("does not expire terminal sessions", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		now := time.Now()

		// Terminal session with expires_at in the past
		terminal := &store.Session{
			ID:        "01JMWX0000000000000019",
			State:     store.StateConsumed, // terminal
			Phone:     "+254712345678",
			Amount:    100,
			Currency:  "KES",
			Shortcode: "174379",
			CreatedAt: now.Add(-2 * time.Minute),
			ExpiresAt: now.Add(-1 * time.Minute),
		}
		if err := a.Create(ctx, terminal); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		count, err := a.ExpireStale(ctx, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("ExpireStale failed: %v", err)
		}
		if count != 0 {
			t.Errorf("got count %d want 0 — terminal sessions must not be expired", count)
		}

		got, err := a.Get(ctx, terminal.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.State != store.StateConsumed {
			t.Errorf("terminal state changed: got %q want CONSUMED", got.State)
		}
	})

	t.Run("returns zero and no error when nothing is stale", func(t *testing.T) {
		a := memory.NewMemoryAdapter()

		count, err := a.ExpireStale(ctx, time.Now())
		if err != nil {
			t.Fatalf("ExpireStale on empty adapter failed: %v", err)
		}
		if count != 0 {
			t.Errorf("got count %d want 0", count)
		}
	})

	t.Run("expires multiple stale sessions in one sweep", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		now := time.Now()

		ids := []string{
			"01JMWX0000000000000020",
			"01JMWX0000000000000021",
			"01JMWX0000000000000022",
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
	})
}

// ── ListPending ───────────────────────────────────────────────────────────────

func TestListPending(t *testing.T) {
	ctx := context.Background()

	t.Run("returns STK_PUSHED sessions older than threshold", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		now := time.Now()

		s := &store.Session{
			ID:        "01JMWX0000000000000023",
			State:     store.StateSTKPushed,
			Phone:     "+254712345678",
			Amount:    100,
			Currency:  "KES",
			Shortcode: "174379",
			CreatedAt: now.Add(-2 * time.Minute), // older than threshold
			ExpiresAt: now.Add(90 * time.Second),
		}
		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		got, err := a.ListPending(ctx, now.Add(-1*time.Minute))
		if err != nil {
			t.Fatalf("ListPending failed: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d sessions want 1", len(got))
		}
		if got[0].ID != s.ID {
			t.Errorf("got ID %q want %q", got[0].ID, s.ID)
		}
	})

	t.Run("returns AWAITING_PIN sessions older than threshold", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		now := time.Now()

		s := &store.Session{
			ID:        "01JMWX0000000000000024",
			State:     store.StateAwaitingPIN,
			Phone:     "+254712345678",
			Amount:    100,
			Currency:  "KES",
			Shortcode: "174379",
			CreatedAt: now.Add(-2 * time.Minute),
			ExpiresAt: now.Add(90 * time.Second),
		}
		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		got, err := a.ListPending(ctx, now.Add(-1*time.Minute))
		if err != nil {
			t.Fatalf("ListPending failed: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d sessions want 1", len(got))
		}
		if got[0].State != store.StateAwaitingPIN {
			t.Errorf("got state %q want AWAITING_PIN", got[0].State)
		}
	})

	t.Run("does not return sessions newer than threshold", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		now := time.Now()

		s := &store.Session{
			ID:        "01JMWX0000000000000025",
			State:     store.StateSTKPushed,
			Phone:     "+254712345678",
			Amount:    100,
			Currency:  "KES",
			Shortcode: "174379",
			CreatedAt: now.Add(-10 * time.Second), // newer than threshold
			ExpiresAt: now.Add(90 * time.Second),
		}
		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		// threshold is 1 minute ago — session is only 10s old
		got, err := a.ListPending(ctx, now.Add(-1*time.Minute))
		if err != nil {
			t.Fatalf("ListPending failed: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d sessions want 0 — session is too new to query", len(got))
		}
	})

	t.Run("does not return CREATED sessions", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		now := time.Now()

		s := &store.Session{
			ID:        "01JMWX0000000000000026",
			State:     store.StateCreated, // not pending
			Phone:     "+254712345678",
			Amount:    100,
			Currency:  "KES",
			Shortcode: "174379",
			CreatedAt: now.Add(-2 * time.Minute),
			ExpiresAt: now.Add(90 * time.Second),
		}
		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		got, err := a.ListPending(ctx, now)
		if err != nil {
			t.Fatalf("ListPending failed: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d sessions want 0 — CREATED is not a pending state", len(got))
		}
	})

	t.Run("does not return CONFIRMED sessions", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		now := time.Now()

		s := &store.Session{
			ID:        "01JMWX0000000000000027",
			State:     store.StateConfirmed,
			Phone:     "+254712345678",
			Amount:    100,
			Currency:  "KES",
			Shortcode: "174379",
			CreatedAt: now.Add(-2 * time.Minute),
			ExpiresAt: now.Add(90 * time.Second),
		}
		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		got, err := a.ListPending(ctx, now)
		if err != nil {
			t.Fatalf("ListPending failed: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d sessions want 0 — CONFIRMED is not a pending state", len(got))
		}
	})

	t.Run("does not return terminal sessions", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		now := time.Now()

		terminalStates := []store.State{
			store.StateConsumed,
			store.StateTimeout,
			store.StateCancelled,
			store.StateFailed,
		}

		ids := []string{
			"01JMWX0000000000000028",
			"01JMWX0000000000000029",
			"01JMWX0000000000000030",
			"01JMWX0000000000000031",
		}

		for i, state := range terminalStates {
			s := &store.Session{
				ID:        ids[i],
				State:     state,
				Phone:     "+254712345678",
				Amount:    100,
				Currency:  "KES",
				Shortcode: "174379",
				CreatedAt: now.Add(-2 * time.Minute),
				ExpiresAt: now.Add(-1 * time.Minute),
			}
			if err := a.Create(ctx, s); err != nil {
				t.Fatalf("Create %s failed: %v", state, err)
			}
		}

		got, err := a.ListPending(ctx, now)
		if err != nil {
			t.Fatalf("ListPending failed: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d sessions want 0 — terminal sessions must not be returned", len(got))
		}
	})

	t.Run("returns empty slice not nil when nothing matches", func(t *testing.T) {
		a := memory.NewMemoryAdapter()

		got, err := a.ListPending(ctx, time.Now())
		if err != nil {
			t.Fatalf("ListPending failed: %v", err)
		}
		if got == nil {
			t.Error("got nil want empty slice")
		}
		if len(got) != 0 {
			t.Errorf("got %d sessions want 0", len(got))
		}
	})

	t.Run("returns copies — mutating result does not affect stored record", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		now := time.Now()

		s := &store.Session{
			ID:        "01JMWX0000000000000032",
			State:     store.StateSTKPushed,
			Phone:     "+254712345678",
			Amount:    100,
			Currency:  "KES",
			Shortcode: "174379",
			CreatedAt: now.Add(-2 * time.Minute),
			ExpiresAt: now.Add(90 * time.Second),
		}
		if err := a.Create(ctx, s); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		got, err := a.ListPending(ctx, now)
		if err != nil {
			t.Fatalf("ListPending failed: %v", err)
		}
		if len(got) == 0 {
			t.Fatal("expected one session")
		}

		// Mutate the returned copy
		got[0].State = store.StateConsumed
		got[0].Phone = "+254999999999"

		// Stored record must be unchanged
		stored, err := a.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if stored.State != store.StateSTKPushed {
			t.Errorf("stored state was mutated: got %q want STK_PUSHED", stored.State)
		}
		if stored.Phone != "+254712345678" {
			t.Errorf("stored phone was mutated: got %q want +254712345678", stored.Phone)
		}
	})

	t.Run("returns multiple matching sessions", func(t *testing.T) {
		a := memory.NewMemoryAdapter()
		now := time.Now()

		ids := []string{
			"01JMWX0000000000000033",
			"01JMWX0000000000000034",
			"01JMWX0000000000000035",
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
				ExpiresAt: now.Add(90 * time.Second),
			}
			if err := a.Create(ctx, s); err != nil {
				t.Fatalf("Create %s failed: %v", id, err)
			}
		}

		got, err := a.ListPending(ctx, now.Add(-1*time.Minute))
		if err != nil {
			t.Fatalf("ListPending failed: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("got %d sessions want 3", len(got))
		}
	})
}
