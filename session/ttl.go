package session

import (
	"context"
	"log"
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

// startCleanupTicker runs a background goroutine that fires every 30 seconds.
// On each tick it runs two passes in order:
//
//  1. Recovery pass — queries Daraja for all sessions in STK_PUSHED or
//     AWAITING_PIN whose CreatedAt is older than QueryThreshold. For each,
//     it calls QuerySTKStatus and drives the state machine from the result.
//     Sessions that cannot be queried are left alone — ExpireStale will
//     catch them on TTL expiry.
//
//  2. Expiry pass — calls ExpireStale to transition any non-terminal session
//     whose expires_at is in the past to TIMEOUT. This is the safety net for
//     sessions that neither the callback nor the recovery pass resolved.
//
// Started automatically by NewManager. Stopped by closing m.stopCleanup
// via Manager.Stop().
func (m *Manager) startCleanupTicker() {
	ticker := time.NewTicker(30 * time.Second)

	go func() {
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				ctx := context.Background()
				m.runRecovery(ctx)
				m.storage.ExpireStale(ctx, time.Now())

			case <-m.stopCleanup:
				return
			}
		}
	}()
}

// runRecovery queries Daraja for all pending sessions older than QueryThreshold
// and drives each one to its correct next state based on the query result.
// Errors from individual sessions are logged and skipped — a single failed
// query must not abort the entire recovery pass.
func (m *Manager) runRecovery(ctx context.Context) {
	threshold := time.Now().Add(-m.cfg.QueryThreshold)

	sessions, err := m.storage.ListPending(ctx, threshold)
	if err != nil {
		log.Printf("session: recovery: list pending failed: %v", err)
		return
	}

	for _, s := range sessions {
		if s.CheckoutRequestID == "" {
			// Session is in STK_PUSHED but Daraja never returned a
			// CheckoutRequestID — STK Push was accepted but correlation
			// ID was lost. Cannot query without it. Leave for ExpireStale.
			continue
		}

		resultCode, _, err := m.auth.QuerySTKStatus(
			ctx,
			m.cfg.Shortcode,
			m.cfg.Passkey,
			s.CheckoutRequestID,
		)
		if err != nil {
			log.Printf("session: recovery: query failed for %s: %v", s.CheckoutRequestID, err)
			continue
		}

		event := queryResultCodeToEvent(resultCode)

		if err := m.transition(ctx, s.ID, s.State, event, nil); err != nil {
			// ErrInvalidTransition here means the session was already
			// resolved by a late-arriving callback between ListPending
			// and this transition — expected, not an error worth logging.
			if err != store.ErrInvalidTransition {
				log.Printf("session: recovery: transition failed for %s: %v", s.ID, err)
			}
		}
	}
}
