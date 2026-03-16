// Package store defines the storage interface and domain types for Malipo
// payment sessions.
//
// The central type is StorageAdapter — an interface that all storage backends
// must implement. Two implementations are provided: store/sqlite (the
// production default, zero-configuration) and store/memory (in-memory, for
// unit tests). Additional backends such as Redis or PostgreSQL can be
// implemented by satisfying this interface.
//
// The session package depends on store via the StorageAdapter interface.
// It never imports a concrete adapter type — both the adapter and the auth
// manager are injected at construction time, keeping the dependency graph
// acyclic and every package independently testable.
package store

import (
	"context"
	"time"
)

// StorageAdapter is the persistence interface for Malipo payment sessions.
//
// All methods must be safe for concurrent use. Implementations are expected
// to enforce the following invariants:
//
//   - Terminal sessions (CONSUMED, TIMEOUT, CANCELLED, FAILED) must reject
//     any further state transitions with ErrInvalidTransition.
//   - ConsumeIfConfirmed must be atomic — it is the double-spend prevention
//     mechanism. Only one concurrent caller may succeed; all others must
//     receive ErrAlreadyConsumed.
//   - Get and ListPending must return copies of stored records, not pointers
//     to internal state. Callers must not be able to mutate the adapter's
//     data without going through the write methods.
//
// Sentinel errors are defined in state.go and should be returned (not wrapped)
// so callers can use errors.Is for type-checking.
type StorageAdapter interface {
	// Create inserts a new session record in CREATED state.
	// Returns ErrSessionExists if a session with the same ID already exists.
	Create(ctx context.Context, s *Session) error

	// Get returns a copy of the session with the given ID.
	// Returns ErrNotFound if no session exists with that ID.
	Get(ctx context.Context, id string) (*Session, error)

	// GetByCheckoutID returns a copy of the session indexed under the given
	// Daraja CheckoutRequestID. Returns ErrNotFound if no session is indexed
	// under that ID.
	//
	// The CheckoutRequestID index is populated during the CREATED→STK_PUSHED
	// transition when Daraja returns the correlation ID. Sessions in CREATED
	// state cannot be looked up by checkout ID.
	GetByCheckoutID(ctx context.Context, checkoutID string) (*Session, error)

	// Transition atomically advances a session from the given from-state to
	// the given to-state, optionally writing the fields in u.
	//
	// Returns ErrNotFound if no session exists with the given ID.
	// Returns ErrInvalidTransition if the session's current state does not
	// match from, or if the session is already in a terminal state.
	//
	// Pointer fields in u are applied only when non-nil — nil means "do not
	// update this field". This allows partial updates without clearing
	// previously written values.
	Transition(ctx context.Context, id string, from, to State, u *Update) error

	// ConsumeIfConfirmed atomically transitions a session from CONFIRMED to
	// CONSUMED and returns the updated session.
	//
	// This is the double-spend prevention mechanism. The check and the write
	// happen inside a single atomic operation with no gap between them — only
	// one concurrent caller can succeed regardless of how many goroutines call
	// this simultaneously.
	//
	// Returns ErrNotFound if no session exists with the given ID.
	// Returns ErrAlreadyConsumed if the session is already CONSUMED.
	// Returns ErrInvalidTransition if the session is in any other state.
	ConsumeIfConfirmed(ctx context.Context, id string) (*Session, error)

	// ExpireStale transitions all non-terminal sessions whose ExpiresAt is
	// before the given cutoff time to TIMEOUT state.
	//
	// Called by the session manager's cleanup ticker every 30 seconds as a
	// safety net for sessions whose per-session TTL goroutine was lost across
	// a process restart. Returns the number of sessions expired, which may
	// be zero — that is not an error.
	ExpireStale(ctx context.Context, before time.Time) (int64, error)

	// ListPending returns copies of all sessions in STK_PUSHED or AWAITING_PIN
	// state whose CreatedAt is before the given threshold.
	//
	// Called by the session manager's recovery loop to find sessions that
	// have not received a Safaricom callback within the QueryThreshold window.
	// These sessions are then queried via the Daraja STK Push Query API to
	// determine their current status.
	//
	// Returns an empty (non-nil) slice when no sessions match.
	ListPending(ctx context.Context, before time.Time) ([]*Session, error)
}