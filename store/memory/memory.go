package memory

import (
	"context"
	"sync"
	"time"

	"github.com/brandon-kigen/malipo/store"
)

// MemoryAdapter is an in-memory implementation of store.StorageAdapter.
// It is intended for use in tests only — state is not persisted across
// process restarts and is lost when the adapter is garbage collected.
//
// All methods are safe for concurrent use. A sync.RWMutex protects the
// two internal maps — reads acquire RLock, writes acquire Lock.
type MemoryAdapter struct {
	mu         sync.RWMutex
	sessions   map[string]*store.Session // primary index — keyed by session ID
	byCheckout map[string]string         // secondary index — CheckoutRequestID → session ID
}

// NewMemoryAdapter returns an initialised MemoryAdapter ready for use.
// Both internal maps are initialised with make — a nil map panics on write.
func NewMemoryAdapter() *MemoryAdapter {
	return &MemoryAdapter{
		sessions:   make(map[string]*store.Session),
		byCheckout: make(map[string]string),
	}
}

// Compile-time check that *MemoryAdapter satisfies store.StorageAdapter.
// If any method is missing or has the wrong signature, this line fails to build.
var _ store.StorageAdapter = (*MemoryAdapter)(nil)

// Create stores a new session record.
// Returns store.ErrSessionExists if a session with the same ID already exists.
// A copy of the session is stored — the caller's pointer and the stored pointer
// point to separate memory, preventing unguarded mutations after Create returns.
func (a *MemoryAdapter) Create(ctx context.Context, s *store.Session) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.sessions[s.ID]; exists {
		return store.ErrSessionExists
	}

	copy := *s
	a.sessions[s.ID] = &copy

	return nil
}

// Get returns a copy of the session with the given ID.
// Returns store.ErrNotFound if no session exists with that ID.
// A copy is returned — the caller cannot mutate the stored record
// without going through the adapter's write methods.
func (a *MemoryAdapter) Get(ctx context.Context, id string) (*store.Session, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	session, exists := a.sessions[id]
	if !exists {
		return nil, store.ErrNotFound
	}

	copy := *session
	return &copy, nil
}

// GetByCheckoutID returns a copy of the session with the given Daraja
// CheckoutRequestID. Uses the byCheckout secondary index for O(1) lookup —
// resolves CheckoutRequestID → session ID → session in two map lookups.
// Returns store.ErrNotFound if no session is indexed under that CheckoutRequestID.
func (a *MemoryAdapter) GetByCheckoutID(ctx context.Context, checkoutID string) (*store.Session, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	sessionID, exists := a.byCheckout[checkoutID]
	if !exists {
		return nil, store.ErrNotFound
	}

	session, exists := a.sessions[sessionID]
	if !exists {
		return nil, store.ErrNotFound
	}

	copy := *session
	return &copy, nil
}

// Transition atomically advances a session from one state to another.
// Three guards are enforced before any write:
//  1. Session must exist — ErrNotFound if not
//  2. Current state must not be terminal — ErrInvalidTransition if it is
//  3. Current state must match from — ErrInvalidTransition if it does not
//
// Pointer fields in u are applied only when non-nil — nil means "do not
// update this field". The byCheckout index is updated when u.CheckoutRequestID
// is set — this is the moment the Daraja correlation ID first becomes known.
func (a *MemoryAdapter) Transition(ctx context.Context, id string, from, to store.State, u *store.Update) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	session, exists := a.sessions[id]
	if !exists {
		return store.ErrNotFound
	}

	if session.State.IsTerminal() {
		return store.ErrInvalidTransition
	}

	if session.State != from {
		return store.ErrInvalidTransition
	}

	if u != nil {
		if u.CheckoutRequestID != nil {
			session.CheckoutRequestID = *u.CheckoutRequestID
			// Update secondary index — enables GetByCheckoutID lookups
			// once Daraja returns the correlation ID.
			a.byCheckout[*u.CheckoutRequestID] = session.ID
		}
		if u.MerchantRequestID != nil {
			session.MerchantRequestID = *u.MerchantRequestID
		}
		if u.MpesaReceiptNumber != nil {
			session.MpesaReceiptNumber = *u.MpesaReceiptNumber
		}
		if u.ConfirmedAmount != nil {
			session.ConfirmedAmount = u.ConfirmedAmount
		}
		if u.ConfirmedPhone != nil {
			session.ConfirmedPhone = u.ConfirmedPhone
		}
		if u.ConsumedAt != nil {
			session.ConsumedAt = u.ConsumedAt
		}
	}

	session.State = to
	return nil
}

// ConsumeIfConfirmed atomically transitions a session from CONFIRMED to CONSUMED.
// This is the double-spend prevention mechanism — the check and write happen
// inside a single lock acquisition with no gap between them.
//
// Returns store.ErrNotFound if the session does not exist.
// Returns store.ErrAlreadyConsumed if the session is already CONSUMED.
// Returns store.ErrInvalidTransition if the session is in any other non-CONFIRMED state.
// Returns a copy of the consumed session on success.
func (a *MemoryAdapter) ConsumeIfConfirmed(ctx context.Context, id string) (*store.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	session, exists := a.sessions[id]
	if !exists {
		return nil, store.ErrNotFound
	}

	if session.State == store.StateConsumed {
		return nil, store.ErrAlreadyConsumed
	}

	if session.State != store.StateConfirmed {
		return nil, store.ErrInvalidTransition
	}

	session.State = store.StateConsumed
	consumedAt := time.Now()
	session.ConsumedAt = &consumedAt

	copy := *session
	return &copy, nil
}

// ExpireStale transitions all non-terminal sessions whose ExpiresAt is
// before the given cutoff time to TIMEOUT.
// Returns the number of sessions that were expired.
// Returns 0, nil if no sessions were eligible — this is not an error.
// Called by the session.Manager cleanup ticker every 30 seconds as a
// safety net for sessions whose per-session goroutine was lost across
// a process restart.
func (a *MemoryAdapter) ExpireStale(ctx context.Context, before time.Time) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var count int64

	for _, session := range a.sessions {
		if !session.State.IsTerminal() && session.ExpiresAt.Before(before) {
			session.State = store.StateTimeout
			count++
		}
	}

	return count, nil
}

// ListPending returns copies of all sessions in STK_PUSHED or AWAITING_PIN
// state whose CreatedAt is before the given threshold.
// Returns an empty slice if no sessions match — never returns nil.
func (a *MemoryAdapter) ListPending(ctx context.Context, before time.Time) ([]*store.Session, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var pending []*store.Session

	for _, session := range a.sessions {
		if session.CreatedAt.Before(before) &&
			(session.State == store.StateSTKPushed || session.State == store.StateAwaitingPIN) {
			copy := *session
			pending = append(pending, &copy)
		}
	}

	if pending == nil {
		pending = []*store.Session{}
	}

	return pending, nil
}