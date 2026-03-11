package session

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"time"

	"github.com/brandon-kigen/malipo/store"
	"github.com/oklog/ulid/v2"
)

// PaymentRequest carries the parameters for a single payment initiation.
// Reference and Desc are optional — if empty, Config defaults are used.
type PaymentRequest struct {
	Phone     string
	Amount    int64
	Currency  string
	Reference string // overrides Config.AccountReference if set
	Desc      string // overrides Config.TransactionDesc if set
}

// Config holds everything the Manager needs to operate.
// Provided by the caller at construction time.
type Config struct {
	Shortcode        string
	Passkey          string
	CallbackURL      string
	TTL              time.Duration // defaults to 90s if zero
	AccountReference string        // default reference shown on M-Pesa prompt
	TransactionDesc  string        // default description shown on M-Pesa prompt
}

// Manager owns the state machine rules and orchestrates payment sessions.
// It holds no session state itself — all state lives in the StorageAdapter.
// Both dependencies are injected as interfaces — no concrete types imported.
type Manager struct {
	storage     store.StorageAdapter
	auth        TokenProvider
	cfg         Config
	entropy     io.Reader    // cryptographically secure monotonic ULID entropy
	stopCleanup chan struct{} // closed when Manager shuts down via Stop()
}

// NewManager constructs a Manager with its injected dependencies.
// TTL defaults to 90s if not set — matching Safaricom's STK Push timeout.
// Starts the background cleanup ticker automatically.
func NewManager(auth TokenProvider, storage store.StorageAdapter, cfg Config) *Manager {
	if cfg.TTL == 0 {
		cfg.TTL = 90 * time.Second
	}

	m := &Manager{
		auth:        auth,
		storage:     storage,
		cfg:         cfg,
		entropy:     ulid.Monotonic(rand.Reader, 0),
		stopCleanup: make(chan struct{}),
	}

	m.startCleanupTicker()

	return m
}

// Stop shuts down the cleanup ticker and all expireAfter goroutines.
// Call this when the Manager is no longer needed, typically in a
// server shutdown handler via signal trapping.
func (m *Manager) Stop() {
	close(m.stopCleanup)
}

// GetStatus returns the current state and expiry time of a session.
// The caller uses this to poll during the async gap between STK Push
// initiation and Safaricom's callback.
func (m *Manager) GetStatus(ctx context.Context, id string) (string, time.Time, error) {
	if id == "" {
		return "", time.Time{}, errors.New("missing session id")
	}

	session, err := m.storage.Get(ctx, id)
	if err != nil {
		return "", time.Time{}, err
	}

	return string(session.State), session.ExpiresAt, nil
}

// transition looks up the valid next state for the given event from the
// current state, then delegates the atomic write to the storage adapter.
// It enforces machine-level rules — storage enforces data integrity.
func (m *Manager) transition(ctx context.Context, id string, from store.State, event Event, u *store.Update) error {
	events, ok := validTransitions[from]
	if !ok {
		return store.ErrInvalidTransition
	}

	to, ok := events[event]
	if !ok {
		return store.ErrInvalidTransition
	}

	if err := m.storage.Transition(ctx, id, from, to, u); err != nil {
		return err
	}
	return nil
}

// InitiatePayment starts a new M-Pesa STK Push payment session.
// It normalises the phone number, fetches a Daraja token, creates a
// storage record, sends the STK Push, and spawns a TTL goroutine.
// It returns the session ID which the caller uses to poll GetStatus.
func (m *Manager) InitiatePayment(ctx context.Context, req PaymentRequest) (string, error) {
	// Step 1 — normalise phone to E.164 before anything else
	phone, err := normalizePhone(req.Phone)
	if err != nil {
		return "", err
	}

	// Step 2 — resolve reference and description overrides
	ref := m.cfg.AccountReference
	if req.Reference != "" {
		ref = req.Reference
	}
	desc := m.cfg.TransactionDesc
	if req.Desc != "" {
		desc = req.Desc
	}

	// Step 3 — fetch Daraja token before creating session
	// failure here means no orphaned session record
	token, err := m.auth.GetAccessToken(ctx)
	if err != nil {
		return "", err
	}

	// Step 4 — generate STK Push password and timestamp
	// timestamp is EAT-formatted YYYYMMDDHHmmss — must match token call time
	password, timestamp := m.auth.GeneratePassword(m.cfg.Shortcode, m.cfg.Passkey)
	if password == "" {
		return "", errors.New("failed to generate password")
	}

	// Step 5 — generate session ID and create storage record
	id := ulid.MustNew(ulid.Now(), m.entropy).String()
	now := time.Now()

	session := &store.Session{
		ID:        id,
		State:     store.StateCreated,
		Phone:     phone,
		Amount:    req.Amount,
		Currency:  req.Currency,
		Shortcode: m.cfg.Shortcode,
		CreatedAt: now,
		ExpiresAt: now.Add(m.cfg.TTL),
	}

	if err := m.storage.Create(ctx, session); err != nil {
		return "", err
	}

	// Step 6 — send STK Push to Daraja
	// sendSTKPush is a stub — implemented when auth package is complete
	checkoutID, merchantID, err := m.sendSTKPush(ctx, store.STKPushRequest{
    Token:       token,
    Password:    password,
    Timestamp:   timestamp,
    Phone:       phone,
    Amount:      req.Amount,
    Shortcode:   m.cfg.Shortcode,
    CallbackURL: m.cfg.CallbackURL,
    Reference:   ref,
    Desc:        desc,
})
	if err != nil {
		// transition to FAILED — session exists in storage, must not be left in CREATED
		_ = m.transition(ctx, id, store.StateCreated, EventSTKRejected, nil)
		return "", err
	}

	// Step 7 — transition CREATED → STK_PUSHED with Daraja correlation IDs
	update := &store.Update{
		CheckoutRequestID: &checkoutID,
		MerchantRequestID: &merchantID,
	}
	if err := m.transition(ctx, id, store.StateCreated, EventSTKAccepted, update); err != nil {
		return "", err
	}

	// Step 8 — spawn TTL goroutine to expire session after cfg.TTL
	go m.expireAfter(id, m.cfg.TTL)

	return id, nil
}

// sendSTKPush makes the outbound HTTP call to the Daraja STK Push endpoint.
// TODO: Stub — implemented in Phase 2 when auth package HTTP client is complete.
func (m *Manager) sendSTKPush(ctx context.Context, req store.STKPushRequest) (checkoutID, merchantID string, err error) {
    return m.auth.SendSTKPush(ctx, req)
}