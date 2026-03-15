package session

import "github.com/brandon-kigen/malipo/store"

// Event represents a trigger that causes a session state transition.
type Event string

const (
	EventSTKAccepted       Event = "STK_ACCEPTED"       // Daraja returned 200 to our push request
	EventSTKRejected       Event = "STK_REJECTED"       // Daraja returned error to our push request
	EventCallbackSuccess   Event = "CALLBACK_SUCCESS"   // ResultCode = 0 from Safaricom
	EventCallbackCancelled Event = "CALLBACK_CANCELLED" // ResultCode = 1032
	EventCallbackTimeout   Event = "CALLBACK_TIMEOUT"   // ResultCode = 1037
	EventCallbackFailed    Event = "CALLBACK_FAILED"    // ResultCode any other non-zero
	EventConsumed          Event = "CONSUMED"           // ConsumeIfConfirmed called successfully
	EventTTLExpired        Event = "TTL_EXPIRED"        // 90s elapsed
	EventQueryAwaitingPIN  Event = "QUERY_AWAITING_PIN" // STK Push Query returned 500.001.1001
)

// validTransitions is the source of truth for the state machine.
// Any (state, event) pair not in this map is an invalid transition.
//
// NOTE: AWAITING_PIN is defined in store.State and is reserved for
// when RP19 (STK Push Query API) research is complete. It is not
// reachable in the current machine — STK_PUSHED covers both stages
// until SIM delivery confirmation is implemented.
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
		EventQueryAwaitingPIN:  store.StateAwaitingPIN, // ← new
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

// resultCodeToEvent maps a Safaricom STK Push ResultCode to the corresponding
// session event. Any non-zero code not explicitly mapped is treated as a
// generic failure.
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

// queryResultCodeToEvent maps a Daraja STK Push Query ResultCode string to
// the corresponding session event.
// Unlike the callback where ResultCode is an integer, the query response
// returns it as a string.
//   - "0"            — payment confirmed
//   - "500.001.1001" — prompt delivered, awaiting PIN entry
//   - anything else  — terminal failure
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