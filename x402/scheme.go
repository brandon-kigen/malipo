// Package x402 implements the x402 HTTP payment protocol for M-Pesa STK Push.
// See package doc in response.go.
package x402

// SchemeName is the x402 scheme identifier for Malipo's M-Pesa integration.
// It appears in every PaymentRequirements payload and identifies this payment
// method to x402-aware clients.
const SchemeName string = "mpesa"

// Network identifies the payment network within the mpesa scheme.
// Scoped to Safaricom Kenya — future providers (MTN MoMo, Airtel Money)
// would register their own network identifiers.
const Network string = "safaricom-ke"

// PaymentHeader is the HTTP header name the client uses when retrying a
// previously gated request with proof of payment. The value is the session
// ID returned in the original 402 response.
const PaymentHeader string = "X-PAYMENT"

// PaymentRequirements describes what payment a server requires before
// granting access to a gated resource. It is the payload inside the
// Response402 Accepts slice and follows the x402 scheme format.
//
// SessionID ties this payment requirement to a specific STK Push session
// created at 402 time. The client must include this ID in the X-PAYMENT
// header when retrying after payment is confirmed.
//
// RetryAfter is a hint to the client in seconds — how long to wait before
// polling the session status endpoint or retrying the request.
type PaymentRequirements struct {
	Scheme      string `json:"scheme"`
	Network     string `json:"network"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	Resource    string `json:"resource"`
	Description string `json:"description"`
	PayTo       string `json:"payTo"`
	SessionID   string `json:"sessionId"`
	RetryAfter  int    `json:"retryAfter"`
}

// PaymentProof is the structured representation of the value the client
// sends in the X-PAYMENT header when retrying a gated request.
// Defined as a struct rather than a bare string to accommodate future
// schemes that may carry additional proof fields such as signatures or
// receipt numbers.
type PaymentProof struct {
	SessionID string `json:"sessionId"`
}
