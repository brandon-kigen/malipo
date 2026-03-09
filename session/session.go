package session

import (
	"context"
	"errors"
	"time"

	"github.com/brandon-kigen/malipo/store"
)

// Config holds everything the Manager needs to operate.
// Provided by the caller at construction time.
type Config struct {
	Shortcode   string
	Passkey     string
	CallbackURL string
	TTL         time.Duration // defaults to 90s if zero
}

// Manager owns the state machine rules and orchestrates
// payment sessions. It holds no session state itself —
// all state lives in the StorageAdapter.
type Manager struct {
	storage store.StorageAdapter
	auth    TokenProvider
	cfg     Config
}

// NewManager constructs a Manager with its two injected dependencies.
// Both are interfaces — no concrete types imported here.
func NewManager(auth TokenProvider, storage store.StorageAdapter, cfg Config) *Manager {
	if cfg.TTL == 0 {
		cfg.TTL = 90 * time.Second
	}
	return &Manager{
		auth:    auth,
		storage: storage,
		cfg:     cfg,
	}
}

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
