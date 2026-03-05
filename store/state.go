package store

import "errors"

// State represents the lifecycle stage of a payment session.
type State string

const (
	StateCreated     State = "CREATED"
	StateSTKPushed   State = "STK_PUSHED"
	StateAwaitingPIN State = "AWAITING_PIN"
	StateConfirmed   State = "CONFIRMED"
	StateConsumed    State = "CONSUMED"
	StateTimeout     State = "TIMEOUT"
	StateCancelled   State = "CANCELLED"
	StateFailed      State = "FAILED"
)

// IsTerminal returns true if no further transitions are possible.
func (s State) IsTerminal() bool {
	switch s {
	case StateConsumed, StateTimeout, StateCancelled, StateFailed:
		return true
	}
	return false
}

// Sentinel errors — returned by adapter implementations.
// Callers check against these with errors.Is().
var (
	ErrNotFound           = errors.New("store: session not found")
	ErrSessionExists      = errors.New("store: session already exists")
	ErrInvalidTransition  = errors.New("store: invalid state transition")
	ErrAlreadyConsumed    = errors.New("store: session already consumed")
	ErrExpired            = errors.New("store: session has expired")
	ErrStorageUnavailable = errors.New("store: storage unavailable")
)
