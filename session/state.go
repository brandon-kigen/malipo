package session

import "github.com/brandon-kigen/malipo/store"

// Event represents a trigger that causes a session state transition.
// Events are produced by three sources:
//
//   - The session manager (EventSTKAccepted, EventSTKRejected, EventTTLExpired)
//   - The callback handler via HandleCallback (EventCallback*)
//   - The recovery loop via runRecovery (EventQueryAwaitingPIN)
type Event string

const (
	// EventSTKAccepted fires when Daraja responds with 200 to the STK Push
	// request. Transitions CREATED → STK_PUSHED.
	EventSTKAccepted Event = "STK_ACCEPTED"

	// EventSTKRejected fires when Daraja returns an error to the STK Push
	// request. Transitions CREATED → FAILED.
	EventSTKRejected Event = "STK_REJECTED"

	// EventCallbackSuccess fires when Safaricom POSTs a callback with
	// ResultCode 0 (payment confirmed). Transitions STK_PUSHED or
	// AWAITING_PIN → CONFIRMED.
	EventCallbackSuccess Event = "CALLBACK_SUCCESS"

	// EventCallbackCancelled fires when Safaricom POSTs a callback with
	// ResultCode 1032 (user cancelled the prompt). Transitions STK_PUSHED or
	// AWAITING_PIN → CANCELLED.
	EventCallbackCancelled Event = "CALLBACK_CANCELLED"

	// EventCallbackTimeout fires when Safaricom POSTs a callback with
	// ResultCode 1037 (SIM unreachable or PIN prompt timed out). Transitions
	// STK_PUSHED or AWAITING_PIN → TIMEOUT.
	EventCallbackTimeout Event = "CALLBACK_TIMEOUT"

	// EventCallbackFailed fires when Safaricom POSTs a callback with any
	// non-zero ResultCode not explicitly mapped above. Transitions STK_PUSHED
	// or AWAITING_PIN → FAILED.
	EventCallbackFailed Event = "CALLBACK_FAILED"

	// EventConsumed fires when ConsumeIfConfirmed is called on a CONFIRMED
	// session by the x402 Gate middleware. Transitions CONFIRMED → CONSUMED.
	EventConsumed Event = "CONSUMED"

	// EventTTLExpired fires when the per-session expireAfter goroutine elapses.
	// Transitions STK_PUSHED or CONFIRMED → TIMEOUT.
	EventTTLExpired Event = "TTL_EXPIRED"

	// EventQueryAwaitingPIN fires when the STK Push Query API returns
	// ResultCode "500.001.1001", indicating the PIN prompt was delivered to
	// the user's SIM and is awaiting their input. Transitions STK_PUSHED →
	// AWAITING_PIN. Produced by the recovery loop in runRecovery.
	EventQueryAwaitingPIN Event = "QUERY_AWAITING_PIN"
)

// validTransitions is the source of truth for the state machine.
// Any (state, event) pair not present in this map is an invalid transition —
// the session manager returns ErrInvalidTransition for any such attempt.
//
// The map is keyed by the current state. Each value maps an event to the
// resulting next state. Only the states that can receive events are present
// as keys — terminal states (CONSUMED, TIMEOUT, CANCELLED, FAILED) have no
// outbound transitions and are enforced by the storage adapter rather than
// duplicated here.
var validTransitions = map[store.State]map[Event]store.State{
	store.StateCreated: {
		EventSTKAccepted: store.StateSTKPushed,
		EventSTKRejected: store.StateFailed,
	},
	store.StateSTKPushed: {
		EventCallbackSuccess:   store.StateConfirmed,
		EventCallbackCancelled: store.StateCancelled,
		EventCallbackTimeout:   store.StateTimeout,
		EventCallbackFailed:    store.StateFailed,
		EventTTLExpired:        store.StateTimeout,
		EventQueryAwaitingPIN:  store.StateAwaitingPIN,
	},
	store.StateAwaitingPIN: {
		EventCallbackSuccess:   store.StateConfirmed,
		EventCallbackCancelled: store.StateCancelled,
		EventCallbackTimeout:   store.StateTimeout,
		EventCallbackFailed:    store.StateFailed,
		EventTTLExpired:        store.StateTimeout,
	},
	store.StateConfirmed: {
		EventConsumed:   store.StateConsumed,
		EventTTLExpired: store.StateTimeout,
	},
}

// resultCodeToEvent maps a Safaricom STK Push callback ResultCode (integer)
// to the corresponding session event.
//
// Daraja-documented codes:
//   - 0    — success, payment confirmed
//   - 1032 — request cancelled by user
//   - 1037 — DS timeout, user's SIM unreachable
//
// Any non-zero code not explicitly listed is treated as a generic failure
// and mapped to EventCallbackFailed.
func resultCodeToEvent(code int) Event {
	switch code {
	case 0:
		return EventCallbackSuccess
	case 1032:
		return EventCallbackCancelled
	case 1037:
		return EventCallbackTimeout
	default:
		return EventCallbackFailed
	}
}

// queryResultCodeToEvent maps a Daraja STK Push Query ResultCode (string)
// to the corresponding session event.
//
// Unlike the callback where ResultCode is an integer, the query response
// returns it as a string. Known codes:
//   - "0"             — payment already confirmed
//   - "500.001.1001"  — prompt delivered, awaiting user PIN entry
//   - anything else   — terminal failure or unknown state
func queryResultCodeToEvent(code string) Event {
	switch code {
	case "0":
		return EventCallbackSuccess
	case "500.001.1001":
		return EventQueryAwaitingPIN
	default:
		return EventCallbackFailed
	}
}