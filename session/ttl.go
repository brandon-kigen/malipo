package session

import (
	"context"
	"time"

	"github.com/brandon-kigen/malipo/store"
)

// expireAfter is spawned as a goroutine during InitiatePayment.
// It blocks until the session TTL elapses then fires EventTTLExpired,
// transitioning the session from STK_PUSHED to TIMEOUT.
//
// If the session already reached a terminal state before the TTL fired —
// confirmed, consumed, cancelled, or failed — transition returns
// ErrInvalidTransition which is silently ignored. The session is already
// resolved and no action is needed.
//
// If the Manager is shut down before the TTL elapses, the stopCleanup
// channel is closed and this goroutine exits cleanly without touching storage.
//
// context.Background() is used because this goroutine outlives the HTTP
// request that spawned it — there is no parent request context to inherit.
func (m *Manager) expireAfter(id string, ttl time.Duration) {
	select {
	case <-time.After(ttl):
		err := m.transition(context.Background(), id, store.StateSTKPushed, EventTTLExpired, nil)
		if err != nil {
			return
		}
	case <-m.stopCleanup:
		return
	}
}

// startCleanupTicker runs a background goroutine that bulk-expires stale
// sessions every 30 seconds by calling storage.ExpireStale with the current
// time as the cutoff.
//
// This is the safety net for sessions whose expireAfter goroutine was lost
// across a process restart. SQLite persists the expires_at timestamp so
// ExpireStale can identify and expire them regardless of whether a goroutine
// is still running for each one.
//
// The two-layer expiry design:
//   - expireAfter  — per-session goroutine, fires exactly at TTL (belt)
//   - startCleanupTicker — bulk sweep every 30s, catches missed expirations (braces)
//
// Started automatically by NewManager. Stopped by closing m.stopCleanup
// via Manager.Stop(). Closing a channel unblocks all goroutines waiting
// on it simultaneously — both expireAfter goroutines and the ticker goroutine
// exit cleanly when Stop() is called.
func (m *Manager) startCleanupTicker() {
	// time.NewTicker creates a ticker that fires every 30 seconds.
	// Unlike time.Tick, this can be stopped — ticker.Stop() releases
	// the underlying timer and prevents a goroutine leak.
	ticker := time.NewTicker(30 * time.Second)

	go func() {
		// ticker.Stop() is deferred so it always runs when this
		// goroutine exits — whether from stopCleanup or a panic.
		defer ticker.Stop()

		for {
			select {

			// ticker.C receives a value every 30 seconds.
			// time.Now() is passed as the cutoff — ExpireStale
			// transitions all non-terminal sessions whose
			// expires_at is before this moment to TIMEOUT.
			case <-ticker.C:
				m.storage.ExpireStale(context.Background(), time.Now())

			// m.stopCleanup is closed by Manager.Stop().
			// Closing a channel unblocks all receivers simultaneously —
			// this case fires immediately when Stop() is called,
			// the goroutine returns, and ticker.Stop() runs via defer.
			case <-m.stopCleanup:
				return
			}
		}
	}()
}