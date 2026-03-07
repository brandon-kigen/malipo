package store

import (
	"context"
	"time"
)

// Adapter is an interface that defines the methods for interacting with a data store.
type StorageAdapter interface {
	Create(ctx context.Context, s *Session) error
	Get(ctx context.Context, id string) (*Session, error)
	GetByCheckoutID(ctx context.Context, checkoutID string) (*Session, error)
	Transition(ctx context.Context, id string, from, to State, u *Update) error
	ConsumeIfConfirmed(ctx context.Context, id string) (*Session, error)
	ExpireStale(ctx context.Context, before time.Time) (int64, error)
}
