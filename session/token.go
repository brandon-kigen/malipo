package session

import (
	"context"
	"github.com/brandon-kigen/malipo/store"
)

// TokenProvider is the interface the Manager uses to get Daraja
// access tokens, generate STK Push passwords, and send STK Pushes.
// Implemented by auth.Manager — defined here to avoid import cycle.
type TokenProvider interface {
	GetAccessToken(ctx context.Context) (string, error)
	GeneratePassword(shortcode, passkey string) (password, timestamp string)
	SendSTKPush(ctx context.Context, req store.STKPushRequest) (checkoutID, merchantID string, err error)
}
