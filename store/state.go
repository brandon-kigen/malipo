package store

import "errors"

// State represents the lifecycle stage of a payment session.
// Sessions advance through states via defined transitions — see
// session/state.go for the full transition table and the events that trigger
// each transition.
//
// States are divided into two categories:
//
// Non-terminal states can accept further transitions:
//   - CREATED, STK_PUSHED, AWAITING_PIN, CONFIRMED
//
// Terminal states cannot be left — any write attempt on a terminal session
// is rejected by the storage adapter with ErrInvalidTransition:
//   - CONSUMED, TIMEOUT, CANCELLED, FAILED
type State string

const (
	// StateCreated is the initial state of every payment session.
	// The session exists in storage but the STK Push has not yet been sent.
	StateCreated State = "CREATED"

	// StateSTKPushed means Daraja accepted the STK Push request and has
	// delivered (or is delivering) the PIN prompt to the user's SIM.
	// CheckoutRequestID and MerchantRequestID are populated at this point.
	StateSTKPushed State = "STK_PUSHED"

	// StateAwaitingPIN means the STK Push Query API confirmed the PIN prompt
	// was delivered to the user's SIM and is awaiting their input.
	// The session enters this state via the recovery loop when a callback
	// has not arrived within the QueryThreshold window.
	StateAwaitingPIN State = "AWAITING_PIN"

	// StateConfirmed means Safaricom sent a success callback (ResultCode 0).
	// MpesaReceiptNumber, ConfirmedAmount, and ConfirmedPhone are populated.
	// The session is ready to be consumed by the x402 Gate middleware.
	StateConfirmed State = "CONFIRMED"

	// StateConsumed is a terminal state. The x402 Gate middleware atomically
	// transitioned the session here via ConsumeIfConfirmed, releasing the
	// gated resource to the client. A session can only be consumed once.
	StateConsumed State = "CONSUMED"

	// StateTimeout is a terminal state. The session expired before a
	// confirmation callback was received — either the user ignored the STK
	// Push prompt, or the network timed out. The client must initiate a new
	// payment session.
	StateTimeout State = "TIMEOUT"

	// StateCancelled is a terminal state. Safaricom sent a callback with
	// ResultCode 1032, indicating the user explicitly cancelled the payment
	// prompt on their phone.
	StateCancelled State = "CANCELLED"

	// StateFailed is a terminal state. Safaricom sent a callback with a
	// non-zero, non-1032, non-1037 ResultCode, or Daraja rejected the
	// initial STK Push request. The specific failure reason is in the
	// Daraja response — Malipo does not surface it to the client.
	StateFailed State = "FAILED"
)

// IsTerminal reports whether the state is a terminal state.
// Terminal states cannot be left — storage adapters must reject any further
// transition attempt with ErrInvalidTransition.
func (s State) IsTerminal() bool {
	switch s {
	case StateConsumed, StateTimeout, StateCancelled, StateFailed:
		return true
	}
	return false
}

// Sentinel errors returned by StorageAdapter implementations.
// Callers should check against these with errors.Is rather than comparing
// error strings — implementations must return these exact values, not
// wrapped copies, so that errors.Is works without errors.As.
var (
	// ErrNotFound is returned when a session lookup by ID or CheckoutRequestID
	// finds no matching record.
	ErrNotFound = errors.New("store: session not found")

	// ErrSessionExists is returned by Create when a session with the same ID
	// already exists in storage. Because session IDs are ULIDs generated with
	// cryptographic entropy, this error indicates a bug in ID generation.
	ErrSessionExists = errors.New("store: session already exists")

	// ErrInvalidTransition is returned by Transition when the session's current
	// state does not match the expected from-state, or when a transition is
	// attempted on a terminal session. Also returned by ConsumeIfConfirmed
	// when the session is not in CONFIRMED state.
	ErrInvalidTransition = errors.New("store: invalid state transition")

	// ErrAlreadyConsumed is returned by ConsumeIfConfirmed when the session
	// has already been consumed. This is the expected outcome for any concurrent
	// caller that loses the race to consume a CONFIRMED session.
	ErrAlreadyConsumed = errors.New("store: session already consumed")

	// ErrExpired is reserved for future use. Sessions expire by transitioning
	// to StateTimeout via ExpireStale or the per-session TTL goroutine.
	ErrExpired = errors.New("store: session has expired")

	// ErrStorageUnavailable is reserved for storage backends that may be
	// temporarily unreachable (e.g. a network-attached database). SQLite and
	// the memory adapter do not return this error.
	ErrStorageUnavailable = errors.New("store: storage unavailable")
)