// session/token.go
package session

import "context"

// TokenProvider is the interface the Manager uses to get Daraja
// access tokens and generate STK Push passwords.
// Implemented by auth.Manager — defined here to avoid import cycle.
type TokenProvider interface {
	GetAccessToken(ctx context.Context) (string, error)
	GeneratePassword(shortcode, passkey string) (password, timestamp string)
}
