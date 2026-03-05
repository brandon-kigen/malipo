package store

import "time"

// Session is the single record that tracks a payment attempt
// from initiation through to consumption or failure.
type Session struct {
	ID        string // ULID — lexicographic, URL-safe, time-ordered
	State     State
	Phone     string // E.164 format — "+254712345678"
	Amount    int64  // KES, whole units
	Currency  string // "KES"
	Shortcode string // M-Pesa business shortcode

	// Populated after STK Push is accepted by Daraja
	CheckoutRequestID string // Daraja's UUID — callback correlation key
	MerchantRequestID string

	// Populated after Safaricom confirms payment (callback)
	MpesaReceiptNumber string
	ConfirmedAmount    *int64  // pointer — nil until confirmed
	ConfirmedPhone     *string // pointer — nil until confirmed

	// Timestamps
	CreatedAt  time.Time
	ExpiresAt  time.Time  // CreatedAt + TTL (default 90s)
	ConsumedAt *time.Time // pointer — nil until consumed
}

// Update carries only the fields that change after a session is created.
// Transition() and ConsumeIfConfirmed() receive an *Update.
// Fields are pointers — nil means "do not update this field".
type Update struct {
	CheckoutRequestID  *string
	MerchantRequestID  *string
	MpesaReceiptNumber *string
	ConfirmedAmount    *int64
	ConfirmedPhone     *string
	ConsumedAt         *time.Time
}
