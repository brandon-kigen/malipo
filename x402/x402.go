// Package x402 implements the x402 HTTP payment protocol for M-Pesa STK Push.
// It provides middleware that gates net/http handlers behind a real mobile money
// payment, following the x402 spec's scheme-based payment requirements model.
//
// The flow is:
//   - Client requests a gated resource with no proof of payment
//   - Middleware initiates an STK Push and returns 402 with payment requirements
//   - Client polls for session confirmation then retries with X-PAYMENT header
//   - Middleware verifies the session and serves the resource
package x402

import (
	"net/http"

	"github.com/brandon-kigen/malipo/session"
	"github.com/brandon-kigen/malipo/store"
)

// GateOptions configures the Gate middleware for a specific protected resource.
// It is constructed once at server startup and shared across all requests to
// the gated handler.
type GateOptions struct {
	// Amount is the payment amount in the smallest currency unit (e.g. cents for KES).
	Amount int64

	// Currency is the ISO 4217 currency code. Defaults to "KES" if empty.
	Currency string

	// Description is the human-readable payment description shown in the
	// STK Push prompt on the user's phone.
	Description string

	// Shortcode is the M-Pesa paybill or till number that receives the payment.
	// Appears as PayTo in the 402 response.
	Shortcode string

	// Manager is the session manager used to initiate STK Push and verify
	// payment state. Must not be nil.
	Manager *session.Manager

	// PhoneExtractor extracts the payer's phone number from the incoming request.
	// The implementation is supplied by the developer and may read from a form
	// value, JWT claim, header, or any other source appropriate to their
	// architecture. Must not be nil. Returning an error causes the middleware
	// to respond with 400 before any payment is attempted.
	PhoneExtractor func(r *http.Request) (string, error)
}

// buildRequirements assembles a PaymentRequirements payload from the gate
// configuration and the live request. The resource URL is reconstructed from
// the request so it reflects the actual endpoint being gated rather than a
// hardcoded value — allowing a single middleware instance to gate multiple routes.
func buildRequirements(opts GateOptions, r *http.Request, sessionID string) PaymentRequirements {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	resource := scheme + "://" + r.Host + r.URL.RequestURI()

	return PaymentRequirements{
		Scheme:      SchemeName,
		Network:     Network,
		Amount:      opts.Amount,
		Currency:    opts.Currency,
		Resource:    resource,
		Description: opts.Description,
		PayTo:       opts.Shortcode,
		SessionID:   sessionID,
		RetryAfter:  5,
	}
}

// Gate returns an x402 payment middleware that gates access to the wrapped
// handler behind a real M-Pesa STK Push payment.
//
// Panics at startup if Manager or PhoneExtractor are nil — these are
// programming errors that must be caught before the server accepts requests.
//
// On each request the middleware follows three branches:
//
//  1. X-PAYMENT header present — verifies the session is CONFIRMED, consumes
//     it atomically, and passes the request to the next handler.
//
//  2. No header, phone extraction fails — returns 400. No payment is attempted.
//     This indicates the phone number was not available on this request — the
//     developer's PhoneExtractor should be checked.
//
//  3. No header, phone extraction succeeds — initiates STK Push via the session
//     manager and returns a 402 with payment requirements. The session ID in the
//     response body is what the client must send in X-PAYMENT on retry.
func Gate(opts GateOptions) func(http.Handler) http.Handler {
	if opts.Manager == nil {
		panic("x402: GateOptions.Manager must not be nil")
	}
	if opts.PhoneExtractor == nil {
		panic("x402: GateOptions.PhoneExtractor must not be nil")
	}
	if opts.Currency == "" {
		opts.Currency = "KES"
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			header := r.Header.Get(PaymentHeader)

			// Branch 1 — proof of payment present.
			if header != "" {
				sessionID := header

				state, _, err := opts.Manager.GetStatus(ctx, sessionID)
				if err != nil {
					w.WriteHeader(http.StatusPaymentRequired)
					return
				}
				if state != string(store.StateConfirmed) {
					w.WriteHeader(http.StatusPaymentRequired)
					return
				}

				if err = opts.Manager.ConsumeIfConfirmed(ctx, sessionID); err != nil {
					w.WriteHeader(http.StatusPaymentRequired)
					return
				}

				next.ServeHTTP(w, r)
				return
			}

			// Branch 2 — no proof, extract phone.
			phone, err := opts.PhoneExtractor(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			// Branch 3 — initiate payment and return 402.
			requirements := session.PaymentRequest{
				Phone:    phone,
				Amount:   opts.Amount,
				Desc:     opts.Description,
				Currency: opts.Currency,
			}

			sessionID, err := opts.Manager.InitiatePayment(ctx, requirements)
			if err != nil {
				http.Error(w, "payment initiation failed", http.StatusInternalServerError)
				return
			}

			reqs := buildRequirements(opts, r, sessionID)
			if err = Write402(w, reqs); err != nil {
				return
			}
		})
	}
}